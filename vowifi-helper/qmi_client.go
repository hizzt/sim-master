package main

import (
	"context"
	"fmt"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

// qmiConnection 封装 QMI 客户端连接和 UIM 服务。
// 连接策略：qmi-proxy 优先，raw 回退。
type qmiConnection struct {
	client      *qmi.Client
	uim         *qmi.UIMService
	uimClientID byte          // 自己分配的 UIM clientID（用于 SendRequest）
	slot        byte
	devicePath  string
	initialized bool
}

// defaultProxyPath 是 qmi-proxy 抽象 socket 路径。
// libqmi ≥1.18 默认监听 @qmi-proxy 抽象 Unix socket。
const defaultProxyPath = "@qmi-proxy"

// newQMIWithProxy 通过 qmi-proxy 优先连接 QMI 设备。
// 连接策略：qmi-proxy → raw 回退。
func newQMIWithProxy(ctx context.Context, devicePath string) (*qmi.Client, error) {
	if devicePath == "" {
		devicePath = "/dev/wwan0qmi0"
	}
	opts := qmi.DefaultClientOptions()
	opts.UseProxy = true
	opts.ProxyPath = defaultProxyPath
	opts.ProxyFallbackToRaw = true
	opts.SyncOnOpen = true
	opts.QueryVersionOnOpen = false
	opts.DefaultRequestTimeout = 30 * time.Second
	opts.ProxyOpenTimeout = 10 * time.Second

	client, err := qmi.NewClientWithOptions(ctx, devicePath, opts)
	if err != nil {
		return nil, fmt.Errorf("qmi connect to %s: %w", devicePath, err)
	}
	return client, nil
}

// newQMIConnection 创建 QMI 连接并初始化 UIM 服务。
func newQMIConnection(ctx context.Context, qmiDevice string) (*qmiConnection, error) {
	if qmiDevice == "" {
		qmiDevice = "/dev/wwan0qmi0"
	}

	client, err := newQMIWithProxy(ctx, qmiDevice)
	if err != nil {
		return nil, err
	}

	// 创建 UIMService（用于 GetUSIMAID/GetIMSI 等高级方法）
	uim, err := qmi.NewUIMServiceWithContext(ctx, client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("uim service: %w", err)
	}

	// 自分配一个 UIM clientID（用于我们自己的 SendRequest 调用）
	myClientID, err := client.AllocateClientIDWithContext(ctx, qmi.ServiceUIM)
	if err != nil {
		uim.Close()
		client.Close()
		return nil, fmt.Errorf("allocate uim client id: %w", err)
	}

	conn := &qmiConnection{
		client:      client,
		uim:         uim,
		uimClientID: myClientID,
		slot:        0, // 默认卡槽 0（QMI 0-based）
		devicePath:  qmiDevice,
	}
	conn.initialized = true
	return conn, nil
}

// Close 释放 QMI 资源。
func (c *qmiConnection) Close() error {
	if c.client != nil {
		// 释放我们自己的 clientID
		_ = c.client.ReleaseClientID(qmi.ServiceUIM, c.uimClientID)
	}
	if c.uim != nil {
		_ = c.uim.Close()
	}
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// ResolveAID 获取 USIM 的完整 AID。
// 优先通过 GetCardStatus 获取完整 AID，失败回退到硬编码前缀。
func (c *qmiConnection) ResolveAID(ctx context.Context) ([]byte, error) {
	aid, err := c.uim.GetUSIMAID(ctx)
	if err == nil && len(aid) > 0 {
		return aid, nil
	}
	// 回退：ISO 7816-4 部分 AID 选择，使用 7 字节前缀
	fallback := []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02}
	return fallback, nil
}

// ResolveISIMAID 获取 ISIM 的完整 AID。
func (c *qmiConnection) ResolveISIMAID(ctx context.Context) ([]byte, error) {
	aid, err := c.uim.GetISIMAID(ctx)
	if err == nil && len(aid) > 0 {
		return aid, nil
	}
	fallback := []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04}
	return fallback, nil
}

// sessionInfoTLV 构造正确的 Session Information TLV（slot + session_type）。
// QMI UIM 规范要求 TLV 0x01 包含 2 字节：slot + session_type。
// session_type = 0（Primary GW Provisioning）。
func sessionInfoTLV(slot byte) qmi.TLV {
	return qmi.TLV{Type: 0x01, Value: []byte{slot, 0x00}}
}

// OpenLogicalChannel 打开 USIM 逻辑通道，使用正确的 Session Information TLV。
func (c *qmiConnection) OpenLogicalChannel(ctx context.Context, aid []byte) (byte, error) {
	aidValue := append([]byte{byte(len(aid))}, aid...)
	tlvs := []qmi.TLV{
		{Type: 0x10, Value: aidValue}, // AID: len + data
		sessionInfoTLV(c.slot),         // Session info: slot + session_type
	}

	resp, err := c.client.SendRequest(ctx, qmi.ServiceUIM, c.uimClientID, qmi.UIMOpenLogicalChannel, tlvs)
	if err != nil {
		return 0, fmt.Errorf("open logical channel: %w", err)
	}
	return parseOpenLogicalChannelResponse(resp)
}

// parseOpenLogicalChannelResponse 解析 OpenLogicalChannel 响应。
func parseOpenLogicalChannelResponse(resp *qmi.Packet) (byte, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("open logical channel: %w", err)
	}
	tlv := qmi.FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 1 {
		return 0, fmt.Errorf("open logical channel response missing channel ID TLV")
	}
	return tlv.Value[0], nil
}

// CloseLogicalChannel 关闭逻辑通道，使用正确的 Session Information TLV。
func (c *qmiConnection) CloseLogicalChannel(ctx context.Context, channel byte) error {
	tlvs := []qmi.TLV{
		sessionInfoTLV(c.slot),               // Session info: slot + session_type
		{Type: 0x11, Value: []byte{channel}}, // Channel ID
		{Type: 0x13, Value: []byte{0x01}},    // 标志
	}

	resp, err := c.client.SendRequest(ctx, qmi.ServiceUIM, c.uimClientID, qmi.UIMCloseLogicalChannel, tlvs)
	if err != nil {
		return fmt.Errorf("close logical channel: %w", err)
	}
	return parseCloseLogicalChannelResponse(resp)
}

// parseCloseLogicalChannelResponse 解析 CloseLogicalChannel 响应。
func parseCloseLogicalChannelResponse(resp *qmi.Packet) error {
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("close logical channel: %w", err)
	}
	return nil
}

// SendAPDU 在逻辑通道上发送 APDU，使用正确的 Session Information TLV。
func (c *qmiConnection) SendAPDU(ctx context.Context, channel byte, apdu []byte) ([]byte, error) {
	length := len(apdu)
	value := make([]byte, 2+len(apdu))
	value[0] = byte(length & 0xFF)
	value[1] = byte(length >> 8)
	copy(value[2:], apdu)

	tlvs := []qmi.TLV{
		{Type: 0x10, Value: []byte{channel}}, // Channel ID
		{Type: 0x02, Value: value},           // APDU data: len + data
		sessionInfoTLV(c.slot),                // Session info: slot + session_type
	}

	resp, err := c.client.SendRequest(ctx, qmi.ServiceUIM, c.uimClientID, qmi.UIMSendAPDU, tlvs)
	if err != nil {
		return nil, fmt.Errorf("send apdu: %w", err)
	}
	return parseSendAPDUResponse(resp)
}

// parseSendAPDUResponse 解析 SendAPDU 响应。
func parseSendAPDUResponse(resp *qmi.Packet) ([]byte, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("send apdu: %w", err)
	}
	tlv := qmi.FindTLV(resp.TLVs, 0x02)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("APDU response TLV missing or too short")
	}
	responseLen := int(binaryLittleEndianUint16(tlv.Value[0:2]))
	if len(tlv.Value) < 2+responseLen {
		return nil, fmt.Errorf("APDU response truncated: need %d have %d", 2+responseLen, len(tlv.Value))
	}
	result := make([]byte, responseLen)
	copy(result, tlv.Value[2:2+responseLen])
	return result, nil
}

// binaryLittleEndianUint16 读取小端 UINT16。
func binaryLittleEndianUint16(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return uint16(b[0]) | uint16(b[1])<<8
}

// GetIMSI 通过 QMI 读取 IMSI。
func (c *qmiConnection) GetIMSI(ctx context.Context) (string, error) {
	return c.uim.GetIMSI(ctx)
}