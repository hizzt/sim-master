package main

import (
	"errors"

	externalsim "github.com/1239t/swu-go/pkg/sim"
	enginesim "github.com/1239t/vowifi-go/engine/sim"
)

// SIMBackend 定义 SIM 后端统一接口。
// AT 后端和 QMI 后端都实现此接口，main.go 通过工厂选择。
type SIMBackend interface {
	GetIMSI() (string, error)
	GetIMEI() (string, error)
	CalculateAKA(rand16, autn16 []byte) (enginesim.AKAResult, error)
	Close() error
}

type simAdapter struct {
	inner *externalsim.DirectSIM
}

func (a *simAdapter) GetIMSI() (string, error) {
	return a.inner.GetIMSI()
}

func (a *simAdapter) GetIMEI() (string, error) {
	return a.inner.GetIMEI()
}

func (a *simAdapter) CalculateAKA(rand16, autn16 []byte) (enginesim.AKAResult, error) {
	res, ck, ik, auts, err := a.inner.CalculateAKA(rand16, autn16)
	result := enginesim.AKAResult{RES: res, CK: ck, IK: ik, AUTS: auts}
	if errors.Is(err, externalsim.ErrSyncFailure) {
		return result, enginesim.ErrSyncFailure
	}
	return result, err
}

func (a *simAdapter) Close() error {
	return a.inner.Close()
}

// NewATSIMBackend 创建 AT 后端（原有逻辑）。
func NewATSIMBackend(serialDevice string) (SIMBackend, error) {
	if serialDevice == "" {
		return nil, errors.New("serial device is required for AT backend")
	}
	directSIM, err := externalsim.NewDirectSIM(serialDevice)
	if err != nil {
		return nil, err
	}
	return &simAdapter{inner: directSIM}, nil
}

// NewSIMBackend 根据后端类型创建 SIM 后端实例。
// backend: "at"（默认）或 "qmi"
// serialDevice: AT 串口设备路径
// qmiDevice: QMI 设备路径（仅 qmi 后端使用）
func NewSIMBackend(backend, serialDevice, qmiDevice string) (SIMBackend, error) {
	switch backend {
	case "qmi":
		return NewQMISIMBackend(serialDevice, qmiDevice)
	default:
		return NewATSIMBackend(serialDevice)
	}
}
