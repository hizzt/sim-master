package voiceclient

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
)

const (
	cpimContentType      = "message/cpim"
	maxSMSPayloadDepth   = 4
	maxSMSPayloadHeaders = 64 * 1024
)

// smsInboundPayload is the normalized SMS body passed to the application
// decoder. replyCPIM remembers the envelope so the RP-ACK follows the same
// CPIM convention as the received MESSAGE.
type smsInboundPayload struct {
	body      []byte
	replyCPIM bool
	cpimFrom  string
	cpimTo    string
}

func decodeSMSInboundPayload(contentType string, body []byte) (smsInboundPayload, error) {
	return decodeSMSInboundPayloadDepth(contentType, body, 0)
}

func decodeSMSInboundPayloadDepth(contentType string, body []byte, depth int) (smsInboundPayload, error) {
	if depth > maxSMSPayloadDepth {
		return smsInboundPayload{}, fmt.Errorf("voiceclient: nested SMS MESSAGE exceeds %d levels", maxSMSPayloadDepth)
	}
	mediaType, params, err := parseSMSMediaType(contentType)
	if err != nil {
		return smsInboundPayload{}, err
	}
	switch mediaType {
	case smsContentType:
		return smsInboundPayload{body: append([]byte(nil), body...)}, nil
	case cpimContentType:
		messageHeaders, contentHeaders, content, err := parseSMSCPIM(body)
		if err != nil {
			return smsInboundPayload{}, err
		}
		innerType := contentHeaders.Get("Content-Type")
		if strings.TrimSpace(innerType) == "" {
			return smsInboundPayload{}, fmt.Errorf("voiceclient: CPIM Content-Type is missing")
		}
		decoded, err := decodeSMSTransferEncoding(contentHeaders.Get("Content-Transfer-Encoding"), content)
		if err != nil {
			return smsInboundPayload{}, err
		}
		inner, err := decodeSMSInboundPayloadDepth(innerType, decoded, depth+1)
		if err != nil {
			return smsInboundPayload{}, err
		}
		inner.replyCPIM = true
		inner.cpimFrom = smsHeaderValue(messageHeaders, "From")
		inner.cpimTo = smsHeaderValue(messageHeaders, "To")
		return inner, nil
	case "multipart/mixed", "multipart/related":
		boundary := strings.TrimSpace(params["boundary"])
		if boundary == "" {
			return smsInboundPayload{}, fmt.Errorf("voiceclient: multipart SMS boundary is missing")
		}
		reader := multipart.NewReader(bytes.NewReader(body), boundary)
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return smsInboundPayload{}, fmt.Errorf("voiceclient: parse multipart SMS: %w", nextErr)
			}
			partBody, readErr := io.ReadAll(io.LimitReader(part, maxSMSPayloadHeaders+16*1024*1024))
			_ = part.Close()
			if readErr != nil {
				return smsInboundPayload{}, fmt.Errorf("voiceclient: read multipart SMS: %w", readErr)
			}
			decoded, transferErr := decodeSMSTransferEncoding(part.Header.Get("Content-Transfer-Encoding"), partBody)
			if transferErr != nil {
				return smsInboundPayload{}, transferErr
			}
			inner, innerErr := decodeSMSInboundPayloadDepth(part.Header.Get("Content-Type"), decoded, depth+1)
			if innerErr == nil {
				return inner, nil
			}
		}
		return smsInboundPayload{}, fmt.Errorf("voiceclient: multipart MESSAGE contains no 3GPP SMS part")
	default:
		return smsInboundPayload{}, fmt.Errorf("voiceclient: unsupported SMS MESSAGE content type %q", mediaType)
	}
}

func smsHeaderValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseSMSMediaType(value string) (string, map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, fmt.Errorf("voiceclient: SMS MESSAGE Content-Type is missing")
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		// Some IMS cores emit an unquoted parameter containing a semicolon. The
		// media type itself is still unambiguous, so retain the lenient path.
		mediaType = strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
		if mediaType == "" {
			return "", nil, fmt.Errorf("voiceclient: invalid SMS MESSAGE Content-Type %q", value)
		}
		params = nil
	}
	return strings.ToLower(strings.TrimSpace(mediaType)), params, nil
}

func parseSMSCPIM(body []byte) (map[string]string, textproto.MIMEHeader, []byte, error) {
	if len(body) > maxSMSPayloadHeaders+16*1024*1024 {
		return nil, nil, nil, fmt.Errorf("voiceclient: CPIM body is too large")
	}
	messageBlock, rest, ok := splitSMSHeaderBlock(body)
	if !ok || len(messageBlock) > maxSMSPayloadHeaders {
		return nil, nil, nil, fmt.Errorf("voiceclient: CPIM message headers are malformed")
	}
	messageHeaders := parseSMSHeaderLines(messageBlock)
	contentBlock, content, ok := splitSMSHeaderBlock(rest)
	if !ok || len(contentBlock) > maxSMSPayloadHeaders {
		return nil, nil, nil, fmt.Errorf("voiceclient: CPIM content headers are malformed")
	}
	contentHeaders := textproto.MIMEHeader{}
	for key, value := range parseSMSHeaderLines(contentBlock) {
		contentHeaders.Set(key, value)
	}
	if length := strings.TrimSpace(contentHeaders.Get("Content-Length")); length != "" {
		n, err := strconv.Atoi(length)
		if err != nil || n < 0 || n > len(content) {
			return nil, nil, nil, fmt.Errorf("voiceclient: invalid CPIM Content-Length %q", length)
		}
		content = content[:n]
	}
	return messageHeaders, contentHeaders, append([]byte(nil), content...), nil
}

func splitSMSHeaderBlock(body []byte) ([]byte, []byte, bool) {
	if idx := bytes.Index(body, []byte("\r\n\r\n")); idx >= 0 {
		return body[:idx], body[idx+4:], true
	}
	if idx := bytes.Index(body, []byte("\n\n")); idx >= 0 {
		return body[:idx], body[idx+2:], true
	}
	return nil, nil, false
}

func parseSMSHeaderLines(block []byte) map[string]string {
	result := make(map[string]string)
	var current string
	for _, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if current != "" {
				result[current] += " " + strings.TrimSpace(line)
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		current = name
		result[name] = strings.TrimSpace(value)
	}
	return result
}

func decodeSMSTransferEncoding(encoding string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "binary", "8bit", "7bit":
		return body, nil
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), ""))
		if err != nil {
			return nil, fmt.Errorf("voiceclient: decode base64 SMS body: %w", err)
		}
		return decoded, nil
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			return nil, fmt.Errorf("voiceclient: decode quoted-printable SMS body: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("voiceclient: unsupported SMS Content-Transfer-Encoding %q", encoding)
	}
}

func (c *Client) buildSMSCPIMReply(req *sip.Request, inbound smsInboundPayload, body []byte) ([]byte, error) {
	if len(body) > 16*1024*1024 {
		return nil, fmt.Errorf("voiceclient: CPIM reply body is too large")
	}
	// A CPIM response reverses the CPIM envelope identities. SIP From/To are
	// only a fallback because an IMS core may use network identities in the
	// outer SIP MESSAGE while CPIM carries the actual endpoint identities.
	from := smsCPIMURI(inbound.cpimTo)
	to := smsCPIMURI(inbound.cpimFrom)
	if req != nil {
		if from == "" {
			if header := req.GetHeader("To"); header != nil {
				from = smsCPIMURI(header.Value())
			}
		}
		if to == "" {
			if header := req.GetHeader("From"); header != nil {
				to = smsCPIMURI(header.Value())
			}
		}
	}
	if from == "" {
		from = strings.TrimSpace(c.cfg.PublicURI)
	}
	if from == "" || to == "" {
		return nil, fmt.Errorf("voiceclient: CPIM reply identities are incomplete")
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "From: <%s>\r\n", from)
	fmt.Fprintf(&out, "To: <%s>\r\n", to)
	fmt.Fprintf(&out, "DateTime: %s\r\n", time.Now().UTC().Format(time.RFC3339))
	out.WriteString("\r\n")
	out.WriteString("Content-Type: " + smsContentType + "\r\n")
	fmt.Fprintf(&out, "Content-Length: %d\r\n", len(body))
	out.WriteString("\r\n")
	out.Write(body)
	return out.Bytes(), nil
}

func smsCPIMURI(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", ""))
	if start := strings.IndexByte(value, '<'); start >= 0 {
		if end := strings.IndexByte(value[start+1:], '>'); end >= 0 {
			return strings.TrimSpace(value[start+1 : start+1+end])
		}
	}
	if semi := strings.IndexByte(value, ';'); semi >= 0 {
		value = value[:semi]
	}
	return strings.Trim(strings.TrimSpace(value), "<>")
}
