package imscore

import (
	"testing"

	"github.com/1239t/vowifi-go/internal/vowifi/policy"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

func TestInitialRegisterLocationHeaderModes(t *testing.T) {
	t.Setenv("VOHIVE_IMS_INITIAL_REJECT_FALLBACK", "")
	tests := []struct {
		mode            string
		includePANI     bool
		includeCellular bool
	}{
		{mode: "both", includePANI: true, includeCellular: true},
		{mode: "pani_only", includePANI: true, includeCellular: false},
		{mode: "cellular_only", includePANI: false, includeCellular: true},
		{mode: "none", includePANI: false, includeCellular: false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			template := policy.DefaultGiffgaffTemplate()
			template.LocationHeaderMode = tt.mode
			variants := initialRegisterVariants(Config{Template: template})
			if len(variants) != 1 {
				t.Fatalf("variant count = %d", len(variants))
			}
			if variants[0].includePANI != tt.includePANI || variants[0].includeCellular != tt.includeCellular {
				t.Fatalf("variant = %+v", variants[0])
			}
		})
	}
}

func TestMessagingProfileUsesSuccessfulRegisterLocationMode(t *testing.T) {
	template := policy.DefaultGiffgaffTemplate()
	template.LocationHeaderMode = "cellular_only"
	profile := messagingRegisterProfile(Config{
		Template: template,
		RegisterProfile: voiceclient.RegisterProfile{
			IncludePAccessNetworkInfo: true,
			IncludeCellularNetwork:    true,
		},
	})
	if profile.IncludePAccessNetworkInfo {
		t.Fatal("messaging profile reintroduced Wi-Fi PANI")
	}
	if !profile.IncludeCellularNetwork {
		t.Fatal("messaging profile dropped the proven cellular location header")
	}
}
