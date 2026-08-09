package main

import (
	"strings"
	"time"

	"github.com/1239t/vowifi-go/runtimehost/carrier"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

type carrierRuntimeOptions struct {
	CellID          string
	CellIDSource    string
	SMSServiceURI   string
	PCSCFAddr       string
	RegisterProfile voiceclient.RegisterProfile
	SIPInstanceURN  string
	RegisterExpiry  time.Duration
}

func resolveCarrierRuntimeOptions(mcc, mnc, liveCellID string) carrierRuntimeOptions {
	options := carrierRuntimeOptions{
		PCSCFAddr:     carrier.ResolveIMSPcscfAddr(mcc, mnc),
		SMSServiceURI: carrier.ResolveSMSServiceCenterURI(mcc, mnc),
	}

	register := carrier.ResolveIMSRegisterProfile(mcc, mnc)
	options.RegisterProfile = register.Profile
	if isGBEECarrier(mcc, mnc) && strings.TrimSpace(options.RegisterProfile.ContactFeatures) == "" {
		locationMode := options.RegisterProfile.LocationHeaderMode
		options.RegisterProfile = voiceclient.SimAdminGBEERegisterProfile()
		options.RegisterProfile.UserAgent = defaultIMSUserAgent(mcc, mnc)
		options.RegisterProfile.SecurityClientFormat = "phone_multi"
		options.RegisterProfile.LocationHeaderMode = locationMode
	}
	if strings.TrimSpace(options.RegisterProfile.UserAgent) == "" {
		options.RegisterProfile.UserAgent = defaultIMSUserAgent(mcc, mnc)
	}
	// The live matrix already established that GB EE accepts the configured
	// identity only without Wi-Fi PANI. Keep the proven carrier profile but
	// stop cycling unrelated handset/header variants on every tunnel start.
	if isGBEECarrier(mcc, mnc) {
		options.RegisterProfile.VariantSet = ""
	}
	options.SIPInstanceURN = register.SIPInstanceURN
	options.RegisterExpiry = register.RegisterExpiry

	switch carrier.IMSCellIDMode(mcc, mnc) {
	case "carrier_only":
		options.CellID = carrier.DefaultUTRANCellIDSuffix(mcc, mnc)
		if options.CellID != "" {
			options.CellIDSource = "carrier_default"
		} else {
			options.CellIDSource = "placeholder"
		}
		return options
	case "none":
		options.CellIDSource = "disabled"
		return options
	}

	options.CellID = strings.ToUpper(strings.TrimSpace(liveCellID))
	if options.CellID != "" {
		options.CellIDSource = "qmi"
		return options
	}
	options.CellID = carrier.DefaultUTRANCellIDSuffix(mcc, mnc)
	if options.CellID != "" {
		options.CellIDSource = "carrier_default"
	} else {
		options.CellIDSource = "placeholder"
	}
	return options
}

func isGBEECarrier(mcc, mnc string) bool {
	mcc = strings.TrimSpace(mcc)
	mnc = strings.TrimLeft(strings.TrimSpace(mnc), "0")
	return mcc == "234" && (mnc == "10" || mnc == "33")
}

func defaultIMSUserAgent(mcc, mnc string) string {
	mcc = strings.TrimSpace(mcc)
	mnc = strings.TrimLeft(strings.TrimSpace(mnc), "0")
	if mnc == "" {
		mnc = "0"
	}
	if mcc == "234" && (mnc == "10" || mnc == "33") {
		return voiceclient.DefaultGBEERegisterProfile().UserAgent
	}
	return voiceclient.RegisterProfile{}.Normalized().UserAgent
}

func registerProfileID(profile voiceclient.RegisterProfile) string {
	if id := strings.TrimSpace(profile.ContactFeatures); id != "" {
		return id
	}
	return "default"
}
