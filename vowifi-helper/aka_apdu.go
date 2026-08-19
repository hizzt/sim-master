package main

import (
	"errors"
	"fmt"

	enginesim "github.com/1239t/vowifi-go/engine/sim"
)

// BuildUSIMAuthAPDU 构造 USIM AKA 鉴权 APDU（RAND/AUTN）。
// includeLe=true 时在尾部追加 0x00（Le），否则不追加。
// 格式: CLA=0x00 INS=0x88 P1=0x00 P2=0x81 Lc [TAG=0x10 RAND[16] TAG=0x10 AUTN[16]] [Le]
func BuildUSIMAuthAPDU(rand16, autn16 []byte, includeLe bool) ([]byte, error) {
	if len(rand16) != 16 {
		return nil, fmt.Errorf("RAND length must be 16 bytes: %d", len(rand16))
	}
	if len(autn16) != 16 {
		return nil, fmt.Errorf("AUTN length must be 16 bytes: %d", len(autn16))
	}

	authData := make([]byte, 0, 1+16+1+16)
	authData = append(authData, 0x10)      // TAG
	authData = append(authData, rand16...) // RAND
	authData = append(authData, 0x10)      // TAG
	authData = append(authData, autn16...) // AUTN

	apdu := make([]byte, 0, 5+len(authData)+1)
	apdu = append(apdu, 0x00, 0x88, 0x00, 0x81, byte(len(authData)))
	apdu = append(apdu, authData...)
	if includeLe {
		apdu = append(apdu, 0x00)
	}
	return apdu, nil
}

// ParseUSIMAuthResponse 解析 USIM AKA 鉴权响应。
// 成功: Tag=0xDB → RES/CK/IK
// 同步失败: Tag=0xDC → AUTS
func ParseUSIMAuthResponse(resp []byte) (enginesim.AKAResult, error) {
	if len(resp) < 2 {
		return enginesim.AKAResult{}, errors.New("response too short")
	}

	sw1 := resp[len(resp)-2]
	sw2 := resp[len(resp)-1]
	body := resp[:len(resp)-2]

	if sw1 != 0x90 || sw2 != 0x00 {
		return enginesim.AKAResult{}, fmt.Errorf("APDU status non-9000: %02X%02X", sw1, sw2)
	}
	if len(body) < 2 {
		return enginesim.AKAResult{}, errors.New("response body too short")
	}

	tag := body[0]
	switch tag {
	case 0xDB:
		// 成功响应: Tag=0xDB, Len, RES, CK, IK
		r := enginesim.AKAResult{}
		pos := 2 // skip tag + len
		if pos+16 > len(body) {
			return r, fmt.Errorf("USIM auth response too short for RES: %d", len(body))
		}
		r.RES = body[pos : pos+16]
		pos += 16
		if pos+16 > len(body) {
			return r, fmt.Errorf("USIM auth response too short for CK: %d", len(body))
		}
		r.CK = body[pos : pos+16]
		pos += 16
		if pos+16 > len(body) {
			return r, fmt.Errorf("USIM auth response too short for IK: %d", len(body))
		}
		r.IK = body[pos : pos+16]
		return r, nil

	case 0xDC:
		// 同步失败: Tag=0xDC, Len, AUTS
		if len(body) < 3 {
			return enginesim.AKAResult{}, fmt.Errorf("USIM auth sync failure body too short: %d", len(body))
		}
		autsLen := int(body[1])
		if len(body) < 2+autsLen {
			return enginesim.AKAResult{}, fmt.Errorf("USIM auth sync failure AUTS length mismatch: body=%d autsLen=%d", len(body), autsLen)
		}
		r := enginesim.AKAResult{AUTS: body[2 : 2+autsLen]}
		return r, enginesim.ErrSyncFailure

	default:
		return enginesim.AKAResult{}, fmt.Errorf("USIM auth response unknown tag: 0x%02X", tag)
	}
}

// sendAPDUGetResponse 处理 APDU 61xx GET RESPONSE 链。
// 如果返回 SW1=0x61，自动在同一条通道上发 GET RESPONSE 获取完整数据。
func sendAPDUGetResponse(sendFunc func([]byte) ([]byte, error), apdu []byte) (body []byte, sw1 byte, sw2 byte, err error) {
	resp, err := sendFunc(apdu)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(resp) < 2 {
		return nil, 0, 0, fmt.Errorf("response too short: %d", len(resp))
	}
	sw1 = resp[len(resp)-2]
	sw2 = resp[len(resp)-1]
	body = resp[:len(resp)-2]

	if sw1 == 0x61 && sw2 != 0x00 {
		getResp := []byte{0x00, 0xC0, 0x00, 0x00, sw2}
		resp2, err := sendFunc(getResp)
		if err != nil {
			return body, sw1, sw2, err
		}
		if len(resp2) < 2 {
			return nil, 0, 0, fmt.Errorf("GET RESPONSE too short: %d", len(resp2))
		}
		sw1 = resp2[len(resp2)-2]
		sw2 = resp2[len(resp2)-1]
		body = resp2[:len(resp2)-2]
	}
	return body, sw1, sw2, nil
}
