package imscore

import (
	"fmt"
	"strings"

	"github.com/emiago/sipgo/sip"

	"github.com/1239t/vowifi-go/internal/vowifi/imsheaders"
)

type secAgreeDecision struct {
	installIPSec     bool
	requireVerify    bool
	missingSecClient bool
	reason           string
}

// decideSecAgreeAfterChallenge decides whether to install IPSec after a 401/407
// challenge round (author sec_agree_decision.go).
func decideSecAgreeAfterChallenge(cfg Config, res *sip.Response) (secAgreeDecision, error) {
	if res == nil {
		return secAgreeDecision{}, fmt.Errorf("missing challenge response")
	}
	secAgree := secAgreeEnabled(cfg.Template)
	secServer := res.GetHeader("Security-Server")
	hasServer := secServer != nil && strings.TrimSpace(secServer.Value()) != ""

	if !secAgree {
		return secAgreeDecision{
			installIPSec:  false,
			requireVerify: false,
			reason:        "sec_agree_disabled",
		}, nil
	}

	if !hasServer {
		return secAgreeDecision{
			installIPSec:  false,
			requireVerify: false,
			reason:        "security_server_missing_auto_disable",
		}, nil
	}

	if securityClientMechanismCount(cfg.Template) == 0 {
		return secAgreeDecision{
			missingSecClient: true,
			reason:           "missing_security_client_when_sec_agree_enabled",
		}, fmt.Errorf("missing security-client mechanisms")
	}

	return secAgreeDecision{
		installIPSec:  true,
		requireVerify: true,
		reason:        "security_server_offer_selected",
	}, nil
}

func buildSecurityVerifyFromChallenge(cfg Config, res *sip.Response) (string, *imsheaders.SecurityOffer, error) {
	if res == nil {
		return "", nil, fmt.Errorf("missing challenge response")
	}
	secHeaders := res.GetHeaders("Security-Server")
	if len(secHeaders) == 0 {
		return "", nil, fmt.Errorf("missing Security-Server")
	}
	// RFC 3329 requires Security-Verify to echo the Security-Server offer.
	// Keep the wire value byte-for-byte (apart from joining repeated header
	// fields) because strict P-CSCF implementations compare parameter spelling,
	// ordering, and quoting instead of treating it as a normalized set.
	rawValues := make([]string, 0, len(secHeaders))
	for _, h := range secHeaders {
		if value := strings.TrimSpace(h.Value()); value != "" {
			rawValues = append(rawValues, value)
		}
	}
	if len(rawValues) == 0 {
		return "", nil, fmt.Errorf("empty Security-Server")
	}
	rawSecurityServer := strings.Join(rawValues, ",")
	offers, err := imsheaders.ParseSecurityServer(rawSecurityServer)
	if err != nil {
		return "", nil, err
	}
	selected, err := imsheaders.SelectSecurityServerOffer(
		offers,
		cfg.Template.SecurityClientMechanisms,
		cfg.Template.StrictSecurityServerOffer,
	)
	if err != nil {
		return "", nil, err
	}
	return rawSecurityServer, selected, nil
}
