package imscore

import (
	"net"

	"github.com/1239t/vowifi-go/engine/sim"
	"github.com/1239t/vowifi-go/internal/vowifi/ipsec3gpp"
	"github.com/1239t/vowifi-go/internal/vowifi/policy"
	"github.com/1239t/vowifi-go/runtimehost/messaging"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

// IPSecInstaller installs a negotiated IMS ESP policy at the SWu inner-packet
// boundary. TCP-stream wrappers cannot implement 3GPP transport mode.
type IPSecInstaller interface {
	InstallIPSec3GPP(ipsec3gpp.Policy, *ipsec3gpp.Transport) error
	ClearIPSec3GPP()
}

// Config configures the RE-based imscore IMS register + messaging service.
type Config struct {
	DeviceID string
	TraceID  string

	LocalIP        net.IP
	Dataplane      voiceclient.PacketDataplane
	IPSecInstaller IPSecInstaller
	PCSCFAddr      string
	// TransportPCSCFAddr overrides the TCP destination for REGISTER when the
	// logical registrar (PCSCFAddr) is the UE inner IPv6 and userspace netstack
	// cannot hairpin to itself.
	TransportPCSCFAddr string
	// RegistrarCandidates is the ordered IKE/ePDG P-CSCF list used for initial
	// REGISTER probing when the first node returns a location/forbidden reject.
	RegistrarCandidates []string

	Realm     string
	PrivateID string
	// EAPPrivateID is the SWu/IKE identity. Some carrier IMS cores also use
	// this NAI as the IMS-AKA private identity, as the stock vohive flow does.
	EAPPrivateID string
	PublicURI    string
	HomeDomain   string
	IMSI         string
	SMSC         string

	AKA sim.AKAProvider

	Template policy.IMSRegisterTemplate

	MCC    string
	MNC    string
	CellID string

	SIPInstanceURN  string
	UserAgent       string
	RegisterProfile voiceclient.RegisterProfile

	RegisterExpirySeconds int

	DeliveryStore           messaging.DeliveryStore
	InboundSMSHandler       messaging.InboundSMSHandler
	InboundSMSResultHandler messaging.InboundSMSResultHandler
}
