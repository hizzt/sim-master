package imscore

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"

	"github.com/1239t/vowifi-go/internal/vowifi/ipsec3gpp"
	"github.com/1239t/vowifi-go/internal/vowifi/policy"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

type resetTrackingIPSecInstaller struct {
	clearCalls int
}

func (*resetTrackingIPSecInstaller) InstallIPSec3GPP(ipsec3gpp.Policy, *ipsec3gpp.Transport) error {
	return nil
}

func (i *resetTrackingIPSecInstaller) ClearIPSec3GPP() {
	i.clearCalls++
}

func TestInitialRegisterVariantsExpandGiffgaffMatrix(t *testing.T) {
	profile := voiceclient.SimAdminGBEERegisterProfile()
	profile.UserAgent = "SimAdmin VoWiFi"
	profile.SecurityClientFormat = "phone_multi"
	template := policy.DefaultGiffgaffTemplate()
	template.EnableInitialRejectFallback = true

	variants := initialRegisterVariants(Config{Template: template, RegisterProfile: profile})
	if len(variants) != 64 {
		t.Fatalf("variant count = %d, want 64", len(variants))
	}

	modeCounts := map[string]int{}
	userAgents := map[string]bool{}
	seen := map[string]bool{}
	for _, variant := range variants {
		modeCounts[variant.locationMode]++
		userAgents[variant.profile.UserAgent] = true
		key := strings.Join([]string{
			variant.profile.UserAgent,
			variant.initialAuth,
			variant.profile.AuthorizationIdentity,
			variant.locationMode,
			variant.profile.ContactFeatures,
			variant.profile.SecurityClientFormat,
			boolString(variant.profile.IncludeRequireSecAgree),
			boolString(variant.profile.IncludeRoute),
			boolString(variant.profile.IncludePPreferredID),
			boolString(variant.profile.IncludePVisitedNetworkID),
			boolString(variant.profile.IncludeAcceptContact),
		}, "|")
		if seen[key] {
			t.Fatalf("duplicate variant key %q", key)
		}
		seen[key] = true
	}
	for _, mode := range []string{"both", "pani_only", "cellular_only", "none"} {
		if modeCounts[mode] != 16 {
			t.Fatalf("mode %s count = %d, want 16", mode, modeCounts[mode])
		}
	}
	for _, ua := range []string{
		"Google_Pixel7Pro_Android16_CP1A.260405.005",
		"SimAdmin VoWiFi",
		"rmx3366 VoWiFi",
		"_M2011K2G_Qualcomm_1690275146_Android13",
	} {
		if !userAgents[ua] {
			t.Fatalf("missing User-Agent %q", ua)
		}
	}
	if got := variants[0].profile.UserAgent; got != "Google_Pixel7Pro_Android16_CP1A.260405.005" {
		t.Fatalf("first User-Agent = %q", got)
	}
}

func TestLegacyVohiveVariantUsesEAPPrivateIdentityAndTemplateSecurity(t *testing.T) {
	profile := voiceclient.SimAdminGBEERegisterProfile()
	template := policy.DefaultGiffgaffTemplate()
	template.EnableInitialRejectFallback = true
	cfg := Config{
		LocalIP:         net.ParseIP("2001:db8::10"),
		PCSCFAddr:       "[2001:db8::20]:5060",
		Realm:           "ims.mnc010.mcc234.3gppnetwork.org",
		PrivateID:       "234100000000001@ims.mnc010.mcc234.3gppnetwork.org",
		EAPPrivateID:    "0234100000000001@nai.epc.mnc010.mcc234.3gppnetwork.org",
		PublicURI:       "sip:234100000000001@ims.mnc010.mcc234.3gppnetwork.org",
		HomeDomain:      "ims.mnc010.mcc234.3gppnetwork.org",
		IMSI:            "234100000000001",
		MCC:             "234",
		MNC:             "10",
		CellID:          "70010BC614E",
		Template:        template,
		RegisterProfile: profile,
	}
	variant := initialRegisterVariants(cfg)[12]
	if variant.profile.AuthorizationIdentity != "eap_private_id" {
		t.Fatalf("first identity = %q", variant.profile.AuthorizationIdentity)
	}
	variantCfg := applyInitialRegisterVariantConfig(cfg, variant)
	if variantCfg.PrivateID != cfg.EAPPrivateID {
		t.Fatalf("private identity = %q, want %q", variantCfg.PrivateID, cfg.EAPPrivateID)
	}
	req, err := buildRegisterRequest(variantCfg, registerState{spiC: 1, spiS: 2, portC: 5064, portS: 5063}, true, variant)
	if err != nil {
		t.Fatal(err)
	}
	assertHeaderContains(t, req, "Authorization", cfg.EAPPrivateID)
	securityClient := req.GetHeader("Security-Client")
	if securityClient == nil || strings.Count(securityClient.Value(), "ipsec-3gpp") != 1 {
		t.Fatalf("Security-Client = %v, want one template mechanism", securityClient)
	}
}

func TestBuildRegisterRequestUsesFullProfileVariant(t *testing.T) {
	profile := voiceclient.XiaomiMi11RegisterProfile()
	variant := applyLocationHeaderMode("pani_only", initialRegisterVariantFromProfile(profile))
	cfg := Config{
		LocalIP:         net.ParseIP("2001:db8::10"),
		PCSCFAddr:       "[2001:db8::20]:5060",
		Realm:           "ims.mnc010.mcc234.3gppnetwork.org",
		PrivateID:       "234100000000001@ims.mnc010.mcc234.3gppnetwork.org",
		PublicURI:       "sip:234100000000001@ims.mnc010.mcc234.3gppnetwork.org",
		HomeDomain:      "ims.mnc010.mcc234.3gppnetwork.org",
		IMSI:            "234100000000001",
		MCC:             "234",
		MNC:             "10",
		CellID:          "70010BC614E",
		Template:        policy.DefaultGiffgaffTemplate(),
		RegisterProfile: profile,
	}
	state := registerState{
		spiC:        1,
		spiS:        2,
		portC:       5064,
		portS:       5063,
		sipInstance: "urn:gsma:imei:86998877-665544-0",
		contactUser: "contact-uuid",
	}

	req, err := buildRegisterRequest(cfg, state, true, variant)
	if err != nil {
		t.Fatal(err)
	}
	assertHeaderContains(t, req, "User-Agent", profile.UserAgent)
	assertHeaderContains(t, req, "Contact", "contact-uuid")
	assertHeaderContains(t, req, "Contact", `+g.3gpp.accesstype="wlan1"`)
	assertHeaderContains(t, req, "Require", "sec-agree")
	assertHeaderContains(t, req, "Proxy-Require", "sec-agree")
	assertHeaderContains(t, req, "P-Access-Network-Info", "IEEE-802.11")
	if req.GetHeader("Cellular-Network-Info") != nil {
		t.Fatal("unexpected Cellular-Network-Info")
	}
	if req.GetHeader("Route") != nil {
		t.Fatal("unexpected Route")
	}
	if req.GetHeader("P-Preferred-Identity") != nil {
		t.Fatal("unexpected P-Preferred-Identity")
	}
	securityClient := req.GetHeader("Security-Client")
	if securityClient == nil || strings.Count(securityClient.Value(), "ipsec-3gpp") != 6 {
		t.Fatalf("Security-Client = %v", securityClient)
	}
}

func TestInitialRegisterTimeoutRetriesSameVariant(t *testing.T) {
	template := policy.DefaultGiffgaffTemplate()
	template.EnableInitialRejectFallback = true
	cfg := Config{Template: template}

	for _, err := range []error{
		context.DeadlineExceeded,
		errors.New("register response timeout: i/o timeout"),
		errors.New("register dial: context deadline exceeded"),
		errors.New("register connection closed before response"),
		errors.New("ep.Bind: port is in use"),
	} {
		if !shouldRetryInitialRegisterSameVariant(context.Background(), cfg, err) {
			t.Fatalf("expected retry for %v", err)
		}
	}
	if shouldRetryInitialRegisterSameVariant(context.Background(), cfg, errors.New("403 Forbidden")) {
		t.Fatal("403 must advance instead of retrying the same variant")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldRetryInitialRegisterSameVariant(canceled, cfg, context.DeadlineExceeded) {
		t.Fatal("canceled session must stop timeout retries")
	}
}

func TestInitialRegisterVariantAdvancesOnlyOnExplicitForbidden(t *testing.T) {
	template := policy.DefaultGiffgaffTemplate()
	template.EnableInitialRejectFallback = true
	cfg := Config{Template: template}
	if !shouldRetryInitialRegisterForStatus(cfg, sip.StatusForbidden) {
		t.Fatal("403 must advance to the next variant")
	}
	for _, status := range []int{sip.StatusOK, sip.StatusUnauthorized, sip.StatusProxyAuthRequired, sip.StatusRequestTimeout} {
		if shouldRetryInitialRegisterForStatus(cfg, status) {
			t.Fatalf("status %d unexpectedly advances the variant", status)
		}
	}
}

func TestBuildRegisterRequestUsesRetriedLocalPortInContact(t *testing.T) {
	profile := voiceclient.DefaultGBEERegisterProfile()
	variant := initialRegisterVariantFromProfile(profile)
	cfg := Config{
		LocalIP:         net.ParseIP("2001:db8::10"),
		PCSCFAddr:       "[2001:db8::20]:5060",
		PublicURI:       "sip:234100000000001@ims.example.org",
		HomeDomain:      "ims.example.org",
		IMSI:            "234100000000001",
		Template:        policy.DefaultGiffgaffTemplate(),
		RegisterProfile: profile,
	}
	req, err := buildRegisterRequest(cfg, registerState{}, true, variant, 34567)
	if err != nil {
		t.Fatal(err)
	}
	assertHeaderContains(t, req, "Contact", "]:34567;transport=tcp")
}

func TestTemporaryRegisterResponseRetryDelay(t *testing.T) {
	res := sip.NewResponse(480, "Temporarily Unavailable")
	res.AppendHeader(sip.NewHeader("Retry-After", "7;duration=2"))
	if got := temporaryRegisterResponseRetryDelay(Config{}, res); got != 7*time.Second {
		t.Fatalf("retry delay = %s", got)
	}
	if got := temporaryRegisterResponseRetryDelay(Config{}, nil); got != 5*time.Second {
		t.Fatalf("default retry delay = %s", got)
	}

	template := policy.DefaultGiffgaffTemplate()
	template.EnableInitialRejectFallback = true
	longRetry := sip.NewResponse(480, "Temporarily Unavailable")
	longRetry.AppendHeader(sip.NewHeader("Retry-After", "1332"))
	if got := temporaryRegisterResponseRetryDelay(Config{Template: template}, longRetry); got != maxMatrixTemporaryRetryDelay {
		t.Fatalf("matrix retry delay = %s, want %s", got, maxMatrixTemporaryRetryDelay)
	}
	if got := temporaryRegisterResponseRetryDelay(Config{}, longRetry); got != 1332*time.Second {
		t.Fatalf("non-matrix retry delay = %s", got)
	}
}

func TestTemporaryRegisterResponseRetryLimitIsBounded(t *testing.T) {
	if maxTemporaryResponseRetries < 1 {
		t.Fatalf("temporary response retry limit = %d, want at least one retry", maxTemporaryResponseRetries)
	}
	if maxTemporaryResponseRetries > 5 {
		t.Fatalf("temporary response retry limit = %d, matrix would stall on repeated 480 responses", maxTemporaryResponseRetries)
	}
}

func TestResetRegisterStateClearsIPSecAndRotatesProtectedClientPort(t *testing.T) {
	installer := &resetTrackingIPSecInstaller{}
	session := newRegisterSession(Config{IPSecInstaller: installer}, nil, nil, "tcp", 0)
	session.state.sipInstance = "urn:uuid:stable"
	session.state.transport = &ipsec3gpp.Transport{}

	session.resetRegisterState()

	if installer.clearCalls != 1 {
		t.Fatalf("clear calls = %d, want 1", installer.clearCalls)
	}
	if session.state.portC == 5064 {
		t.Fatalf("protected client port was not rotated: %d", session.state.portC)
	}
	if session.state.portS != 5063 {
		t.Fatalf("protected server port = %d, want 5063", session.state.portS)
	}
	if session.state.sipInstance != "urn:uuid:stable" {
		t.Fatalf("SIP instance = %q", session.state.sipInstance)
	}
}

func TestTransportTimeoutRetryLimitAllowsRepeatedRetries(t *testing.T) {
	if maxTransportTimeoutRetries < 10 {
		t.Fatalf("transport timeout retry limit = %d, want at least 10", maxTransportTimeoutRetries)
	}
	if maxTransportTimeoutRetries > 20 {
		t.Fatalf("transport timeout retry limit = %d, dead P-CSCF candidate would stall the matrix", maxTransportTimeoutRetries)
	}
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func assertHeaderContains(t *testing.T, req *sip.Request, name, want string) {
	t.Helper()
	header := req.GetHeader(name)
	if header == nil || !strings.Contains(header.Value(), want) {
		t.Fatalf("%s = %v, want substring %q", name, header, want)
	}
}
