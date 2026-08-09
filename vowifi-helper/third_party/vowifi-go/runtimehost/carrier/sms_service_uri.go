package carrier

import "strings"

// ResolveSMSServiceCenterURI returns an explicitly configured public
// service-centre PSI, if the carrier override supplies one. An empty result
// preserves the legacy derivation from the SIM SMSC and associated domain.
func ResolveSMSServiceCenterURI(mcc, mnc string) string {
	preset, ok := lookup(mcc, mnc)
	if !ok {
		return ""
	}
	return strings.TrimSpace(preset.SMSServicesURI)
}
