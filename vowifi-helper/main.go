package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/1239t/swu-go/pkg/logger"
	externalsim "github.com/1239t/swu-go/pkg/sim"
	"github.com/1239t/vowifi-go/runtimehost"
	"github.com/1239t/vowifi-go/runtimehost/carrier"
	"github.com/1239t/vowifi-go/runtimehost/eventhost"
	"github.com/1239t/vowifi-go/runtimehost/identity"
)

type runtimeObserver struct {
	report   *reporter
	instance *runtimehost.Instance
}

type runtimeEventDispatcher struct {
	report *reporter
}

func (d runtimeEventDispatcher) Dispatch(_ context.Context, event eventhost.Event) {
	learned, ok := event.(eventhost.LocalNumberLearned)
	if !ok || d.report == nil {
		return
	}
	number := strings.TrimSpace(learned.Number)
	if number == "" {
		return
	}
	d.report.mutateIfChanged(func(status *tunnelStatus) {
		status.PhoneNumber = number
	})
}

func (o runtimeObserver) Observe(_ context.Context, event runtimehost.Event) {
	o.report.applyRuntimeState(event.State, o.instance)
}

func main() {
	os.Exit(run())
}

func run() int {
	epdgIP := flag.String("epdg", "", "resolved ePDG IP")
	epdgFQDN := flag.String("epdg-fqdn", "", "ePDG FQDN for status reporting")
	serialDevice := flag.String("serial", "", "AT serial device")
	localIP := flag.String("local-ip", "", "broadband source IP")
	accessInterface := flag.String("access-interface", "", "broadband interface")
	modemID := flag.String("modem-id", "", "selected SimAdmin modem id")
	mcc := flag.String("mcc", "", "home MCC")
	mnc := flag.String("mnc", "", "home MNC")
	liveCellID := flag.String("live-cell-id", "", "live QMI TAC+ECI suffix for IMS REGISTER")
	smsc := flag.String("smsc", "", "SMS service center address")
	phoneNumber := flag.String("phone-number", "", "subscriber number")
	carrierOverrides := flag.String("carrier-overrides", "data/carrier_overrides.json", "VoWiFi carrier overrides JSON")
	statusFile := flag.String("status-file", "", "atomic JSON status file")
	controlSocket := flag.String("control-socket", "", "Unix control socket")
	flag.Parse()
	proxyEnabled := envEnabled("SIMADMIN_VOWIFI_PROXY_ENABLED")
	proxyAddress := strings.TrimSpace(os.Getenv("SIMADMIN_VOWIFI_PROXY_ADDRESS"))
	proxyUsername := strings.TrimSpace(os.Getenv("SIMADMIN_VOWIFI_PROXY_USERNAME"))
	proxyPassword := os.Getenv("SIMADMIN_VOWIFI_PROXY_PASSWORD")

	if *statusFile == "" || *controlSocket == "" {
		fmt.Fprintln(os.Stderr, "status-file and control-socket are required")
		return 2
	}
	initialStatus := tunnelStatus{
		Stage:            "starting",
		Running:          true,
		PID:              os.Getpid(),
		ModemID:          strings.TrimSpace(*modemID),
		SerialDevice:     strings.TrimSpace(*serialDevice),
		AccessInterface:  strings.TrimSpace(*accessInterface),
		LocalIP:          strings.TrimSpace(*localIP),
		EPDGFQDN:         strings.TrimSpace(*epdgFQDN),
		EPDGIP:           strings.TrimSpace(*epdgIP),
		SMSC:             strings.TrimSpace(*smsc),
		PhoneNumber:      strings.TrimSpace(*phoneNumber),
		ProxyEnabled:     proxyEnabled,
		ProxyAddress:     proxyAddress,
		ProxyUsername:    proxyUsername,
		ProxyHasPassword: proxyPassword != "",
		OuterTransport:   map[bool]string{true: "socks5_udp_associate", false: "direct"}[proxyEnabled],
		PCSCFProbeState:  "not_started",
		IMSState:         "not_started",
		IMSTransport:     "none",
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
	restoreReceivedSMSState(*statusFile, &initialStatus)
	report := newReporter(*statusFile, initialStatus)
	report.update("starting", nil)

	if err := validateFlags(*epdgIP, *serialDevice, *localIP, *mcc, *mnc); err != nil {
		report.update("failed", func(status *tunnelStatus) {
			status.Running = false
			status.Error = err.Error()
		})
		return 2
	}
	if proxyEnabled && proxyAddress == "" {
		report.fail("VoWiFi upstream SOCKS5 proxy address is required")
		return 2
	}

	_ = logger.Init("info", "json")
	loadResult, loadErr := carrier.LoadCarrierOverrides(strings.TrimSpace(*carrierOverrides))
	overridesLoaded := loadErr == nil && loadResult != nil && !loadResult.Missing
	if loadErr != nil {
		carrier.ClearCarrierOverrides()
		logger.Warn("carrier overrides unavailable; using built-in carrier settings",
			logger.String("path", strings.TrimSpace(*carrierOverrides)),
			logger.String("error", loadErr.Error()))
	} else if overridesLoaded {
		logger.Info("carrier overrides loaded",
			logger.String("path", loadResult.Path),
			logger.Int("entries", loadResult.Count))
	}
	carrierOptions := resolveCarrierRuntimeOptions(*mcc, *mnc, *liveCellID)
	carrierConfig := carrier.ResolveEffectiveCarrierConfig(carrier.EffectiveCarrierConfigInput{
		MCC: strings.TrimSpace(*mcc),
		MNC: strings.TrimSpace(*mnc),
	})
	registerUserAgent := strings.TrimSpace(carrierOptions.RegisterProfile.UserAgent)
	if registerUserAgent == "" {
		registerUserAgent = "SimAdmin VoWiFi"
	}
	report.mutate(func(status *tunnelStatus) {
		status.CarrierPresetID = carrierConfig.PresetID
		status.CarrierOverridesLoaded = overridesLoaded
		status.IMSCellID = carrierOptions.CellID
		status.IMSCellIDSource = carrierOptions.CellIDSource
		status.IMSRegisterProfile = registerProfileID(carrierOptions.RegisterProfile)
		status.IMSUserAgent = registerUserAgent
		status.PCSCFOverride = carrierOptions.PCSCFAddr
	})
	logger.Info("VoWiFi carrier runtime options resolved",
		logger.String("preset_id", carrierConfig.PresetID),
		logger.String("cell_id", carrierOptions.CellID),
		logger.String("cell_id_source", carrierOptions.CellIDSource),
		logger.String("register_profile", registerProfileID(carrierOptions.RegisterProfile)),
		logger.String("register_user_agent", registerUserAgent),
		logger.Bool("require_sec_agree", carrierOptions.RegisterProfile.IncludeRequireSecAgree),
		logger.String("pcscf_override", carrierOptions.PCSCFAddr))

	directSIM, err := externalsim.NewDirectSIM(*serialDevice)
	if err != nil {
		report.fail("open SIM AT interface: " + err.Error())
		return 1
	}
	adapter := &simAdapter{inner: directSIM}
	imsi, err := adapter.GetIMSI()
	if err != nil {
		_ = adapter.Close()
		report.fail("read IMSI: " + err.Error())
		return 1
	}
	imei, _ := directSIM.GetIMEI()
	report.mutate(func(status *tunnelStatus) {
		prefixLength := 6
		if len(imsi) < prefixLength {
			prefixLength = len(imsi)
		}
		status.IMSIprefix = imsi[:prefixLength]
	})

	profile := identity.Profile{
		IMSI: imsi,
		MCC:  strings.TrimSpace(*mcc),
		MNC:  strings.TrimSpace(*mnc),
		IMEI: imei,
		SMSC: strings.TrimSpace(*smsc),
	}
	// Keep the numeric SIM SMSC for RP-DATA encoding, while allowing the
	// carrier override to identify a distinct public IP-SM-GW PSI on SIP.
	if serviceURI := strings.TrimSpace(carrierOptions.SMSServiceURI); serviceURI != "" {
		profile.SMSC = serviceURI
	}
	runtimeEPDGOverride := strings.TrimSpace(*epdgIP)
	if strings.TrimSpace(carrierConfig.EPDGAddr) != "" {
		runtimeEPDGOverride = ""
	}
	prepared, err := identity.PrepareStart(identity.PrepareStartInput{
		DeviceID:            strings.TrimSpace(*modemID),
		Profile:             profile,
		RuntimeEPDGOverride: runtimeEPDGOverride,
	})
	if err != nil {
		_ = adapter.Close()
		report.fail("prepare IMS identity: " + err.Error())
		return 1
	}
	runtimeContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	var shouldRun atomic.Bool
	shouldRun.Store(true)
	deliveryStore := newMemoryDeliveryStore()
	inbound := newInboundDecoder(report)
	instance, err := runtimehost.Start(runtimeContext, runtimehost.StartRequest{
		Mode:                    runtimehost.StartModeMain,
		DeviceID:                strings.TrimSpace(*modemID),
		TraceID:                 runtimehost.NewTraceID(),
		Profile:                 profile,
		Prepared:                &prepared,
		NetworkMode:             "wifi",
		SIM:                     adapter,
		Dataplane:               runtimehost.DataplanePolicy{Mode: "userspace"},
		Proxy:                   runtimeProxyConfig(proxyEnabled, proxyAddress, proxyUsername, proxyPassword),
		PCSCFAddr:               carrierOptions.PCSCFAddr,
		CellID:                  carrierOptions.CellID,
		RegisterProfile:         carrierOptions.RegisterProfile,
		SIPInstanceURN:          carrierOptions.SIPInstanceURN,
		RegisterExpiry:          carrierOptions.RegisterExpiry,
		DeliveryStore:           deliveryStore,
		InboundSMSHandler:       inbound.handle,
		InboundSMSResultHandler: inbound.handleResult,
		Dispatch:                runtimeEventDispatcher{report: report},
		ShouldRun:               shouldRun.Load,
	})
	if err != nil {
		_ = adapter.Close()
		report.fail("start VoWiFi runtime: " + err.Error())
		return 1
	}
	instance.AddObserver(runtimeObserver{report: report, instance: instance})
	report.applyRuntimeState(instance.State(), instance)

	server := newControlServer(*controlSocket, report, instance, deliveryStore, strings.TrimSpace(*smsc))
	serverError := make(chan error, 1)
	go func() { serverError <- server.serve(runtimeContext) }()

	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-runtimeContext.Done():
			shouldRun.Store(false)
			report.update("stopping", nil)
			stopContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = instance.Stop(stopContext)
			cancel()
			report.update("stopped", func(status *tunnelStatus) {
				resetStoppedStatus(status)
			})
			return 0
		case err := <-serverError:
			if err != nil && !errors.Is(err, context.Canceled) {
				report.fail("control socket: " + err.Error())
				shouldRun.Store(false)
				stopContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = instance.Stop(stopContext)
				cancel()
				return 1
			}
		case <-poll.C:
			report.applyRuntimeState(instance.State(), instance)
		}
	}
}

func resetStoppedStatus(status *tunnelStatus) {
	status.Running = false
	status.Established = false
	status.TunnelIPv4 = ""
	status.TunnelIPv6 = ""
	status.InnerTXPackets = 0
	status.InnerRXPackets = 0
	status.PCSCFv4 = nil
	status.PCSCFv6 = nil
	status.PCSCFAddress = ""
	status.PCSCFProbeState = "not_started"
	status.PCSCFReachable = false
	status.PCSCFSIPCode = 0
	status.PCSCFProbeSent = ""
	status.PCSCFProbeReply = ""
	status.PCSCFProbeError = ""
	status.IMSRegistered = false
	status.IMSAuthenticated = false
	status.IMSState = "not_started"
	status.IMSTransport = "none"
	status.IMSSecurityMode = ""
	status.IMSIPSecEstablished = false
	status.IMSErrorClass = ""
	status.SMSOverIMSReady = false
	status.SMSTXVerified = false
	status.SMSRXVerified = false
	status.SMSLastTXAt = ""
	status.SMSLastTXTo = ""
	status.SMSLastTXText = ""
	status.SMSLastTXMessageID = ""
	status.SMSLastTXSIPCode = 0
	status.SMSLastTXRPState = ""
	status.SMSLastTXRPCause = 0
	status.SMSLastTXError = ""
	status.SMSLastRXAt = ""
	status.SMSLastRXID = ""
	status.SMSLastRXFrom = ""
	status.SMSLastRXText = ""
	status.SMSLastRXRPMR = 0
	status.SMSLastRXRPAckSIPCode = 0
	status.SMSLastRXState = ""
	status.SMSLastRXError = ""
	status.Error = ""
}

func envEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func runtimeProxyConfig(enabled bool, address, username, password string) *runtimehost.ProxyConfig {
	if !enabled {
		return nil
	}
	return &runtimehost.ProxyConfig{
		Addr:     strings.TrimSpace(address),
		Username: strings.TrimSpace(username),
		Password: password,
		Enabled:  true,
	}
}

func validateFlags(epdgIP, serialDevice, localIP, mcc, mnc string) error {
	if strings.TrimSpace(epdgIP) == "" || strings.TrimSpace(serialDevice) == "" ||
		strings.TrimSpace(localIP) == "" || strings.TrimSpace(mcc) == "" || strings.TrimSpace(mnc) == "" {
		return errors.New("epdg, serial, local-ip, mcc and mnc are required")
	}
	if net.ParseIP(epdgIP) == nil || net.ParseIP(localIP) == nil {
		return errors.New("epdg and local-ip must be IP addresses")
	}
	return nil
}
