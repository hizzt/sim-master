use base64::{engine::general_purpose, Engine as _};
use serde::{Deserialize, Serialize};
use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::{Arc, Mutex as StdMutex};
use std::time::Duration;
use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::UnixStream;
use tokio::process::Command;
use tokio::sync::Mutex;
use tracing::{info, warn};
use zbus::Connection;

use crate::db::{Database, SmsMessage};
use crate::modem_manager::{get_airplane_mode, set_airplane_mode};
use crate::notification::NotificationSender;

const HELPER_FILENAME: &str = "simadmin-vowifi-helper";
const STATUS_FILENAME: &str = "vowifi-tunnel.json";
const CONTROL_FILENAME: &str = "vowifi-control.sock";
const CARRIER_OVERRIDES_FILENAME: &str = "data/carrier_overrides.json";
const UPSTREAM_PROXY_SETTING_KEY: &str = "vowifi_upstream_proxy";
/// SIM AKA 后端选择：`at`（默认，AT+CSIM）或 `qmi`（QMI UIM 原生 APDU）。
/// 存储为 app_settings 键，面板 UI 可在 VoWiFi 设置页切换。
pub(crate) const DEVICE_BACKEND_SETTING_KEY: &str = "vowifi_device_backend";
const MAX_CALL_AUDIO_RAW_BYTES: usize = 1_920_000;
const MAX_CALL_WAV_CONTAINER_OVERHEAD: usize = 64 * 1024;

#[derive(Debug, Clone)]
pub struct VowifiTunnelLaunchConfig {
    pub modem_id: String,
    pub serial_device: String,
    pub access_interface: String,
    pub local_ip: String,
    pub epdg_fqdn: String,
    pub epdg_ip: String,
    pub mcc: String,
    pub mnc: String,
    pub live_cell_id: String,
    pub smsc: String,
    pub phone_number: String,
    pub proxy_enabled: bool,
    pub proxy_address: String,
    pub proxy_username: String,
    pub proxy_password: String,
    pub device_backend: String,
    pub qmi_device: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct VowifiTunnelStartRequest {
    pub proxy: Option<VowifiUpstreamProxyRequest>,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct VowifiUpstreamProxyRequest {
    pub enabled: bool,
    pub address: String,
    pub username: String,
    pub password: Option<String>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
struct StoredVowifiUpstreamProxy {
    enabled: bool,
    address: String,
    username: String,
    password: String,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct VowifiTunnelStatus {
    pub stage: String,
    pub running: bool,
    pub established: bool,
    pub pid: u32,
    pub modem_id: String,
    pub serial_device: String,
    pub access_interface: String,
    pub local_ip: String,
    pub epdg_fqdn: String,
    pub epdg_ip: String,
    pub tunnel_ipv4: String,
    pub tunnel_ipv6: String,
    #[serde(default)]
    pub pcscf_v4: Vec<String>,
    #[serde(default)]
    pub pcscf_v6: Vec<String>,
    pub pcscf_address: String,
    pub pcscf_override: String,
    pub imsi_prefix: String,
    pub phone_number: String,
    pub smsc: String,
    pub proxy_enabled: bool,
    pub proxy_address: String,
    pub proxy_username: String,
    pub proxy_has_password: bool,
    pub outer_transport: String,
    pub inner_tx_packets: u64,
    pub inner_rx_packets: u64,
    pub pcscf_probe_state: String,
    pub pcscf_reachable: bool,
    pub pcscf_sip_code: u16,
    pub pcscf_probe_sent_at: String,
    pub pcscf_probe_response_at: String,
    pub pcscf_probe_error: String,
    pub ims_registration_state: String,
    pub ims_registered: bool,
    pub ims_authenticated: bool,
    pub ims_transport: String,
    pub ims_security_mode: String,
    pub ims_ipsec_established: bool,
    pub ims_error_class: String,
    pub carrier_preset_id: String,
    pub carrier_overrides_loaded: bool,
    pub ims_cell_id: String,
    pub ims_cell_id_source: String,
    pub ims_register_profile: String,
    pub ims_user_agent: String,
    pub sms_over_ims_ready: bool,
    pub sms_tx_path_verified: bool,
    pub sms_rx_path_verified: bool,
    pub sms_last_tx_at: String,
    pub sms_last_tx_to: String,
    pub sms_last_tx_text: String,
    pub sms_last_tx_message_id: String,
    pub sms_last_tx_sip_code: u16,
    pub sms_last_tx_rp_state: String,
    pub sms_last_tx_rp_cause: u16,
    pub sms_last_tx_error: String,
    pub sms_last_rx_at: String,
    pub sms_last_rx_id: String,
    pub sms_last_rx_from: String,
    pub sms_last_rx_text: String,
    pub sms_last_rx_rp_mr: u16,
    pub sms_last_rx_rp_ack_sip_code: u16,
    pub sms_last_rx_state: String,
    pub sms_last_rx_error: String,
    pub sms_received_messages: Vec<VowifiReceivedSms>,
    pub started_at: String,
    pub updated_at: String,
    pub error: String,
    #[serde(default)]
    pub helper_available: bool,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct VowifiReceivedSms {
    pub id: String,
    pub from: String,
    pub text: String,
    pub received_at: String,
    pub rp_mr: u16,
    pub rp_ack_sip_code: u16,
}

#[derive(Debug, Clone, Default, Serialize)]
pub struct VowifiVerificationCheck {
    pub state: String,
    pub evidence: String,
    pub observed_at: String,
}

#[derive(Debug, Clone, Default, Serialize)]
pub struct VowifiVerificationStatus {
    pub enabled: bool,
    pub verdict: String,
    pub summary: String,
    pub swu: VowifiVerificationCheck,
    pub pcscf: VowifiVerificationCheck,
    pub ims: VowifiVerificationCheck,
    pub sms_send: VowifiVerificationCheck,
    pub sms_receive: VowifiVerificationCheck,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SmsPathVerificationRequest {
    pub direction: String,
    #[serde(default)]
    pub phone_number: String,
    #[serde(default)]
    pub content: String,
    #[serde(default)]
    pub encoding: String,
    #[serde(default)]
    pub timeout_seconds: u64,
    #[serde(default)]
    pub after_id: String,
}

#[derive(Debug, Clone, Default, Serialize)]
pub struct SmsPathVerificationResult {
    pub direction: String,
    pub verified: bool,
    pub state: String,
    pub transport: String,
    pub evidence: String,
    pub observed_at: String,
    pub phone_number: String,
    pub content: String,
    pub message_id: String,
    pub sip_code: u16,
    pub rp_state: String,
    pub rp_cause: u16,
    pub from: String,
    pub rp_mr: u16,
    pub rp_ack_sip_code: u16,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct VowifiDialCallRequest {
    pub phone_number: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct VowifiHangupCallRequest {
    pub call_id: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct VowifiCallAudioPlayRequest {
    pub call_id: String,
    pub audio_format: String,
    pub audio_base64: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct VowifiCallAudioRequest {
    pub call_id: String,
    pub audio_format: String,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct VowifiCallAudioStats {
    pub call_id: String,
    pub codec: String,
    pub sample_rate: u32,
    pub frame_duration_ms: u32,
    pub playback_active: bool,
    pub rtp_packets_sent: u64,
    pub rtp_bytes_sent: u64,
    pub pcm_samples_sent: u64,
    pub audio_packets_decoded: u64,
    pub audio_samples_recorded: u64,
    pub recording_bytes: u64,
    pub recording_duration_ms: u64,
    pub recording_truncated: bool,
    pub rtp_packets_lost: u64,
    pub rtp_packets_out_of_order: u64,
    pub last_playback_at: String,
    pub last_playback_error: String,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct VowifiCallAudio {
    pub call_id: String,
    pub format: String,
    pub content_type: String,
    pub sample_rate: u32,
    pub channels: u16,
    pub bits_per_sample: u16,
    pub data_base64: String,
    pub stats: VowifiCallAudioStats,
}

/// A real IMS SIP dialog initiated through the host-side SWu stack.
/// Media readiness reports valid RTP reception. G.711 supports file-backed
/// audio; live microphone and speaker devices are not attached.
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct VowifiCallStatus {
    pub call_id: String,
    pub phone_number: String,
    pub state: String,
    pub sip_code: u16,
    pub reason: String,
    pub started_at: String,
    pub updated_at: String,
    pub media_ready: bool,
    pub media_supported: bool,
    pub media_mode: String,
    pub media_codec: String,
    pub media_direction: String,
    pub audio_ready: bool,
    pub audio_mode: String,
    pub rtp_packets_received: u64,
    pub rtp_bytes_received: u64,
    pub rtp_packets_sent: u64,
    pub rtp_bytes_sent: u64,
    pub rtcp_packets_received: u64,
    pub rtcp_bytes_received: u64,
    pub audio_packets_decoded: u64,
    pub audio_samples_recorded: u64,
    pub audio_recording_bytes: u64,
    pub audio_recording_truncated: bool,
    pub rtp_packets_lost: u64,
    pub rtp_packets_out_of_order: u64,
}

#[derive(Debug, Serialize)]
struct HelperControlRequest<'a> {
    action: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    call_id: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    phone_number: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    content: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    encoding: &'a str,
    timeout_seconds: u64,
    #[serde(skip_serializing_if = "str::is_empty")]
    after_id: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    audio_format: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    audio_base64: &'a str,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct HelperControlResponse {
    ok: bool,
    error: String,
    send: Option<HelperSmsSendResult>,
    receive: Option<HelperSmsReceiveResult>,
    call: Option<VowifiCallStatus>,
    calls: Vec<VowifiCallStatus>,
    audio: Option<VowifiCallAudio>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct HelperSmsSendResult {
    verified: bool,
    state: String,
    message_id: String,
    sip_code: u16,
    rp_state: String,
    rp_cause: u16,
    evidence: String,
    observed_at: String,
    phone_number: String,
    content: String,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct HelperSmsReceiveResult {
    verified: bool,
    state: String,
    message_id: String,
    from: String,
    content: String,
    rp_mr: u16,
    rp_ack_sip_code: u16,
    evidence: String,
    observed_at: String,
}

impl VowifiTunnelStatus {
    fn stopped(helper_available: bool) -> Self {
        Self {
            stage: "stopped".to_string(),
            helper_available,
            ..Default::default()
        }
    }

    fn starting(config: &VowifiTunnelLaunchConfig, pid: u32, helper_available: bool) -> Self {
        Self {
            stage: "starting".to_string(),
            running: true,
            pid,
            modem_id: config.modem_id.clone(),
            serial_device: config.serial_device.clone(),
            access_interface: config.access_interface.clone(),
            local_ip: config.local_ip.clone(),
            epdg_fqdn: config.epdg_fqdn.clone(),
            epdg_ip: config.epdg_ip.clone(),
            phone_number: config.phone_number.clone(),
            smsc: config.smsc.clone(),
            proxy_enabled: config.proxy_enabled,
            proxy_address: config.proxy_address.clone(),
            proxy_username: config.proxy_username.clone(),
            proxy_has_password: !config.proxy_password.is_empty(),
            outer_transport: if config.proxy_enabled {
                "socks5_udp_associate".to_string()
            } else {
                "direct".to_string()
            },
            helper_available,
            ..Default::default()
        }
    }

    pub fn verification(&self) -> VowifiVerificationStatus {
        let swu = if self.established && self.running {
            VowifiVerificationCheck {
                state: "passed".to_string(),
                evidence: format!(
                    "已与 ePDG {} 建立 SWu Child SA，隧道地址 {}",
                    self.epdg_ip,
                    first_non_empty(&self.tunnel_ipv6, &self.tunnel_ipv4)
                ),
                observed_at: self.updated_at.clone(),
            }
        } else if self.running {
            VowifiVerificationCheck {
                state: "running".to_string(),
                evidence: format!("SWu 握手阶段：{}", self.stage),
                observed_at: self.updated_at.clone(),
            }
        } else if self.stage == "failed" {
            VowifiVerificationCheck {
                state: "failed".to_string(),
                evidence: if self.error.is_empty() {
                    "SWu helper 报告失败".to_string()
                } else {
                    self.error.clone()
                },
                observed_at: self.updated_at.clone(),
            }
        } else {
            VowifiVerificationCheck {
                state: "not_started".to_string(),
                evidence: "没有运行中的 SWu 会话".to_string(),
                observed_at: self.updated_at.clone(),
            }
        };

        let pcscf = if self.pcscf_reachable {
            VowifiVerificationCheck {
                state: "passed".to_string(),
                evidence: if self.pcscf_sip_code == 0 {
                    "已通过 SWu 收到 P-CSCF 的 SIP 响应".to_string()
                } else {
                    format!(
                        "P-CSCF 已通过 SWu 响应初始 SIP REGISTER：SIP {}",
                        self.pcscf_sip_code
                    )
                },
                observed_at: self.pcscf_probe_response_at.clone(),
            }
        } else if self.pcscf_probe_state == "sent" {
            VowifiVerificationCheck {
                state: "running".to_string(),
                evidence: "已通过 SWu 向 P-CSCF 发送初始 SIP REGISTER".to_string(),
                observed_at: self.pcscf_probe_sent_at.clone(),
            }
        } else if matches!(self.pcscf_probe_state.as_str(), "failed" | "timed_out") {
            VowifiVerificationCheck {
                state: "failed".to_string(),
                evidence: if self.pcscf_probe_error.is_empty() {
                    "P-CSCF 未通过 SWu 返回 SIP 响应".to_string()
                } else {
                    self.pcscf_probe_error.clone()
                },
                observed_at: self.updated_at.clone(),
            }
        } else {
            VowifiVerificationCheck {
                state: if self.established {
                    "unavailable".to_string()
                } else {
                    "blocked".to_string()
                },
                evidence: if self.established {
                    first_non_empty(&self.pcscf_probe_error, "P-CSCF 探测没有可用地址").to_string()
                } else {
                    "P-CSCF 验证需要先建立 SWu 会话".to_string()
                },
                observed_at: self.updated_at.clone(),
            }
        };

        let ims_registered_over_swu = self.established
            && self.ims_registered
            && self.ims_authenticated
            && self.ims_transport == "swu";
        let ims_ipsec_protected = ims_registered_over_swu
            && self.ims_ipsec_established
            && self.ims_security_mode == "ipsec3gpp";
        // A carrier may accept an IMS-AKA authenticated REGISTER without
        // offering a separate 3GPP IMS IPsec Security-Agreement. In that
        // case IMS is still registered over the encrypted SWu tunnel; report
        // the missing optional security agreement separately instead of
        // claiming that authentication or registration is incomplete.
        let ims_enabled = ims_registered_over_swu;
        let location_rejected = self.ims_registration_state == "register_location_rejected"
            || self
                .error
                .to_ascii_lowercase()
                .contains("service not allowed in this location");
        let ims_failed = matches!(
            self.ims_registration_state.as_str(),
            "register_failed" | "register_location_rejected"
        );
        let ims = if ims_ipsec_protected {
            VowifiVerificationCheck {
                state: "passed".to_string(),
                evidence:
                    "已确认通过 SWu 完成 IMS-AKA 注册，3GPP IMS IPsec Security-Agreement 已生效"
                        .to_string(),
                observed_at: self.updated_at.clone(),
            }
        } else if ims_registered_over_swu {
            VowifiVerificationCheck {
                state: "passed".to_string(),
                evidence: "已确认通过 SWu 完成 IMS-AKA 鉴权注册；P-CSCF 未协商独立的 3GPP IMS IPsec Security-Agreement，IMS 信令由 SWu 隧道承载"
                    .to_string(),
                observed_at: first_non_empty(
                    &self.pcscf_probe_response_at,
                    &self.updated_at,
                )
                .to_string(),
            }
        } else if location_rejected {
            VowifiVerificationCheck {
                state: "failed".to_string(),
                evidence: "P-CSCF 已返回 SIP 403：运营商拒绝当前位置的 IMS 服务".to_string(),
                observed_at: first_non_empty(&self.pcscf_probe_response_at, &self.updated_at)
                    .to_string(),
            }
        } else if ims_failed {
            VowifiVerificationCheck {
                state: "failed".to_string(),
                evidence: first_non_empty(&self.error, "IMS 注册失败").to_string(),
                observed_at: self.updated_at.clone(),
            }
        } else if self.pcscf_reachable {
            VowifiVerificationCheck {
                state: "incomplete".to_string(),
                evidence: format!(
                    "P-CSCF 可达，但 IMS 鉴权注册未完成（{}）",
                    ims_state_label(&self.ims_registration_state)
                ),
                observed_at: first_non_empty(&self.pcscf_probe_response_at, &self.updated_at)
                    .to_string(),
            }
        } else {
            VowifiVerificationCheck {
                state: "blocked".to_string(),
                evidence: "没有 IMS 鉴权注册经过 SWu/P-CSCF 的路径证据".to_string(),
                observed_at: self.updated_at.clone(),
            }
        };

        let sms_send = sms_send_check(self);
        let sms_receive = sms_receive_check(self);
        let (verdict, summary) = if ims_ipsec_protected {
            (
                "enabled".to_string(),
                "VoWiFi 已启用：IMS 已通过 SWu 完成 AKA 鉴权注册，3GPP IMS IPsec Security-Agreement 已生效。"
                    .to_string(),
            )
        } else if ims_registered_over_swu {
            (
                "enabled".to_string(),
                "VoWiFi 已启用：IMS 已通过 SWu 完成 AKA 鉴权注册；P-CSCF 未协商独立的 3GPP IMS IPsec。"
                    .to_string(),
            )
        } else if location_rejected {
            (
                "failed".to_string(),
                "ePDG/SWu 已建立，但运营商拒绝当前位置的 IMS 服务；请使用运营商允许地区的网络出口或 VoWiFi 上游 SOCKS5 UDP 代理。".to_string(),
            )
        } else if ims_failed {
            (
                "failed".to_string(),
                "ePDG/SWu 已建立，但 IMS 注册失败；请查看 P-CSCF/IMS 证据中的具体错误。"
                    .to_string(),
            )
        } else if self.established {
            (
                "tunnel_only".to_string(),
                "仅证明 ePDG/SWu 隧道已建立；缺少 IMS 鉴权注册，VoWiFi 尚未启用。".to_string(),
            )
        } else if self.running {
            (
                "connecting".to_string(),
                "正在建立 ePDG/SWu 隧道。".to_string(),
            )
        } else if self.stage == "failed" {
            (
                "failed".to_string(),
                "VoWiFi 在完成 IMS 鉴权注册前验证失败。".to_string(),
            )
        } else {
            (
                "disabled".to_string(),
                "没有运行中的 SWu 会话，VoWiFi 未启用。".to_string(),
            )
        };

        VowifiVerificationStatus {
            enabled: ims_enabled,
            verdict,
            summary,
            swu,
            pcscf,
            ims,
            sms_send,
            sms_receive,
        }
    }
}

fn first_non_empty<'a>(first: &'a str, second: &'a str) -> &'a str {
    if first.trim().is_empty() {
        second
    } else {
        first
    }
}

fn ims_state_label(state: &str) -> &str {
    match state {
        "initial_register_sent" => "已发送初始 REGISTER",
        "aka_challenge_received" => "已收到 IMS-AKA 挑战",
        "security_agreement_required" => "需要完成 IMS 安全协商",
        "register_response_received" => "已收到未鉴权 REGISTER 响应",
        "register_rejected" => "初始 REGISTER 被拒绝",
        "register_location_rejected" => "运营商拒绝当前位置",
        "pcscf_no_response" => "P-CSCF 无响应",
        "registering" => "IMS-AKA / Security-Agreement 注册中",
        "register_failed" => "IMS 注册失败",
        "registered" => "IMS 已注册",
        _ => "未注册",
    }
}

fn sms_send_check(status: &VowifiTunnelStatus) -> VowifiVerificationCheck {
    if status.sms_tx_path_verified {
        VowifiVerificationCheck {
            state: "passed".to_string(),
            evidence: format!(
                "真实 SMS-over-IMS 发送成功：SIP {}，异步 {}",
                status.sms_last_tx_sip_code, status.sms_last_tx_rp_state
            ),
            observed_at: status.sms_last_tx_at.clone(),
        }
    } else if !status.sms_last_tx_error.is_empty()
        || status.sms_last_tx_rp_state.ends_with("failed")
    {
        VowifiVerificationCheck {
            state: "failed".to_string(),
            evidence: if status.sms_last_tx_error.is_empty() {
                format!("SMS over IMS 发送失败：{}", status.sms_last_tx_rp_state)
            } else {
                status.sms_last_tx_error.clone()
            },
            observed_at: status.sms_last_tx_at.clone(),
        }
    } else if status.sms_over_ims_ready {
        VowifiVerificationCheck {
            state: if status.sms_last_tx_sip_code == 202 {
                "incomplete".to_string()
            } else {
                "not_tested".to_string()
            },
            evidence: if status.sms_last_tx_sip_code == 202 {
                "SIP 202 已接受，仍在等待网络异步 RP-ACK".to_string()
            } else {
                "SMS over IMS 已就绪，但尚未执行真实发送".to_string()
            },
            observed_at: status.sms_last_tx_at.clone(),
        }
    } else {
        VowifiVerificationCheck {
            state: "blocked".to_string(),
            evidence: "IMS 鉴权注册和 3GPP SMS 运行时尚未就绪，无法验证 SMS over IMS".to_string(),
            observed_at: status.sms_last_tx_at.clone(),
        }
    }
}

fn sms_receive_check(status: &VowifiTunnelStatus) -> VowifiVerificationCheck {
    if status.sms_rx_path_verified {
        VowifiVerificationCheck {
            state: "passed".to_string(),
            evidence: format!(
                "已解码真实 SMS-DELIVER，并以 SIP {} 完成 RP-ACK 协议确认",
                status.sms_last_rx_rp_ack_sip_code
            ),
            observed_at: status.sms_last_rx_at.clone(),
        }
    } else if status.sms_over_ims_ready {
        VowifiVerificationCheck {
            state: if status.sms_last_rx_state.ends_with("failed") {
                "failed".to_string()
            } else {
                "not_tested".to_string()
            },
            evidence: if status.sms_last_rx_error.is_empty() {
                "SMS over IMS 已就绪，正在等待真实下行 SMS-DELIVER".to_string()
            } else {
                status.sms_last_rx_error.clone()
            },
            observed_at: status.sms_last_rx_at.clone(),
        }
    } else {
        VowifiVerificationCheck {
            state: "blocked".to_string(),
            evidence: "IMS 鉴权注册和 3GPP SMS 运行时尚未就绪，无法验证 SMS over IMS".to_string(),
            observed_at: status.sms_last_rx_at.clone(),
        }
    }
}

#[derive(Default)]
struct RuntimeState {
    pid: Option<u32>,
    fallback_status: Option<VowifiTunnelStatus>,
    restore_radio_after_stop: bool,
}

pub struct VowifiTunnelManager {
    helper_path: PathBuf,
    status_path: PathBuf,
    control_path: PathBuf,
    database: Option<Arc<Database>>,
    dbus_conn: Option<Arc<Connection>>,
    airplane_mode_requested: Option<Arc<std::sync::atomic::AtomicBool>>,
    runtime: Arc<Mutex<RuntimeState>>,
    lifecycle: Arc<Mutex<()>>,
    notification_sender: Arc<StdMutex<Option<Arc<NotificationSender>>>>,
}

impl VowifiTunnelManager {
    pub fn new_default(
        database: Arc<Database>,
        dbus_conn: Arc<Connection>,
        airplane_mode_requested: Arc<std::sync::atomic::AtomicBool>,
    ) -> Self {
        Self::new_with_database(
            default_helper_path(),
            default_status_path(),
            default_control_path(),
            Some(database),
            Some(dbus_conn),
            Some(airplane_mode_requested),
        )
    }

    pub fn new(helper_path: PathBuf, status_path: PathBuf) -> Self {
        let control_path = status_path.with_file_name(CONTROL_FILENAME);
        Self::new_with_database(helper_path, status_path, control_path, None, None, None)
    }

    fn new_with_database(
        helper_path: PathBuf,
        status_path: PathBuf,
        control_path: PathBuf,
        database: Option<Arc<Database>>,
        dbus_conn: Option<Arc<Connection>>,
        airplane_mode_requested: Option<Arc<std::sync::atomic::AtomicBool>>,
    ) -> Self {
        Self {
            helper_path,
            status_path,
            control_path,
            database,
            dbus_conn,
            airplane_mode_requested,
            runtime: Arc::new(Mutex::new(RuntimeState::default())),
            lifecycle: Arc::new(Mutex::new(())),
            notification_sender: Arc::new(StdMutex::new(None)),
        }
    }

    pub fn set_notification_sender(&self, sender: Arc<NotificationSender>) {
        *self.notification_sender.lock().unwrap() = Some(sender);
    }

    pub fn helper_available(&self) -> bool {
        helper_is_available_at(&self.helper_path)
    }

    pub async fn status(&self) -> VowifiTunnelStatus {
        let helper_available = self.helper_available();
        let mut parsed = match tokio::fs::read(&self.status_path).await {
            Ok(bytes) => match serde_json::from_slice::<VowifiTunnelStatus>(&bytes) {
                Ok(status) => Some(status),
                Err(error) => {
                    warn!(path = %self.status_path.display(), error = %error, "Invalid VoWiFi tunnel status file");
                    None
                }
            },
            Err(error) if error.kind() == io::ErrorKind::NotFound => None,
            Err(error) => {
                warn!(path = %self.status_path.display(), error = %error, "Failed to read VoWiFi tunnel status file");
                None
            }
        };

        let (runtime_pid, fallback) = {
            let runtime = self.runtime.lock().await;
            (runtime.pid, runtime.fallback_status.clone())
        };
        // Every helper writes to the same atomic status path.  If an older
        // process survived a previous stop, its final/poll update must never
        // replace the status of the process this manager actually owns.
        if runtime_pid.is_some_and(|pid| parsed.as_ref().is_some_and(|status| status.pid != pid)) {
            parsed = None;
        }
        let mut status = parsed
            .or(fallback)
            .unwrap_or_else(|| VowifiTunnelStatus::stopped(helper_available));
        status.helper_available = helper_available;

        if let Ok(proxy) = self.load_upstream_proxy() {
            status.proxy_enabled = proxy.enabled;
            status.proxy_address = proxy.address;
            status.proxy_username = proxy.username;
            status.proxy_has_password = !proxy.password.is_empty();
            if status.outer_transport.is_empty() {
                status.outer_transport = if proxy.enabled {
                    "socks5_udp_associate".to_string()
                } else {
                    "direct".to_string()
                };
            }
        }

        if status.running && (status.pid == 0 || !pid_is_helper(status.pid, &self.helper_path)) {
            status.running = false;
            status.established = false;
            status.stage = "failed".to_string();
            if status.error.is_empty() {
                status.error = "SWu helper process is no longer running".to_string();
            }
        }
        if let Some(database) = &self.database {
            sync_sms_to_database(database, &status, self.notification_sender.clone());
        }
        status
    }

    pub async fn verification_status(&self) -> VowifiVerificationStatus {
        self.status().await.verification()
    }

    pub async fn verify_sms_path(
        &self,
        request: &SmsPathVerificationRequest,
    ) -> Result<SmsPathVerificationResult, String> {
        let status = self.status().await;
        if !status.running || !status.sms_over_ims_ready {
            return Err("IMS registration and SMS over IMS are not ready".to_string());
        }
        let direction = request.direction.trim();
        let action = match direction {
            "send" => "send_sms",
            "receive" => "wait_receive",
            _ => return Err("direction must be send or receive".to_string()),
        };
        if direction == "send"
            && (request.phone_number.trim().is_empty() || request.content.trim().is_empty())
        {
            return Err("phone_number and content are required for send verification".to_string());
        }
        let timeout_seconds = if request.timeout_seconds == 0 {
            if direction == "send" {
                30
            } else {
                60
            }
        } else {
            request.timeout_seconds.min(120)
        };
        let response = self
            .control_request(
                &HelperControlRequest {
                    action,
                    call_id: "",
                    phone_number: request.phone_number.trim(),
                    content: request.content.trim(),
                    encoding: request.encoding.trim(),
                    timeout_seconds,
                    after_id: request.after_id.trim(),
                    audio_format: "",
                    audio_base64: "",
                },
                Duration::from_secs(timeout_seconds + 10),
            )
            .await?;
        if !response.ok {
            return Err(if response.error.is_empty() {
                "VoWiFi helper rejected the SMS verification request".to_string()
            } else {
                response.error
            });
        }

        let result = if direction == "send" {
            let result = response
                .send
                .ok_or_else(|| "VoWiFi helper omitted the send result".to_string())?;
            SmsPathVerificationResult {
                direction: direction.to_string(),
                verified: result.verified,
                state: result.state,
                transport: "swu".to_string(),
                evidence: result.evidence,
                observed_at: result.observed_at,
                phone_number: result.phone_number,
                content: result.content,
                message_id: result.message_id,
                sip_code: result.sip_code,
                rp_state: result.rp_state,
                rp_cause: result.rp_cause,
                ..Default::default()
            }
        } else {
            let result = response
                .receive
                .ok_or_else(|| "VoWiFi helper omitted the receive result".to_string())?;
            SmsPathVerificationResult {
                direction: direction.to_string(),
                verified: result.verified,
                state: result.state,
                transport: "swu".to_string(),
                evidence: result.evidence,
                observed_at: result.observed_at,
                content: result.content,
                message_id: result.message_id,
                from: result.from,
                rp_mr: result.rp_mr,
                rp_ack_sip_code: result.rp_ack_sip_code,
                ..Default::default()
            }
        };
        let refreshed = self.status().await;
        if let Some(database) = &self.database {
            sync_sms_to_database(database, &refreshed, self.notification_sender.clone());
        }
        Ok(result)
    }

    pub async fn dial_call(
        &self,
        request: &VowifiDialCallRequest,
    ) -> Result<VowifiCallStatus, String> {
        let phone_number = request.phone_number.trim();
        if phone_number.is_empty() {
            return Err("phone_number is required".to_string());
        }
        let status = self.status().await;
        if !ims_call_signaling_ready(&status) {
            return Err(
                "IMS must be authenticated and registered over the active SWu tunnel".to_string(),
            );
        }
        let response = self
            .control_request(
                &HelperControlRequest {
                    action: "dial_call",
                    call_id: "",
                    phone_number,
                    content: "",
                    encoding: "",
                    timeout_seconds: 0,
                    after_id: "",
                    audio_format: "",
                    audio_base64: "",
                },
                Duration::from_secs(10),
            )
            .await?;
        if !response.ok {
            return Err(helper_call_error(
                response.error,
                "VoWiFi INVITE was rejected",
            ));
        }
        response
            .call
            .ok_or_else(|| "VoWiFi helper omitted the call status".to_string())
    }

    pub async fn hangup_call(
        &self,
        request: &VowifiHangupCallRequest,
    ) -> Result<VowifiCallStatus, String> {
        let call_id = request.call_id.trim();
        if call_id.is_empty() {
            return Err("call_id is required".to_string());
        }
        let response = self
            .control_request(
                &HelperControlRequest {
                    action: "hangup_call",
                    call_id,
                    phone_number: "",
                    content: "",
                    encoding: "",
                    timeout_seconds: 0,
                    after_id: "",
                    audio_format: "",
                    audio_base64: "",
                },
                Duration::from_secs(15),
            )
            .await?;
        if !response.ok {
            return Err(helper_call_error(
                response.error,
                "VoWiFi hangup was rejected",
            ));
        }
        response
            .call
            .ok_or_else(|| "VoWiFi helper omitted the call status".to_string())
    }

    pub async fn call_statuses(&self) -> Result<Vec<VowifiCallStatus>, String> {
        let response = self
            .control_request(
                &HelperControlRequest {
                    action: "voice_status",
                    call_id: "",
                    phone_number: "",
                    content: "",
                    encoding: "",
                    timeout_seconds: 0,
                    after_id: "",
                    audio_format: "",
                    audio_base64: "",
                },
                Duration::from_secs(5),
            )
            .await?;
        if !response.ok {
            return Err(helper_call_error(
                response.error,
                "VoWiFi call status request was rejected",
            ));
        }
        Ok(response.calls)
    }

    pub async fn play_call_audio(
        &self,
        request: &VowifiCallAudioPlayRequest,
    ) -> Result<VowifiCallAudio, String> {
        let call_id = require_call_id(&request.call_id)?;
        let audio_format = normalize_audio_format(&request.audio_format, "wav")?;
        let audio_base64 = request.audio_base64.trim();
        if audio_base64.is_empty() {
            return Err("audio_base64 is required".to_string());
        }
        let decoded = general_purpose::STANDARD
            .decode(audio_base64)
            .map_err(|error| format!("audio_base64 is invalid: {error}"))?;
        if decoded.is_empty() {
            return Err("audio payload is empty".to_string());
        }
        let max_decoded_bytes = if audio_format == "wav" {
            MAX_CALL_AUDIO_RAW_BYTES + MAX_CALL_WAV_CONTAINER_OVERHEAD
        } else {
            MAX_CALL_AUDIO_RAW_BYTES
        };
        if decoded.len() > max_decoded_bytes {
            return Err(format!(
                "audio payload exceeds the {} byte PCM (120 second) limit",
                MAX_CALL_AUDIO_RAW_BYTES
            ));
        }
        drop(decoded);

        self.call_audio_control(
            "play_audio",
            call_id,
            audio_format,
            audio_base64,
            Duration::from_secs(135),
        )
        .await
    }

    pub async fn call_recording(
        &self,
        request: &VowifiCallAudioRequest,
    ) -> Result<VowifiCallAudio, String> {
        let call_id = require_call_id(&request.call_id)?;
        let audio_format = normalize_audio_format(&request.audio_format, "wav")?;
        self.call_audio_control(
            "get_recording",
            call_id,
            audio_format,
            "",
            Duration::from_secs(15),
        )
        .await
    }

    pub async fn call_audio_stats(
        &self,
        request: &VowifiCallAudioRequest,
    ) -> Result<VowifiCallAudio, String> {
        let call_id = require_call_id(&request.call_id)?;
        self.call_audio_control("audio_stats", call_id, "", "", Duration::from_secs(5))
            .await
    }

    async fn call_audio_control(
        &self,
        action: &str,
        call_id: &str,
        audio_format: &str,
        audio_base64: &str,
        timeout: Duration,
    ) -> Result<VowifiCallAudio, String> {
        let response = self
            .control_request(
                &HelperControlRequest {
                    action,
                    call_id,
                    phone_number: "",
                    content: "",
                    encoding: "",
                    timeout_seconds: 0,
                    after_id: "",
                    audio_format,
                    audio_base64,
                },
                timeout,
            )
            .await?;
        if !response.ok {
            return Err(helper_call_error(
                response.error,
                "VoWiFi audio request was rejected",
            ));
        }
        response
            .audio
            .ok_or_else(|| "VoWiFi helper omitted the audio result".to_string())
    }

    async fn control_request(
        &self,
        request: &HelperControlRequest<'_>,
        timeout: Duration,
    ) -> Result<HelperControlResponse, String> {
        let payload = serde_json::to_vec(request)
            .map_err(|error| format!("Failed to encode VoWiFi helper request: {error}"))?;
        tokio::time::timeout(timeout, async {
            let mut stream = UnixStream::connect(&self.control_path)
                .await
                .map_err(|error| {
                    format!(
                        "Failed to connect to VoWiFi helper at {}: {error}",
                        self.control_path.display()
                    )
                })?;
            stream
                .write_all(&payload)
                .await
                .map_err(|error| format!("Failed to send VoWiFi helper request: {error}"))?;
            stream
                .shutdown()
                .await
                .map_err(|error| format!("Failed to finish VoWiFi helper request: {error}"))?;
            let mut response = Vec::new();
            stream
                .read_to_end(&mut response)
                .await
                .map_err(|error| format!("Failed to read VoWiFi helper response: {error}"))?;
            serde_json::from_slice::<HelperControlResponse>(&response)
                .map_err(|error| format!("Invalid VoWiFi helper response: {error}"))
        })
        .await
        .map_err(|_| {
            format!(
                "VoWiFi helper request timed out after {}s",
                timeout.as_secs()
            )
        })?
    }

    pub async fn start(
        &self,
        mut config: VowifiTunnelLaunchConfig,
        requested_proxy: Option<VowifiUpstreamProxyRequest>,
    ) -> Result<VowifiTunnelStatus, String> {
        let _lifecycle = self.lifecycle.lock().await;
        if !self.helper_available() {
            return Err(format!(
                "VoWiFi SWu helper is unavailable at {}",
                self.helper_path.display()
            ));
        }

        {
            let runtime = self.runtime.lock().await;
            if let Some(pid) = runtime.pid {
                if pid_is_helper(pid, &self.helper_path) {
                    return Err(format!("An SWu tunnel is already running with PID {pid}"));
                }
            }
        }
        let existing = self.status().await;
        if existing.running {
            return Err(format!(
                "An SWu tunnel is already running for modem {}",
                existing.modem_id
            ));
        }

        let proxy = self.resolve_upstream_proxy(requested_proxy)?;
        config.proxy_enabled = proxy.enabled;
        config.proxy_address = proxy.address;
        config.proxy_username = proxy.username;
        config.proxy_password = proxy.password;

        // 应用持久化的 SIM AKA 后端选择（QMI 或 AT）。
        // prepare_vowifi_tunnel_config 已读取一次，这里兜底覆盖，
        // 确保任何路径下 helper 都使用当前保存的后端。
        if let Some(database) = &self.database {
            if let Ok(Some(backend)) =
                database.get_app_setting(DEVICE_BACKEND_SETTING_KEY)
            {
                if !backend.is_empty() {
                    config.device_backend = backend;
                }
            }
        }

        // Keep the previous atomic status file as the helper's bounded receive
        // history handoff. The new helper restores only successfully RP-ACKed
        // SMS entries and overwrites all live process/tunnel fields on startup.
        // status() independently validates the PID, so a stale file is never
        // treated as proof that the old helper is still running.
        match tokio::fs::remove_file(&self.control_path).await {
            Ok(()) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => {
                return Err(format!(
                    "Failed to clear stale VoWiFi control socket {}: {error}",
                    self.control_path.display()
                ));
            }
        }

        let restore_radio_after_stop = if let Some(conn) = &self.dbus_conn {
            let airplane = get_airplane_mode(conn).await.map_err(|error| {
                format!("Failed to read radio state before VoWiFi start: {error}")
            })?;
            if airplane.enabled {
                info!("Modem is already in airplane mode; skipping redundant radio change");
                false
            } else {
                set_airplane_mode(conn, true).await.map_err(|error| {
                    format!("Failed to enter airplane mode for VoWiFi: {error}")
                })?;
                tokio::time::sleep(Duration::from_millis(500)).await;
                true
            }
        } else {
            false
        };

        let mut command = Command::new(&self.helper_path);
        command
            .arg("--epdg")
            .arg(&config.epdg_ip)
            .arg("--epdg-fqdn")
            .arg(&config.epdg_fqdn)
            .arg("--serial")
            .arg(&config.serial_device)
            .arg("--device-backend")
            .arg(&config.device_backend)
            .arg("--qmi-device")
            .arg(&config.qmi_device)
            .arg("--local-ip")
            .arg(&config.local_ip)
            .arg("--access-interface")
            .arg(&config.access_interface)
            .arg("--modem-id")
            .arg(&config.modem_id)
            .arg("--mcc")
            .arg(&config.mcc)
            .arg("--mnc")
            .arg(&config.mnc)
            .arg("--live-cell-id")
            .arg(&config.live_cell_id)
            .arg("--carrier-overrides")
            .arg(carrier_overrides_path(&self.helper_path))
            .arg("--smsc")
            .arg(&config.smsc)
            .arg("--phone-number")
            .arg(&config.phone_number)
            .arg("--status-file")
            .arg(&self.status_path)
            .arg("--control-socket")
            .arg(&self.control_path)
            .env(
                "SIMADMIN_VOWIFI_PROXY_ENABLED",
                if config.proxy_enabled { "1" } else { "0" },
            )
            .env("SIMADMIN_VOWIFI_PROXY_ADDRESS", &config.proxy_address)
            .env("SIMADMIN_VOWIFI_PROXY_USERNAME", &config.proxy_username)
            .env("SIMADMIN_VOWIFI_PROXY_PASSWORD", &config.proxy_password)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(false);

        let mut child = match command.spawn() {
            Ok(child) => child,
            Err(error) => {
                let start_error = format!("Failed to start VoWiFi SWu helper: {error}");
                if restore_radio_after_stop {
                    if let Some(conn) = &self.dbus_conn {
                        if let Err(restore_error) = set_airplane_mode(conn, false).await {
                            return Err(format!(
                                "{start_error}; failed to restore radio: {restore_error}"
                            ));
                        }
                    }
                }
                return Err(start_error);
            }
        };
        let pid = child
            .id()
            .ok_or_else(|| "VoWiFi SWu helper started without a process id".to_string())?;
        let fallback = VowifiTunnelStatus::starting(&config, pid, true);

        if let Some(stdout) = child.stdout.take() {
            tokio::spawn(async move {
                let mut lines = BufReader::new(stdout).lines();
                while let Ok(Some(line)) = lines.next_line().await {
                    info!(pid, output = %line, "VoWiFi SWu helper");
                }
            });
        }
        if let Some(stderr) = child.stderr.take() {
            tokio::spawn(async move {
                let mut lines = BufReader::new(stderr).lines();
                while let Ok(Some(line)) = lines.next_line().await {
                    warn!(pid, output = %line, "VoWiFi SWu helper stderr");
                }
            });
        }

        {
            let mut runtime = self.runtime.lock().await;
            runtime.pid = Some(pid);
            runtime.fallback_status = Some(fallback.clone());
            runtime.restore_radio_after_stop = restore_radio_after_stop;
        }
        if let Some(database) = self.database.clone() {
            let status_path = self.status_path.clone();
            let helper_path = self.helper_path.clone();
            let notification_sender = self.notification_sender.clone();
            tokio::spawn(async move {
                while pid_is_helper(pid, &helper_path) {
                    if let Ok(bytes) = tokio::fs::read(&status_path).await {
                        if let Ok(status) = serde_json::from_slice::<VowifiTunnelStatus>(&bytes) {
                            sync_sms_to_database(&database, &status, notification_sender.clone());
                        }
                    }
                    tokio::time::sleep(Duration::from_secs(1)).await;
                }
            });
        }
        let runtime = Arc::clone(&self.runtime);
        let lifecycle = Arc::clone(&self.lifecycle);
        let dbus_conn = self.dbus_conn.clone();
        let airplane_mode_requested = self.airplane_mode_requested.clone();
        tokio::spawn(async move {
            let result = child.wait().await;
            match result {
                Ok(exit) if exit.success() => {
                    info!(pid, status = %exit, "VoWiFi SWu helper exited")
                }
                Ok(exit) => warn!(pid, status = %exit, "VoWiFi SWu helper exited with an error"),
                Err(error) => warn!(pid, error = %error, "Failed to wait for VoWiFi SWu helper"),
            }
            {
                let mut state = runtime.lock().await;
                if state.pid == Some(pid) {
                    state.pid = None;
                    state.fallback_status = None;
                }
            }
            let _lifecycle = lifecycle.lock().await;
            if let Err(error) = restore_radio_if_needed(
                &runtime,
                dbus_conn.as_deref(),
                airplane_mode_requested.as_deref(),
            )
            .await
            {
                warn!(pid, error = %error, "Failed to restore radio after VoWiFi helper exit");
            }
        });

        info!(
            pid,
            modem_id = %config.modem_id,
            epdg = %config.epdg_ip,
            interface = %config.access_interface,
            "VoWiFi SWu helper started"
        );
        Ok(fallback)
    }

    fn load_upstream_proxy(&self) -> Result<StoredVowifiUpstreamProxy, String> {
        let Some(database) = &self.database else {
            return Ok(StoredVowifiUpstreamProxy::default());
        };
        let Some(value) = database
            .get_app_setting(UPSTREAM_PROXY_SETTING_KEY)
            .map_err(|error| format!("Failed to read VoWiFi upstream proxy setting: {error}"))?
        else {
            return Ok(StoredVowifiUpstreamProxy::default());
        };
        serde_json::from_str(&value)
            .map_err(|error| format!("Invalid VoWiFi upstream proxy setting: {error}"))
    }

    fn resolve_upstream_proxy(
        &self,
        requested: Option<VowifiUpstreamProxyRequest>,
    ) -> Result<StoredVowifiUpstreamProxy, String> {
        let existing = self.load_upstream_proxy()?;
        let Some(requested) = requested else {
            validate_upstream_proxy(&existing)?;
            return Ok(existing);
        };
        let username = requested.username.trim().to_string();
        let password = match requested.password {
            Some(password) => password,
            None if username == existing.username => existing.password,
            None => String::new(),
        };
        let proxy = StoredVowifiUpstreamProxy {
            enabled: requested.enabled,
            address: requested.address.trim().to_string(),
            username,
            password,
        };
        validate_upstream_proxy(&proxy)?;
        if let Some(database) = &self.database {
            let value = serde_json::to_string(&proxy)
                .map_err(|error| format!("Failed to encode VoWiFi upstream proxy: {error}"))?;
            database
                .set_app_setting(UPSTREAM_PROXY_SETTING_KEY, &value)
                .map_err(|error| format!("Failed to persist VoWiFi upstream proxy: {error}"))?;
        }
        Ok(proxy)
    }

    pub async fn stop(&self) -> Result<VowifiTunnelStatus, String> {
        let _lifecycle = self.lifecycle.lock().await;
        let current = self.status().await;
        let runtime_pid = {
            let runtime = self.runtime.lock().await;
            runtime
                .pid
                .filter(|pid| pid_is_helper(*pid, &self.helper_path))
        };
        let target_pid = runtime_pid.or_else(|| {
            (current.running && current.pid != 0 && pid_is_helper(current.pid, &self.helper_path))
                .then_some(current.pid)
        });
        let Some(target_pid) = target_pid else {
            restore_radio_if_needed(
                &self.runtime,
                self.dbus_conn.as_deref(),
                self.airplane_mode_requested.as_deref(),
            )
            .await?;
            return Ok(current);
        };

        let result = unsafe { libc::kill(target_pid as libc::pid_t, libc::SIGTERM) };
        if result != 0 {
            let error = io::Error::last_os_error();
            if error.raw_os_error() != Some(libc::ESRCH) {
                return Err(format!(
                    "Failed to stop VoWiFi SWu helper PID {}: {error}",
                    target_pid
                ));
            }
        }
        info!(
            pid = target_pid,
            "Requested graceful VoWiFi SWu tunnel shutdown"
        );

        let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
        loop {
            if !pid_is_helper(target_pid, &self.helper_path) {
                restore_radio_if_needed(
                    &self.runtime,
                    self.dbus_conn.as_deref(),
                    self.airplane_mode_requested.as_deref(),
                )
                .await?;
                return Ok(self.status().await);
            }
            if tokio::time::Instant::now() >= deadline {
                break;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }

        // A helper can get stuck while unwinding the in-process IKE/IPsec
        // dataplane and ignore SIGTERM indefinitely.  Re-check the executable
        // immediately before escalating so a recycled PID can never be
        // targeted, then finish the stop operation instead of leaving the UI
        // permanently in the "stopping" state.
        if pid_is_helper(target_pid, &self.helper_path) {
            warn!(
                pid = target_pid,
                "VoWiFi SWu helper ignored SIGTERM; forcing shutdown"
            );
            let result = unsafe { libc::kill(target_pid as libc::pid_t, libc::SIGKILL) };
            if result != 0 {
                let error = io::Error::last_os_error();
                if error.raw_os_error() != Some(libc::ESRCH) {
                    return Err(format!(
                        "Failed to force-stop VoWiFi SWu helper PID {}: {error}",
                        target_pid
                    ));
                }
            }
        }

        let force_deadline = tokio::time::Instant::now() + Duration::from_secs(5);
        while pid_is_helper(target_pid, &self.helper_path) {
            if tokio::time::Instant::now() >= force_deadline {
                return Err(format!(
                    "Timed out waiting for force-stopped VoWiFi SWu helper PID {} to exit",
                    target_pid
                ));
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        restore_radio_if_needed(
            &self.runtime,
            self.dbus_conn.as_deref(),
            self.airplane_mode_requested.as_deref(),
        )
        .await?;
        Ok(self.status().await)
    }
}

fn ims_call_signaling_ready(status: &VowifiTunnelStatus) -> bool {
    status.running
        && status.established
        && status.ims_registered
        && status.ims_authenticated
        && status.ims_transport.eq_ignore_ascii_case("swu")
}

fn require_call_id(call_id: &str) -> Result<&str, String> {
    let call_id = call_id.trim();
    if call_id.is_empty() {
        Err("call_id is required".to_string())
    } else {
        Ok(call_id)
    }
}

fn normalize_audio_format<'a>(
    audio_format: &'a str,
    default_format: &'a str,
) -> Result<&'a str, String> {
    let audio_format = audio_format.trim();
    let audio_format = if audio_format.is_empty() {
        default_format
    } else {
        audio_format
    };
    match audio_format {
        "wav" | "pcm_s16le" => Ok(audio_format),
        _ => Err("audio_format must be wav or pcm_s16le".to_string()),
    }
}

fn helper_call_error(error: String, fallback: &str) -> String {
    if error.trim().is_empty() {
        fallback.to_string()
    } else {
        error
    }
}

async fn restore_radio_if_needed(
    runtime: &Mutex<RuntimeState>,
    dbus_conn: Option<&Connection>,
    airplane_mode_requested: Option<&std::sync::atomic::AtomicBool>,
) -> Result<(), String> {
    let should_restore = {
        let mut state = runtime.lock().await;
        std::mem::take(&mut state.restore_radio_after_stop)
    };
    if !should_restore {
        return Ok(());
    }
    if airplane_mode_requested
        .is_some_and(|requested| requested.load(std::sync::atomic::Ordering::SeqCst))
    {
        info!("Keeping modem in airplane mode after VoWiFi stop because it is user-requested");
        return Ok(());
    }
    let Some(conn) = dbus_conn else {
        return Ok(());
    };
    if let Err(error) = set_airplane_mode(conn, false).await {
        runtime.lock().await.restore_radio_after_stop = true;
        return Err(format!(
            "Failed to restore radio after VoWiFi stop: {error}"
        ));
    }
    info!("Restored modem radio after VoWiFi stop");
    Ok(())
}

fn validate_upstream_proxy(proxy: &StoredVowifiUpstreamProxy) -> Result<(), String> {
    if proxy.address.len() > 512 || proxy.username.len() > 255 || proxy.password.len() > 255 {
        return Err("VoWiFi upstream proxy fields are too long".to_string());
    }
    if !proxy.enabled {
        return Ok(());
    }
    let (host, port) = proxy
        .address
        .rsplit_once(':')
        .ok_or_else(|| "VoWiFi upstream proxy address must use host:port".to_string())?;
    let host = host.trim().trim_start_matches('[').trim_end_matches(']');
    if host.is_empty() || host.chars().any(char::is_whitespace) {
        return Err("VoWiFi upstream proxy host is invalid".to_string());
    }
    let port = port
        .parse::<u16>()
        .map_err(|_| "VoWiFi upstream proxy port must be between 1 and 65535".to_string())?;
    if port == 0 {
        return Err("VoWiFi upstream proxy port must be between 1 and 65535".to_string());
    }
    if !proxy.username.is_empty() && proxy.password.is_empty() {
        return Err(
            "VoWiFi upstream proxy password is required when a username is set".to_string(),
        );
    }
    Ok(())
}

pub fn helper_is_available() -> bool {
    helper_is_available_at(&default_helper_path())
}

fn helper_is_available_at(path: &Path) -> bool {
    std::fs::metadata(path)
        .is_ok_and(|metadata| metadata.is_file() && metadata.permissions().mode() & 0o111 != 0)
}

fn default_helper_path() -> PathBuf {
    if let Some(path) = std::env::var_os("SIMADMIN_VOWIFI_HELPER") {
        return PathBuf::from(path);
    }
    executable_directory().join(HELPER_FILENAME)
}

fn default_status_path() -> PathBuf {
    if let Some(path) = std::env::var_os("SIMADMIN_VOWIFI_STATUS") {
        return PathBuf::from(path);
    }
    executable_directory().join(STATUS_FILENAME)
}

fn default_control_path() -> PathBuf {
    if let Some(path) = std::env::var_os("SIMADMIN_VOWIFI_CONTROL") {
        return PathBuf::from(path);
    }
    executable_directory().join(CONTROL_FILENAME)
}

fn carrier_overrides_path(helper_path: &Path) -> PathBuf {
    if let Some(path) = std::env::var_os("SIMADMIN_VOWIFI_CARRIER_OVERRIDES") {
        return PathBuf::from(path);
    }
    helper_path
        .parent()
        .unwrap_or_else(|| Path::new("."))
        .join(CARRIER_OVERRIDES_FILENAME)
}

fn sync_sms_to_database(
    database: &Database,
    status: &VowifiTunnelStatus,
    notification_sender: Arc<StdMutex<Option<Arc<NotificationSender>>>>,
) {
    if !status.sms_last_tx_message_id.trim().is_empty()
        && !status.sms_last_tx_to.trim().is_empty()
        && !status.sms_last_tx_text.trim().is_empty()
    {
        let marker = format!("vowifi:tx:{}", status.sms_last_tx_message_id.trim());
        let delivery_status = if status.sms_tx_path_verified {
            "sent"
        } else if status.sms_last_tx_rp_state == "failed"
            || status.sms_last_tx_rp_state == "submit_failed"
        {
            "failed"
        } else {
            "pending"
        };
        let timestamp = first_non_empty(&status.sms_last_tx_at, &status.updated_at);
        if let Err(error) = database.upsert_sms_by_pdu(
            "outgoing",
            &status.sms_last_tx_to,
            &status.sms_last_tx_text,
            timestamp,
            delivery_status,
            &marker,
        ) {
            warn!(error = %error, marker = %marker, "Failed to persist VoWiFi outgoing SMS");
        }
    }

    for message in &status.sms_received_messages {
        persist_received_sms(database, message, &status.updated_at, &notification_sender);
    }

    // Compatibility with helpers that predate sms_received_messages. State
    // and SIP-code checks prevent a stale `verified` bit from persisting a
    // newer pending or failed delivery.
    if status.sms_received_messages.is_empty()
        && status.sms_rx_path_verified
        && status.sms_last_rx_state == "rp_acked"
        && (200..300).contains(&status.sms_last_rx_rp_ack_sip_code)
        && !status.sms_last_rx_id.trim().is_empty()
        && !status.sms_last_rx_from.trim().is_empty()
    {
        persist_received_sms(
            database,
            &VowifiReceivedSms {
                id: status.sms_last_rx_id.clone(),
                from: status.sms_last_rx_from.clone(),
                text: status.sms_last_rx_text.clone(),
                received_at: status.sms_last_rx_at.clone(),
                rp_mr: status.sms_last_rx_rp_mr,
                rp_ack_sip_code: status.sms_last_rx_rp_ack_sip_code,
            },
            &status.updated_at,
            &notification_sender,
        );
    }
}

fn persist_received_sms(
    database: &Database,
    message: &VowifiReceivedSms,
    fallback_time: &str,
    notification_sender: &Arc<StdMutex<Option<Arc<NotificationSender>>>>,
) {
    if message.id.trim().is_empty()
        || message.from.trim().is_empty()
        || !(200..300).contains(&message.rp_ack_sip_code)
    {
        return;
    }
    let marker = format!("vowifi:rx:{}", message.id.trim());
    let timestamp = first_non_empty(&message.received_at, fallback_time);
    let existed = database.sms_exists_by_pdu(&marker).unwrap_or(false);
    match database.upsert_sms_by_pdu(
        "incoming",
        &message.from,
        &message.text,
        timestamp,
        "received",
        &marker,
    ) {
        Ok(Some(id)) if !existed => {
            let sms = SmsMessage {
                id,
                direction: "incoming".to_string(),
                phone_number: message.from.clone(),
                content: message.text.clone(),
                timestamp: timestamp.to_string(),
                status: "received".to_string(),
                pdu: Some(marker),
            };
            if let Some(sender) = notification_sender.lock().unwrap().clone() {
                tokio::spawn(async move {
                    let _ = sender.forward_sms(&sms).await;
                });
            }
        }
        Ok(_) => {}
        Err(error) => {
            warn!(error = %error, marker = %marker, "Failed to persist VoWiFi incoming SMS")
        }
    }
}

fn executable_directory() -> PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|path| path.parent().map(Path::to_path_buf))
        .unwrap_or_else(|| PathBuf::from("."))
}

fn pid_is_helper(pid: u32, helper_path: &Path) -> bool {
    if pid == 0 {
        return false;
    }
    let process_exe = std::fs::read_link(format!("/proc/{pid}/exe"));
    let expected = std::fs::canonicalize(helper_path);
    match (process_exe, expected) {
        (Ok(process_exe), Ok(expected)) => helper_executable_matches(&process_exe, &expected),
        _ => false,
    }
}

fn helper_executable_matches(process_exe: &Path, expected: &Path) -> bool {
    if process_exe == expected {
        return true;
    }

    // Linux appends this marker to /proc/<pid>/exe after an atomic binary
    // replacement. The process is still our helper and must remain stoppable;
    // treating it as gone can otherwise permit a second helper to start.
    process_exe
        .to_str()
        .and_then(|path| path.strip_suffix(" (deleted)"))
        .is_some_and(|path| Path::new(path) == expected)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn helper_executable_accepts_live_and_atomically_replaced_paths() {
        let expected = Path::new("/opt/simadmin/simadmin-vowifi-helper");

        assert!(helper_executable_matches(expected, expected));
        assert!(helper_executable_matches(
            Path::new("/opt/simadmin/simadmin-vowifi-helper (deleted)"),
            expected,
        ));
        assert!(!helper_executable_matches(
            Path::new("/opt/simadmin/other-helper (deleted)"),
            expected,
        ));
    }

    #[test]
    fn stopped_status_reports_helper_capability() {
        let status = VowifiTunnelStatus::stopped(true);
        assert_eq!(status.stage, "stopped");
        assert!(status.helper_available);
        assert!(!status.running);
        assert!(!status.established);
    }

    #[test]
    fn missing_helper_is_not_available() {
        assert!(!helper_is_available_at(Path::new(
            "/tmp/simadmin-vowifi-helper-does-not-exist"
        )));
    }

    #[test]
    fn carrier_overrides_are_resolved_next_to_helper() {
        assert_eq!(
            carrier_overrides_path(Path::new("/opt/simadmin/simadmin-vowifi-helper")),
            PathBuf::from("/opt/simadmin/data/carrier_overrides.json")
        );
    }

    #[test]
    fn helper_status_accepts_omitted_empty_fields() {
        let status: VowifiTunnelStatus = serde_json::from_str(
            r#"{"stage":"established","running":true,"established":true,"pid":42,"tunnel_ipv6":"2001:db8::1"}"#,
        )
        .expect("helper status should use defaults for omitted empty fields");
        assert_eq!(status.tunnel_ipv4, "");
        assert_eq!(status.tunnel_ipv6, "2001:db8::1");
        assert!(status.pcscf_v4.is_empty());
    }

    #[test]
    fn established_swu_is_not_reported_as_enabled_vowifi() {
        let status = VowifiTunnelStatus {
            stage: "established".to_string(),
            running: true,
            established: true,
            epdg_ip: "192.0.2.10".to_string(),
            tunnel_ipv6: "2001:db8::10".to_string(),
            ..Default::default()
        };
        let verification = status.verification();
        assert!(!verification.enabled);
        assert_eq!(verification.verdict, "tunnel_only");
        assert_eq!(verification.swu.state, "passed");
        assert_ne!(verification.ims.state, "passed");
    }

    #[test]
    fn location_rejection_is_reported_as_failure() {
        let status = VowifiTunnelStatus {
            stage: "established".to_string(),
            running: true,
            established: true,
            ims_registration_state: "register_location_rejected".to_string(),
            error: "403 Forbidden - Service not allowed in this location".to_string(),
            ..Default::default()
        };
        let verification = status.verification();
        assert!(!verification.enabled);
        assert_eq!(verification.verdict, "failed");
        assert_eq!(verification.ims.state, "failed");
        assert!(verification.summary.contains("SOCKS5 UDP"));
    }

    #[test]
    fn upstream_proxy_validation_requires_udp_proxy_endpoint_and_credentials() {
        assert!(validate_upstream_proxy(&StoredVowifiUpstreamProxy {
            enabled: true,
            address: "uk-proxy.example:1080".to_string(),
            ..Default::default()
        })
        .is_ok());
        assert!(validate_upstream_proxy(&StoredVowifiUpstreamProxy {
            enabled: true,
            address: "uk-proxy.example".to_string(),
            ..Default::default()
        })
        .is_err());
        assert!(validate_upstream_proxy(&StoredVowifiUpstreamProxy {
            enabled: true,
            address: "uk-proxy.example:1080".to_string(),
            username: "subscriber".to_string(),
            ..Default::default()
        })
        .is_err());
    }

    #[test]
    fn terminal_ims_timeout_is_reported_as_failure() {
        let status = VowifiTunnelStatus {
            stage: "established".to_string(),
            running: true,
            established: true,
            pcscf_probe_state: "failed".to_string(),
            ims_registration_state: "register_failed".to_string(),
            error: "register response timeout".to_string(),
            ..Default::default()
        };
        let verification = status.verification();
        assert_eq!(verification.verdict, "failed");
        assert_eq!(verification.ims.state, "failed");
    }

    #[test]
    fn vowifi_accepts_authenticated_ims_over_swu_without_separate_ipsec() {
        let mut status = VowifiTunnelStatus {
            stage: "established".to_string(),
            running: true,
            established: true,
            ims_registered: true,
            ims_transport: "swu".to_string(),
            ..Default::default()
        };
        assert!(!status.verification().enabled);

        status.ims_authenticated = true;
        let verification = status.verification();
        assert!(verification.enabled);
        assert_eq!(verification.verdict, "enabled");
        assert_eq!(verification.ims.state, "passed");
        assert!(verification.ims.evidence.contains("未协商独立的"));
        assert!(!verification.ims.evidence.contains("鉴权注册未完成"));

        status.ims_ipsec_established = true;
        status.ims_security_mode = "ipsec3gpp".to_string();
        let verification = status.verification();
        assert!(verification.enabled);
        assert_eq!(verification.verdict, "enabled");
        assert_eq!(verification.ims.state, "passed");
        assert!(verification.ims.evidence.contains("已生效"));
    }

    #[test]
    fn call_signaling_accepts_plain_authenticated_ims_over_swu() {
        let status = VowifiTunnelStatus {
            running: true,
            established: true,
            ims_registered: true,
            ims_authenticated: true,
            ims_transport: "swu".to_string(),
            ims_security_mode: "plain".to_string(),
            ims_ipsec_established: false,
            ..Default::default()
        };
        assert!(ims_call_signaling_ready(&status));

        let mut wrong_transport = status.clone();
        wrong_transport.ims_transport = "cellular".to_string();
        assert!(!ims_call_signaling_ready(&wrong_transport));
    }

    #[test]
    fn helper_call_status_preserves_rtp_transport_status() {
        let response: HelperControlResponse = serde_json::from_str(
            r#"{
                "ok": true,
                "call": {
                    "call_id": "call-1",
                    "phone_number": "+18005551212",
                    "state": "signaling_established",
                    "sip_code": 200,
                    "media_ready": true,
                    "media_supported": true,
                    "media_mode": "rtp_transport_receiving",
                    "media_codec": "AMR-WB",
                    "media_direction": "sendrecv",
                    "audio_ready": false,
                    "audio_mode": "no_local_audio_io",
                    "rtp_packets_received": 12,
                    "rtp_bytes_received": 640,
                    "rtcp_packets_received": 2,
                    "rtcp_bytes_received": 128
                }
            }"#,
        )
        .expect("call control response should deserialize");
        let call = response.call.expect("call status");
        assert_eq!(call.call_id, "call-1");
        assert_eq!(call.sip_code, 200);
        assert!(call.media_ready);
        assert!(call.media_supported);
        assert_eq!(call.media_mode, "rtp_transport_receiving");
        assert_eq!(call.media_codec, "AMR-WB");
        assert_eq!(call.media_direction, "sendrecv");
        assert!(!call.audio_ready);
        assert_eq!(call.audio_mode, "no_local_audio_io");
        assert_eq!(call.rtp_packets_received, 12);
        assert_eq!(call.rtp_bytes_received, 640);
        assert_eq!(call.rtcp_packets_received, 2);
        assert_eq!(call.rtcp_bytes_received, 128);
    }

    #[test]
    fn helper_audio_result_preserves_g711_file_io_statistics() {
        let response: HelperControlResponse = serde_json::from_str(
            r#"{
                "ok": true,
                "audio": {
                    "call_id": "call-audio-1",
                    "format": "wav",
                    "content_type": "audio/wav",
                    "sample_rate": 8000,
                    "channels": 1,
                    "bits_per_sample": 16,
                    "data_base64": "UklGRg==",
                    "stats": {
                        "call_id": "call-audio-1",
                        "codec": "PCMA",
                        "sample_rate": 8000,
                        "frame_duration_ms": 20,
                        "playback_active": true,
                        "rtp_packets_sent": 7,
                        "rtp_bytes_sent": 1204,
                        "pcm_samples_sent": 1120,
                        "audio_packets_decoded": 9,
                        "audio_samples_recorded": 1440,
                        "recording_bytes": 2880,
                        "recording_duration_ms": 180,
                        "recording_truncated": false,
                        "rtp_packets_lost": 2,
                        "rtp_packets_out_of_order": 1,
                        "last_playback_at": "2026-08-08T09:00:00Z",
                        "last_playback_error": ""
                    }
                }
            }"#,
        )
        .expect("audio control response should deserialize");
        let audio = response.audio.expect("audio result");
        assert_eq!(audio.call_id, "call-audio-1");
        assert_eq!(audio.format, "wav");
        assert_eq!(audio.sample_rate, 8000);
        assert_eq!(audio.data_base64, "UklGRg==");
        assert_eq!(audio.stats.codec, "PCMA");
        assert_eq!(audio.stats.rtp_packets_sent, 7);
        assert_eq!(audio.stats.recording_duration_ms, 180);
        assert_eq!(audio.stats.rtp_packets_lost, 2);
        assert_eq!(audio.stats.rtp_packets_out_of_order, 1);
    }

    #[test]
    fn helper_audio_request_uses_the_control_schema() {
        let payload = serde_json::to_value(HelperControlRequest {
            action: "play_audio",
            call_id: "call-1",
            phone_number: "",
            content: "",
            encoding: "",
            timeout_seconds: 0,
            after_id: "",
            audio_format: "pcm_s16le",
            audio_base64: "AAA=",
        })
        .expect("audio request should serialize");
        assert_eq!(payload["action"], "play_audio");
        assert_eq!(payload["call_id"], "call-1");
        assert_eq!(payload["audio_format"], "pcm_s16le");
        assert_eq!(payload["audio_base64"], "AAA=");
        assert!(payload.get("phone_number").is_none());
    }

    #[test]
    fn audio_format_validation_is_strict_and_defaults_recordings_to_wav() {
        assert_eq!(normalize_audio_format("", "wav").unwrap(), "wav");
        assert_eq!(
            normalize_audio_format("pcm_s16le", "wav").unwrap(),
            "pcm_s16le"
        );
        assert!(normalize_audio_format("mp3", "wav").is_err());
        assert!(require_call_id("   ").is_err());
        assert_eq!(require_call_id(" call-1 ").unwrap(), "call-1");
    }

    #[test]
    fn sms_checks_require_swu_path_evidence() {
        let mut status = VowifiTunnelStatus::default();
        assert_eq!(status.verification().sms_send.state, "blocked");
        status.sms_over_ims_ready = true;
        assert_eq!(status.verification().sms_send.state, "not_tested");
        status.sms_last_tx_rp_state = "submit_failed".to_string();
        status.sms_last_tx_error = "P-CSCF TCP dial timed out".to_string();
        let failed = status.verification().sms_send;
        assert_eq!(failed.state, "failed");
        assert_eq!(failed.evidence, "P-CSCF TCP dial timed out");
        status.sms_last_tx_rp_state.clear();
        status.sms_last_tx_error.clear();
        status.sms_tx_path_verified = true;
        assert_eq!(status.verification().sms_send.state, "passed");
    }

    #[test]
    fn received_sms_history_is_persisted_without_polling_loss_or_duplicates() {
        let database = Database::new(PathBuf::from(":memory:")).unwrap();
        let status = VowifiTunnelStatus {
            updated_at: "2026-08-08T10:00:02Z".to_string(),
            sms_received_messages: vec![
                VowifiReceivedSms {
                    id: "call-a:11".to_string(),
                    from: "+8615556250521".to_string(),
                    text: "first".to_string(),
                    received_at: "2026-08-08T10:00:00Z".to_string(),
                    rp_mr: 0x11,
                    rp_ack_sip_code: 200,
                },
                VowifiReceivedSms {
                    id: "call-b:12".to_string(),
                    from: "+8615556250521".to_string(),
                    text: "second".to_string(),
                    received_at: "2026-08-08T10:00:01Z".to_string(),
                    rp_mr: 0x12,
                    rp_ack_sip_code: 202,
                },
            ],
            ..Default::default()
        };

        sync_sms_to_database(&database, &status, Arc::new(StdMutex::new(None)));
        sync_sms_to_database(&database, &status, Arc::new(StdMutex::new(None)));

        let messages = database.get_sms_messages(10, 0, Some("incoming")).unwrap();
        assert_eq!(messages.len(), 2);
        assert_eq!(messages[0].content, "second");
        assert_eq!(messages[1].content, "first");
        assert!(messages.iter().all(|message| message.status == "received"));
    }

    #[tokio::test]
    async fn rust_status_api_replay_is_sqlite_idempotent_across_restart() {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let temp_dir = std::env::temp_dir().join(format!(
            "simadmin-vowifi-rx-restart-{}-{unique}",
            std::process::id()
        ));
        std::fs::create_dir_all(&temp_dir).unwrap();
        let status_path = temp_dir.join("vowifi-tunnel.json");
        let control_path = temp_dir.join("vowifi-control.sock");
        let database_path = temp_dir.join("data.db");
        let helper_path = std::env::current_exe().unwrap();
        let history = VowifiTunnelStatus {
            updated_at: "2026-08-08T10:00:01Z".to_string(),
            sms_received_messages: vec![VowifiReceivedSms {
                id: "fake-ims-call:34".to_string(),
                from: "10086".to_string(),
                text: "hello".to_string(),
                received_at: "2026-08-08T10:00:00Z".to_string(),
                rp_mr: 0x34,
                rp_ack_sip_code: 200,
            }],
            ..Default::default()
        };
        std::fs::write(&status_path, serde_json::to_vec(&history).unwrap()).unwrap();

        let database = Arc::new(Database::new(database_path.clone()).unwrap());
        let manager = VowifiTunnelManager::new_with_database(
            helper_path.clone(),
            status_path.clone(),
            control_path.clone(),
            Some(Arc::clone(&database)),
            None,
            None,
        );
        let api_status = manager.status().await;
        assert_eq!(api_status.sms_received_messages.len(), 1);
        assert_eq!(
            database
                .get_sms_messages(10, 0, Some("incoming"))
                .unwrap()
                .len(),
            1
        );
        drop(manager);
        drop(database);

        // Reopening both the Rust manager and SQLite database replays the same
        // helper history, as happens after a backend restart. The protocol
        // marker must still identify the original row.
        let reopened_database = Arc::new(Database::new(database_path).unwrap());
        let reopened_manager = VowifiTunnelManager::new_with_database(
            helper_path,
            status_path,
            control_path,
            Some(Arc::clone(&reopened_database)),
            None,
            None,
        );
        let _ = reopened_manager.status().await;
        let messages = reopened_database
            .get_sms_messages(10, 0, Some("incoming"))
            .unwrap();
        assert_eq!(messages.len(), 1);
        assert_eq!(
            messages[0].pdu.as_deref(),
            Some("vowifi:rx:fake-ims-call:34")
        );

        drop(reopened_manager);
        drop(reopened_database);
        std::fs::remove_dir_all(temp_dir).unwrap();
    }

    #[test]
    fn stale_verified_bit_does_not_persist_pending_inbound_sms() {
        let database = Database::new(PathBuf::from(":memory:")).unwrap();
        let status = VowifiTunnelStatus {
            sms_rx_path_verified: true,
            sms_last_rx_id: "pending-call:21".to_string(),
            sms_last_rx_from: "+8615556250521".to_string(),
            sms_last_rx_text: "must not persist".to_string(),
            sms_last_rx_state: "rp_ack_pending".to_string(),
            sms_last_rx_rp_ack_sip_code: 200,
            ..Default::default()
        };

        sync_sms_to_database(&database, &status, Arc::new(StdMutex::new(None)));
        assert!(database
            .get_sms_messages(10, 0, Some("incoming"))
            .unwrap()
            .is_empty());
    }

    #[test]
    fn invalid_or_unacknowledged_receive_history_is_not_persisted() {
        let database = Database::new(PathBuf::from(":memory:")).unwrap();
        let status = VowifiTunnelStatus {
            sms_received_messages: vec![
                VowifiReceivedSms {
                    id: "failed:34".to_string(),
                    from: "10086".to_string(),
                    text: "failed".to_string(),
                    rp_ack_sip_code: 500,
                    ..Default::default()
                },
                VowifiReceivedSms {
                    id: "missing-sender:35".to_string(),
                    text: "invalid".to_string(),
                    rp_ack_sip_code: 200,
                    ..Default::default()
                },
            ],
            ..Default::default()
        };

        sync_sms_to_database(&database, &status, Arc::new(StdMutex::new(None)));
        assert!(database
            .get_sms_messages(10, 0, Some("incoming"))
            .unwrap()
            .is_empty());
    }
}
