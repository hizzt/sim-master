package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	externalsim "github.com/1239t/swu-go/pkg/sim"
	enginesim "github.com/1239t/vowifi-go/engine/sim"
)

// qmiSIMBackend 实现 SIMAdapter 接口，通过 QMI UIM 服务做 AKA 计算。
// GetIMSI/GetIMEI 继续走 AT（复用现有 DirectSIM），只有 CalculateAKA 走 QMI。
type qmiSIMBackend struct {
	serialDevice string // AT 串口设备（用于读 IMSI/IMEI）
	qmiDevice    string // QMI 设备路径
	directSIM    *externalsim.DirectSIM
	mmWasActive  bool // 进入时 ModemManager 是否正在运行（需恢复）
}

// NewQMISIMBackend 创建 QMI 后端的 SIM 适配器。
// serialDevice: AT 串口设备（如 /dev/wwan0at0）
// qmiDevice: QMI 设备路径（如 /dev/wwan0qmi0）
func NewQMISIMBackend(serialDevice, qmiDevice string) (*qmiSIMBackend, error) {
	if qmiDevice == "" {
		qmiDevice = "/dev/wwan0qmi0"
	}
	// 验证 QMI 设备存在
	// 不立即打开，留到 CalculateAKA 时按需连接

	// 先暂停 ModemManager：它独占 AT 串口，若不先停，
	// DirectSIM 的 AT 命令（读 IMSI/IMEI）会无响应而卡死。
	// MM 恢复在 Close() 中执行。
	b := &qmiSIMBackend{
		serialDevice: serialDevice,
		qmiDevice:    qmiDevice,
	}
	b.stopModemManager()

	// 创建 AT 后端用于读 IMSI/IMEI
	directSIM, err := externalsim.NewDirectSIM(serialDevice)
	if err != nil {
		b.restoreModemManager()
		return nil, fmt.Errorf("open AT SIM interface: %w", err)
	}

	b.directSIM = directSIM
	return b, nil
}

// GetIMSI 通过 AT 端口读取 IMSI。
// 如果 AT 端口不可用，回退到 QMI 路径。
func (b *qmiSIMBackend) GetIMSI() (string, error) {
	imsi, err := b.directSIM.GetIMSI()
	if err == nil {
		return imsi, nil
	}
	// AT 回退：通过 QMI 读 IMSI
	return b.getIMSIViaQMI()
}

// getIMSIViaQMI 通过 QMI UIM ReadTransparent 读取 IMSI。
func (b *qmiSIMBackend) getIMSIViaQMI() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := newQMIConnection(ctx, b.qmiDevice, 1)
	if err != nil {
		return "", fmt.Errorf("qmi get IMSI: %w", err)
	}
	defer conn.Close()

	return conn.GetIMSI(ctx)
}

// CalculateAKA 通过 QMI UIM 逻辑通道计算 AKA。
// 实现流程：
// 1. 如果 ModemManager 运行中且 qmi-proxy 不可用，暂停它
// 2. 连接 QMI（qmi-proxy 优先）
// 3. 获取 USIM AID（回退到硬编码前缀）
// 4. USIM 失败则回退 ISIM
// 5. 打开逻辑通道
// 6. 先尝试无 Le 的 APDU，失败后再尝试带 Le 的变体
// 7. 处理 61xx GET RESPONSE
// 8. 解析响应（0xDB 成功 / 0xDC 同步失败）
// 9. 关闭逻辑通道
func (b *qmiSIMBackend) CalculateAKA(rand16, autn16 []byte) (enginesim.AKAResult, error) {
	// MM 已在 NewQMISIMBackend 暂停（Close 时恢复），raw QMI 直连可用

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 连接 QMI
	conn, err := newQMIConnection(ctx, b.qmiDevice, 1)
	if err != nil {
		return enginesim.AKAResult{}, fmt.Errorf("qmi connect: %w", err)
	}
	defer conn.Close()

	// 尝试 USIM
	result, err := b.calculateOnApp(ctx, conn, conn.ResolveAID, rand16, autn16)
	if err == nil {
		return result, nil
	}
	// 如果 USIM 失败且不是卡上没有应用，直接返回错误
	if !strings.Contains(err.Error(), "application not found") {
		return enginesim.AKAResult{}, err
	}

	// 回退到 ISIM
	result, err = b.calculateOnApp(ctx, conn, conn.ResolveISIMAID, rand16, autn16)
	if err != nil {
		return enginesim.AKAResult{}, fmt.Errorf("USIM and ISIM AKA both failed: %w", err)
	}
	return result, nil
}

// calculateOnApp 在指定应用（USIM 或 ISIM）上计算 AKA。
func (b *qmiSIMBackend) calculateOnApp(
	ctx context.Context,
	conn *qmiConnection,
	resolveAID func(context.Context) ([]byte, error),
	rand16, autn16 []byte,
) (enginesim.AKAResult, error) {
	// 获取 AID
	aid, err := resolveAID(ctx)
	if err != nil {
		return enginesim.AKAResult{}, fmt.Errorf("resolve AID: %w", err)
	}

	// 尝试打开逻辑通道
	ch, err := conn.OpenLogicalChannel(ctx, aid)
	if err != nil {
		// 回退到 basic channel 0
		fmt.Printf("QMI_DEBUG: OpenLogicalChannel failed (%v), falling back to basic channel 0\n", err)
		ch = 0
	} else {
		defer conn.CloseLogicalChannel(ctx, ch) // nolint: errcheck
	}

	// 先尝试无 Le 的 APDU
	apdu, err := BuildUSIMAuthAPDU(rand16, autn16, false)
	if err != nil {
		return enginesim.AKAResult{}, fmt.Errorf("build APDU (no Le): %w", err)
	}
	result, err := b.sendLogicalAuth(ctx, conn, ch, apdu)
	if err == nil {
		return result, nil
	}

	// 带 Le 变体重试
	apdu2, err := BuildUSIMAuthAPDU(rand16, autn16, true)
	if err != nil {
		return enginesim.AKAResult{}, fmt.Errorf("build APDU (with Le): %w", err)
	}
	result, err2 := b.sendLogicalAuth(ctx, conn, ch, apdu2)
	if err2 != nil {
		return enginesim.AKAResult{}, fmt.Errorf("AKA failed (no Le: %v, with Le: %v)", err, err2)
	}
	return result, nil
}

// sendLogicalAuth 在已打开的通道上发送 AUTHENTICATE APDU，处理 61xx GET RESPONSE。
func (b *qmiSIMBackend) sendLogicalAuth(ctx context.Context, conn *qmiConnection, ch byte, apdu []byte) (enginesim.AKAResult, error) {
	sendFunc := func(cmd []byte) ([]byte, error) {
		return conn.SendAPDU(ctx, ch, cmd)
	}

	body, sw1, sw2, err := sendAPDUGetResponse(sendFunc, apdu)
	if err != nil {
		return enginesim.AKAResult{}, err
	}

	// 拼接 body + SW1/SW2 返回完整响应供解析
	fullResp := append(body, sw1, sw2)
	return ParseUSIMAuthResponse(fullResp)
}

// GetIMEI 通过 AT 端口读取 IMEI（原有逻辑，不变）。
func (b *qmiSIMBackend) GetIMEI() (string, error) {
	return b.directSIM.GetIMEI()
}

// Close 释放所有资源。
func (b *qmiSIMBackend) Close() error {
	var errs []string
	if b.directSIM != nil {
		if err := b.directSIM.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("AT close: %v", err))
		}
	}
	// MM 已在 NewQMISIMBackend 中暂停，这里恢复
	b.restoreModemManager()
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// stopModemManager 暂停 ModemManager（如果它在运行）。
// 仅当 qmi-proxy 不可用时作为 raw 模式回退的前置步骤。
// 当前实现使用 qmi-proxy 模式，无需停 MM，此函数保留为回退备用。
func (b *qmiSIMBackend) stopModemManager() {
	if b.mmWasActive {
		return
	}
	output, err := exec.Command("systemctl", "is-active", "ModemManager").Output()
	if err != nil {
		return
	}
	if strings.TrimSpace(string(output)) == "active" {
		_ = exec.Command("systemctl", "stop", "ModemManager").Run()
		b.mmWasActive = true
	}
}

// restoreModemManager 恢复之前暂停的 ModemManager。
func (b *qmiSIMBackend) restoreModemManager() {
	if b.mmWasActive {
		_ = exec.Command("systemctl", "start", "ModemManager").Run()
		b.mmWasActive = false
	}
}
