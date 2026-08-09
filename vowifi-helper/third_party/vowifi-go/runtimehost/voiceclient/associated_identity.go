package voiceclient

import (
	"fmt"
	"strings"

	"github.com/emiago/sipgo/sip"
)

// AssociatedPublicIdentities returns the network-ordered public identities
// from REGISTER 200 P-Associated-URI headers.
func AssociatedPublicIdentities(response *sip.Response) []string {
	if response == nil {
		return nil
	}
	var identities []string
	for _, header := range response.GetHeaders("P-Associated-URI") {
		if header == nil {
			continue
		}
		for _, value := range splitSIPAddressList(header.Value()) {
			var uri sip.Uri
			var params sip.HeaderParams
			if _, err := sip.ParseAddressValue(value, &uri, &params); err != nil {
				continue
			}
			if scheme := strings.ToLower(strings.TrimSpace(uri.Scheme)); scheme != "sip" && scheme != "sips" && scheme != "tel" {
				continue
			}
			identities = append(identities, uri.String())
		}
	}
	return identities
}

// AssociatedPreferredIdentity selects the SIP/SIPS IMPU for the request From
// and P-Preferred-Identity headers. Some cores advertise a tel identity first
// and the SIP AOR second; using that tel URI as From can leave MESSAGE outside
// the REGISTER binding's authorization space. The telephone identity remains
// available separately through AssociatedTelephoneIdentity for SMS PPI.
func AssociatedPreferredIdentity(identities []string) string {
	for _, identity := range identities {
		var uri sip.Uri
		if sip.ParseUri(strings.TrimSpace(identity), &uri) != nil {
			continue
		}
		scheme := strings.ToLower(strings.TrimSpace(uri.Scheme))
		if scheme == "sip" || scheme == "sips" {
			return uri.String()
		}
	}
	if len(identities) > 0 {
		return strings.TrimSpace(identities[0])
	}
	return ""
}

// AssociatedPhoneNumber returns the first telephone identity advertised by
// the network. SIP/SIPS identities are deliberately ignored because their user
// part commonly contains the IMSI or another private-style identifier.
func AssociatedPhoneNumber(identities []string) string {
	identity := AssociatedTelephoneIdentity(identities)
	if identity == "" {
		return ""
	}
	var uri sip.Uri
	if sip.ParseUri(identity, &uri) != nil {
		return ""
	}
	return normalizedTelephoneSubscriber(uri.Host)
}

// AssociatedTelephoneIdentity returns the first network-associated tel URI.
// Keeping the URI as an identity is distinct from extracting its displayable
// phone number above.
func AssociatedTelephoneIdentity(identities []string) string {
	for _, identity := range identities {
		var uri sip.Uri
		if sip.ParseUri(strings.TrimSpace(identity), &uri) != nil || !strings.EqualFold(strings.TrimSpace(uri.Scheme), "tel") {
			continue
		}
		if number := normalizedTelephoneSubscriber(uri.Host); number != "" {
			return uri.String()
		}
	}
	return ""
}

func normalizedTelephoneSubscriber(value string) string {
	value = strings.TrimSpace(value)
	digits := value
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	}
	if len(digits) < 3 || len(digits) > 15 {
		return ""
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return value
}

func splitSIPAddressList(value string) []string {
	var values []string
	var current strings.Builder
	inQuote := false
	escaped := false
	angleDepth := 0
	for _, char := range value {
		switch {
		case escaped:
			current.WriteRune(char)
			escaped = false
		case char == '\\' && inQuote:
			current.WriteRune(char)
			escaped = true
		case char == '"':
			current.WriteRune(char)
			inQuote = !inQuote
		case char == '<' && !inQuote:
			angleDepth++
			current.WriteRune(char)
		case char == '>' && !inQuote:
			if angleDepth > 0 {
				angleDepth--
			}
			current.WriteRune(char)
		case char == ',' && !inQuote && angleDepth == 0:
			if part := strings.TrimSpace(current.String()); part != "" {
				values = append(values, part)
			}
			current.Reset()
		default:
			current.WriteRune(char)
		}
	}
	if part := strings.TrimSpace(current.String()); part != "" {
		values = append(values, part)
	}
	return values
}

func associatedIdentitySchemes(identities []string) string {
	seen := make(map[string]struct{})
	var schemes []string
	for _, identity := range identities {
		var uri sip.Uri
		if sip.ParseUri(identity, &uri) != nil {
			continue
		}
		scheme := strings.ToLower(strings.TrimSpace(uri.Scheme))
		if scheme == "" {
			continue
		}
		if _, ok := seen[scheme]; ok {
			continue
		}
		seen[scheme] = struct{}{}
		schemes = append(schemes, scheme)
	}
	return strings.Join(schemes, ",")
}

func (c *Client) preferredPublicIdentity() string {
	if c != nil {
		if identity := strings.TrimSpace(c.cfg.PreferredIdentity); identity != "" {
			return identity
		}
		return strings.TrimSpace(c.cfg.PublicURI)
	}
	return ""
}

func (c *Client) preferredSMSPublicIdentity() string {
	if c != nil {
		if identity := strings.TrimSpace(c.cfg.SMSPreferredIdentity); identity != "" {
			return identity
		}
	}
	return c.preferredPublicIdentity()
}

// applyPreferredServiceIdentity refreshes a cloned service request after a
// REGISTER recovery while preserving its existing From tag.
func (c *Client) applyPreferredServiceIdentity(request *sip.Request) error {
	return applyPreferredIdentity(request, c.preferredPublicIdentity())
}

func (c *Client) applyPreferredSMSIdentity(request *sip.Request) error {
	// Preserve the registered SIP IMPU in From while using the associated tel
	// identity in P-Preferred-Identity. This is the exact split accepted by the
	// O2 IP-SM-GW during the 06:17 live submission.
	if err := applyPreferredIdentity(request, c.preferredPublicIdentity()); err != nil {
		return err
	}
	identity := c.preferredSMSPublicIdentity()
	var uri sip.Uri
	if err := sip.ParseUri(identity, &uri); err != nil {
		return fmt.Errorf("voiceclient: parse preferred SMS identity: %w", err)
	}
	request.RemoveHeader("P-Preferred-Identity")
	request.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+identity+">"))
	return nil
}

func applyPreferredIdentity(request *sip.Request, identity string) error {
	if request == nil {
		return nil
	}
	var uri sip.Uri
	if err := sip.ParseUri(identity, &uri); err != nil {
		return fmt.Errorf("voiceclient: parse preferred service identity: %w", err)
	}
	if from := request.From(); from != nil {
		from.Address = uri
		request.ReplaceHeader(from)
	}
	request.RemoveHeader("P-Preferred-Identity")
	request.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+identity+">"))
	return nil
}
