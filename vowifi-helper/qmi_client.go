package main

import (
	"context"
	"fmt"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

// qmiConnection 封装 QMI 客户端连接和 UIM 服务。
type qmiConnection struct {
	client     *qmi.Client
	uim        *qmi.UIMService
	slot       byte
	devicePath string
	initialized bool
}

const defaultProxyPath = "@qmi-proxy"

// newQMIWithProxy 通过 qmi-proxy 优先连接 QMI 设备。
func newQMIWithProxy(ctx context.Context, devicePath string) (*qmi.Client, error) {
	if devicePath == "" {
		devicePath = "/dev/wwan0qmi0"
	}
	opts := qmi.DefaultClientOptions()
	opts.UseProxy = false
	opts.SyncOnOpen = true
	opts.QueryVersionOnOpen = false
	opts.DefaultRequestTimeout = 30 * time.Second

	client, err := qmi.NewClientWithOptions(ctx, devicePath, opts)
	if err != nil {
		return nil, fmt.Errorf("qmi connect to %s: %w", devicePath, err)
	}
	return client, nil
}

// newQMIConnection 创建 QMI 连接并初始化 UIM 服务。
// 参数 slot 是 QMI 卡槽编号（1-based，与 qmicli Slot [1] 一致）。
func newQMIConnection(ctx context.Context, qmiDevice string, slot byte) (*qmiConnection, error) {
	if qmiDevice == "" {
		qmiDevice = "/dev/wwan0qmi0"
	}

	client, err := newQMIWithProxy(ctx, qmiDevice)
	if err != nil {
		return nil, err
	}

	uim, err := qmi.NewUIMServiceWithContext(ctx, client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("uim service: %w", err)
	}

	conn := &qmiConnection{
		client:     client,
		uim:        uim,
		slot:       slot,
		devicePath: qmiDevice,
	}
	conn.initialized = true
	return conn, nil
}

// Close 释放 QMI 资源。
func (c *qmiConnection) Close() error {
	if c.uim != nil {
		_ = c.uim.Close()
	}
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// ResolveAID 获取 USIM 的完整 AID。
func (c *qmiConnection) ResolveAID(ctx context.Context) ([]byte, error) {
	aid, err := c.uim.GetUSIMAID(ctx)
	if err == nil && len(aid) > 0 {
		// 调试日志：打印解析到的 AID
		fmt.Printf("QMI_DEBUG: USIM AID via GetCardStatus: % X (len=%d)\n", aid, len(aid))
		return aid, nil
	}
	fmt.Printf("QMI_DEBUG: GetUSIMAID failed: %v, fallback to prefix\n", err)
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

// OpenLogicalChannel 打开 USIM 逻辑通道，直接使用库的原始实现（不修改 TLV）。
func (c *qmiConnection) OpenLogicalChannel(ctx context.Context, aid []byte) (byte, error) {
	fmt.Printf("QMI_DEBUG: OpenLogicalChannel slot=%d aid=% X (len=%d)\n", c.slot, aid, len(aid))
	ch, err := c.uim.OpenLogicalChannel(ctx, c.slot, aid)
	if err != nil {
		fmt.Printf("QMI_DEBUG: OpenLogicalChannel FAILED: %v\n", err)
		return 0, err
	}
	fmt.Printf("QMI_DEBUG: OpenLogicalChannel OK channel=%d\n", ch)
	return ch, nil
}

// CloseLogicalChannel 关闭逻辑通道，直接使用库的原始实现。
func (c *qmiConnection) CloseLogicalChannel(ctx context.Context, channel byte) error {
	return c.uim.CloseLogicalChannel(ctx, c.slot, channel)
}

// SendAPDU 发送 APDU，直接使用库的原始实现。
func (c *qmiConnection) SendAPDU(ctx context.Context, channel byte, apdu []byte) ([]byte, error) {
	fmt.Printf("QMI_DEBUG: SendAPDU slot=%d channel=%d apdu=% X\n", c.slot, channel, apdu)
	resp, err := c.uim.SendAPDU(ctx, c.slot, channel, apdu)
	if err != nil {
		fmt.Printf("QMI_DEBUG: SendAPDU FAILED: %v\n", err)
		return nil, err
	}
	fmt.Printf("QMI_DEBUG: SendAPDU OK response=% X\n", resp)
	return resp, nil
}

// GetIMSI 通过 QMI 读取 IMSI。
func (c *qmiConnection) GetIMSI(ctx context.Context) (string, error) {
	return c.uim.GetIMSI(ctx)
}

// PowerOnAndActivateSession 确保 SIM 卡通电并激活 provisioning session。
// 某些 modem 在 APDU 操作前需要先建立 session。
func (c *qmiConnection) PowerOnAndActivateSession(ctx context.Context, aid []byte) error {
	// 1. PowerOn SIM
	if err := c.uim.PowerOnSIM(ctx, c.slot); err != nil {
		fmt.Printf("QMI_DEBUG: PowerOnSIM failed (may be already on): %v\n", err)
		// 不返回错误——可能已通电
	}

	// 2. ChangeProvisioningSession: 激活 Primary GW 会话
	req := qmi.UIMChangeProvisioningSessionRequest{
		SessionType:           0, // Primary GW Provisioning
		Activate:              true,
		ApplicationIdentifier: aid,
	}
	if err := c.uim.ChangeProvisioningSession(ctx, req); err != nil {
		fmt.Printf("QMI_DEBUG: ChangeProvisioningSession failed: %v\n", err)
		return fmt.Errorf("activate session: %w", err)
	}
	fmt.Printf("QMI_DEBUG: Provisioning session activated\n")
	return nil
}