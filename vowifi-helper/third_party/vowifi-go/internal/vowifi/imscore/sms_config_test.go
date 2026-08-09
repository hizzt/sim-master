package imscore

import (
	"testing"

	"github.com/1239t/vowifi-go/internal/vowifi/policy"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

func TestSMSServiceCentreSurvivesVoiceConfigConversion(t *testing.T) {
	ims := IMSConfigFromVoice(voiceclient.Config{SMSC: "+447802002606"}, policy.IMSRegisterTemplate{}, "")
	if ims.SMSC != "+447802002606" {
		t.Fatalf("IMSConfig SMSC = %q", ims.SMSC)
	}
	cfg := internalConfigFromIMS(ims, StartSessionInput{})
	if cfg.SMSC != "+447802002606" {
		t.Fatalf("internal Config SMSC = %q", cfg.SMSC)
	}
}
