package smscodec

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

// decodeDeliverTPDUFallback handles standards-compliant address encodings
// that github.com/warthog618/sms currently mis-sizes, notably alphanumeric
// originator addresses used by OTP and service senders.
func decodeDeliverTPDUFallback(raw []byte) (string, string, time.Time, ConcatInfo, error) {
	if len(raw) < 12 || raw[0]&0x03 != 0x00 {
		return "", "", time.Time{}, ConcatInfo{}, errors.New("invalid SMS-DELIVER TPDU")
	}
	firstOctet := raw[0]
	i := 1
	oaLen := int(raw[i])
	i++
	if i >= len(raw) {
		return "", "", time.Time{}, ConcatInfo{}, errors.New("SMS-DELIVER originator address type missing")
	}
	oaTOA := raw[i]
	i++
	oaOctets := (oaLen + 1) / 2
	if oaTOA&0x70 == 0x50 {
		oaOctets = (oaLen*7 + 7) / 8
	}
	if i+oaOctets > len(raw) {
		return "", "", time.Time{}, ConcatInfo{}, errors.New("SMS-DELIVER originator address truncated")
	}
	sender, err := decodeDeliverAddress(oaLen, oaTOA, raw[i:i+oaOctets])
	if err != nil {
		return "", "", time.Time{}, ConcatInfo{}, err
	}
	i += oaOctets
	if i+10 > len(raw) {
		return "", "", time.Time{}, ConcatInfo{}, errors.New("SMS-DELIVER fields truncated")
	}
	i++ // PID
	dcs := raw[i]
	i++
	sentAt, err := decodeDeliverTimestamp(raw[i : i+7])
	if err != nil {
		return "", "", time.Time{}, ConcatInfo{}, err
	}
	i += 7
	udl := int(raw[i])
	i++
	text, concat, err := decodeDeliverUserData(raw[i:], udl, dcs, firstOctet&0x40 != 0)
	if err != nil {
		return "", "", time.Time{}, ConcatInfo{}, err
	}
	return sender, text, sentAt, concat, nil
}

func decodeDeliverAddress(length int, toa byte, data []byte) (string, error) {
	if toa&0x70 == 0x50 {
		return decodeDeliverGSM7(unpackDeliverSeptets(data, length, 0)), nil
	}
	var out strings.Builder
	if toa&0x70 == 0x10 {
		out.WriteByte('+')
	}
	written := 0
	for _, item := range data {
		for _, nibble := range []byte{item & 0x0f, item >> 4} {
			if written >= length || nibble == 0x0f {
				return out.String(), nil
			}
			digit, ok := deliverAddressDigit(nibble)
			if !ok {
				return "", fmt.Errorf("invalid SMS address digit 0x%x", nibble)
			}
			out.WriteByte(digit)
			written++
		}
	}
	if written != length {
		return "", errors.New("SMS address truncated")
	}
	return out.String(), nil
}

func deliverAddressDigit(nibble byte) (byte, bool) {
	switch {
	case nibble <= 9:
		return '0' + nibble, true
	case nibble == 0x0a:
		return '*', true
	case nibble == 0x0b:
		return '#', true
	case nibble >= 0x0c && nibble <= 0x0e:
		return 'a' + nibble - 0x0c, true
	default:
		return 0, false
	}
}

func decodeDeliverUserData(data []byte, udl int, dcs byte, hasUDH bool) (string, ConcatInfo, error) {
	if udl < 0 {
		return "", ConcatInfo{}, errors.New("invalid SMS user data length")
	}
	headerBytes := 0
	concat := ConcatInfo{}
	if hasUDH {
		if len(data) == 0 {
			return "", concat, errors.New("SMS UDH length missing")
		}
		headerBytes = int(data[0]) + 1
		if headerBytes > len(data) {
			return "", concat, errors.New("SMS UDH truncated")
		}
		concat = parseDeliverConcat(data[:headerBytes])
	}

	switch deliverAlphabet(dcs) {
	case "ucs2":
		payloadBytes := udl - headerBytes
		if payloadBytes < 0 || headerBytes+payloadBytes > len(data) {
			return "", concat, errors.New("UCS2 SMS user data truncated")
		}
		payload := data[headerBytes : headerBytes+payloadBytes]
		if len(payload)%2 != 0 {
			return "", concat, errors.New("UCS2 SMS user data has odd length")
		}
		units := make([]uint16, 0, len(payload)/2)
		for i := 0; i < len(payload); i += 2 {
			units = append(units, uint16(payload[i])<<8|uint16(payload[i+1]))
		}
		return string(utf16.Decode(units)), concat, nil
	case "8bit":
		payloadBytes := udl - headerBytes
		if payloadBytes < 0 || headerBytes+payloadBytes > len(data) {
			return "", concat, errors.New("8-bit SMS user data truncated")
		}
		return strings.ToValidUTF8(string(data[headerBytes:headerBytes+payloadBytes]), ""), concat, nil
	default:
		headerSeptets := 0
		fillBits := 0
		if headerBytes > 0 {
			headerSeptets = (headerBytes*8 + 6) / 7
			fillBits = (7 - (headerBytes*8)%7) % 7
		}
		septets := udl - headerSeptets
		if septets < 0 {
			return "", concat, errors.New("GSM7 SMS user data shorter than UDH")
		}
		payloadOctets := (fillBits + septets*7 + 7) / 8
		if septets == 0 {
			payloadOctets = 0
		}
		if headerBytes+payloadOctets > len(data) {
			return "", concat, errors.New("GSM7 SMS user data truncated")
		}
		septetData := unpackDeliverSeptets(data[headerBytes:headerBytes+payloadOctets], septets, fillBits)
		return decodeDeliverGSM7(septetData), concat, nil
	}
}

func parseDeliverConcat(udh []byte) ConcatInfo {
	if len(udh) < 2 {
		return ConcatInfo{}
	}
	for i := 1; i+1 < len(udh); {
		iei := udh[i]
		length := int(udh[i+1])
		i += 2
		if i+length > len(udh) {
			return ConcatInfo{}
		}
		value := udh[i : i+length]
		switch {
		case iei == 0x00 && len(value) == 3 && value[1] > 1:
			return ConcatInfo{IsConcat: true, Ref: int(value[0]), RefBits: 8, Total: int(value[1]), Seq: int(value[2])}
		case iei == 0x08 && len(value) == 4 && value[2] > 1:
			return ConcatInfo{IsConcat: true, Ref: int(value[0])<<8 | int(value[1]), RefBits: 16, Total: int(value[2]), Seq: int(value[3])}
		}
		i += length
	}
	return ConcatInfo{}
}

func deliverAlphabet(dcs byte) string {
	group := dcs & 0xf0
	if group == 0xe0 || (group <= 0x70 && dcs&0x0c == 0x08) {
		return "ucs2"
	}
	if group == 0xf0 && dcs&0x04 != 0 || group <= 0x70 && dcs&0x0c == 0x04 {
		return "8bit"
	}
	return "gsm7"
}

func unpackDeliverSeptets(data []byte, count, bitOffset int) []byte {
	out := make([]byte, 0, count)
	for i := 0; i < count; i++ {
		bitPos := bitOffset + i*7
		bytePos := bitPos / 8
		shift := bitPos % 8
		if bytePos >= len(data) {
			break
		}
		value := (data[bytePos] >> shift) & 0x7f
		if shift > 1 && bytePos+1 < len(data) {
			value |= (data[bytePos+1] << (8 - shift)) & 0x7f
		}
		out = append(out, value)
	}
	return out
}

var deliverGSM7Basic = []rune{
	'@', '£', '$', '¥', 'è', 'é', 'ù', 'ì', 'ò', 'Ç', '\n', 'Ø', 'ø', '\r', 'Å', 'å',
	'Δ', '_', 'Φ', 'Γ', 'Λ', 'Ω', 'Π', 'Ψ', 'Σ', 'Θ', 'Ξ', '\x1b', 'Æ', 'æ', 'ß', 'É',
	' ', '!', '"', '#', '¤', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':', ';', '<', '=', '>', '?',
	'¡', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O',
	'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§',
	'¿', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o',
	'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'ä', 'ö', 'ñ', 'ü', 'à',
}

var deliverGSM7Extension = map[byte]rune{
	0x0a: '\f', 0x14: '^', 0x28: '{', 0x29: '}', 0x2f: '\\',
	0x3c: '[', 0x3d: '~', 0x3e: ']', 0x40: '|', 0x65: '€',
}

func decodeDeliverGSM7(septets []byte) string {
	var out strings.Builder
	for i := 0; i < len(septets); i++ {
		code := septets[i] & 0x7f
		if code == 0x1b && i+1 < len(septets) {
			if value, ok := deliverGSM7Extension[septets[i+1]&0x7f]; ok {
				out.WriteRune(value)
			}
			i++
			continue
		}
		if int(code) < len(deliverGSM7Basic) {
			out.WriteRune(deliverGSM7Basic[code])
		}
	}
	return out.String()
}

func decodeDeliverTimestamp(raw []byte) (time.Time, error) {
	if len(raw) != 7 {
		return time.Time{}, errors.New("SMS timestamp must be 7 octets")
	}
	values := make([]int, 6)
	for i := 0; i < 6; i++ {
		values[i] = decodeDeliverSemiOctet(raw[i])
	}
	tzOctet := raw[6]
	negative := tzOctet&0x08 != 0
	tzOctet &^= 0x08
	tzQuarters := decodeDeliverSemiOctet(tzOctet)
	if values[0] < 0 || values[1] < 1 || values[1] > 12 || values[2] < 1 || values[2] > 31 || values[3] < 0 || values[3] > 23 || values[4] < 0 || values[4] > 59 || values[5] < 0 || values[5] > 59 || tzQuarters < 0 {
		return time.Time{}, errors.New("SMS timestamp contains invalid BCD")
	}
	year := 2000 + values[0]
	if values[0] >= 90 {
		year = 1900 + values[0]
	}
	offset := tzQuarters * 15 * 60
	if negative {
		offset = -offset
	}
	return time.Date(year, time.Month(values[1]), values[2], values[3], values[4], values[5], 0, time.FixedZone("", offset)), nil
}

func decodeDeliverSemiOctet(value byte) int {
	lo := int(value & 0x0f)
	hi := int(value >> 4)
	if lo > 9 || hi > 9 {
		return -1
	}
	return lo*10 + hi
}
