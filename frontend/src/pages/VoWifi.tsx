import { useCallback, useEffect, useState, type ReactNode } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  FormControlLabel,
  Paper,
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import {
  CheckCircle as PassedIcon,
  Error as FailedIcon,
  HelpOutline as UnknownIcon,
  Inbox as ReceiveIcon,
  Pending as PendingIcon,
  PlayArrow as StartIcon,
  Refresh as RefreshIcon,
  Send as SendIcon,
  SettingsInputAntenna as RegisterIcon,
  Stop as StopIcon,
} from '@mui/icons-material'
import {
  api,
  type ImsStatusResponse,
  type VowifiSmsPathVerificationResult,
  type VowifiTunnelStatus,
  type VowifiVerificationCheck,
  type VowifiVerificationStatus,
} from '../api/current'
import { PageHeader } from '../components/PageFrame'
import { useModems } from '../contexts/ModemContext'

function BoolChip({ value, yes, no }: { value?: boolean | null; yes: string; no: string }) {
  if (value === undefined || value === null) return <Chip size="small" variant="outlined" label="未知" />
  return <Chip size="small" variant="outlined" color={value ? 'success' : 'default'} label={value ? yes : no} />
}

function StatusField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box sx={{ minWidth: 0, py: 1.5, borderBottom: 1, borderColor: 'divider' }}>
      <Typography variant="caption" color="text.secondary" display="block" sx={{ mb: 0.75 }}>{label}</Typography>
      {children}
    </Box>
  )
}

function MonoValue({ children }: { children: ReactNode }) {
  return <Typography variant="body2" sx={{ fontFamily: '"Geist Mono", monospace', wordBreak: 'break-all' }}>{children}</Typography>
}

function transportLabel(value: ImsStatusResponse['transport']) {
  if (value === 'wifi') return 'Wi-Fi / ePDG'
  if (value === 'cellular') return '蜂窝网络'
  if (value === 'none') return '未建立'
  return '未知'
}

function tunnelStageLabel(stage?: VowifiTunnelStatus['stage']) {
  if (stage === 'starting') return '准备中'
  if (stage === 'authenticating') return 'IKEv2 / EAP-AKA 认证中'
  if (stage === 'established') return 'Child SA 已建立'
  if (stage === 'stopping') return '正在停止'
  if (stage === 'failed') return '建立失败'
  return '未运行'
}

function cellIDSourceLabel(source?: string) {
  if (source === 'qmi') return '实时 QMI'
  if (source === 'carrier_default') return '运营商回退'
  if (source === 'disabled') return '已禁用'
  if (source === 'placeholder') return '全零占位'
  return '未知来源'
}

function verificationStateLabel(state: VowifiVerificationCheck['state']) {
  if (state === 'passed') return '通过'
  if (state === 'running') return '验证中'
  if (state === 'incomplete') return '未完成'
  if (state === 'failed') return '失败'
  if (state === 'not_tested') return '未验证'
  if (state === 'blocked') return '不可验证'
  if (state === 'unavailable') return '不可用'
  return '未开始'
}

function verificationStateColor(state: VowifiVerificationCheck['state']): 'success' | 'error' | 'warning' | 'default' {
  if (state === 'passed') return 'success'
  if (state === 'failed') return 'error'
  if (state === 'running' || state === 'incomplete') return 'warning'
  return 'default'
}

function VerificationRow({ label, check }: { label: string; check: VowifiVerificationCheck }) {
  const StateIcon = check.state === 'passed'
    ? PassedIcon
    : check.state === 'failed'
      ? FailedIcon
      : check.state === 'running' || check.state === 'incomplete'
        ? PendingIcon
        : UnknownIcon
  const iconColor = check.state === 'passed' ? 'success.main' : check.state === 'failed' ? 'error.main' : 'text.secondary'
  return (
    <Box sx={{ display: 'grid', gridTemplateColumns: '32px minmax(0, 1fr) auto', alignItems: 'start', gap: 1.25, py: 1.5, borderBottom: 1, borderColor: 'divider' }}>
      <StateIcon sx={{ color: iconColor, mt: 0.25 }} />
      <Box minWidth={0}>
        <Typography variant="body2" fontWeight={700}>{label}</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.35, wordBreak: 'break-word' }}>{check.evidence}</Typography>
      </Box>
      <Chip size="small" variant="outlined" color={verificationStateColor(check.state)} label={verificationStateLabel(check.state)} />
    </Box>
  )
}

export default function VoWifi() {
  const { selectedModem } = useModems()
  const [status, setStatus] = useState<ImsStatusResponse | null>(null)
  const [tunnel, setTunnel] = useState<VowifiTunnelStatus | null>(null)
  const [verification, setVerification] = useState<VowifiVerificationStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [registering, setRegistering] = useState(false)
  const [tunnelBusy, setTunnelBusy] = useState(false)
  const [smsVerifying, setSmsVerifying] = useState<'send' | 'receive' | null>(null)
  const [smsResult, setSmsResult] = useState<Record<'send' | 'receive', VowifiSmsPathVerificationResult | null>>({ send: null, receive: null })
  const [smsPhoneNumber, setSmsPhoneNumber] = useState('')
  const [smsContent, setSmsContent] = useState('SimAdmin VoWiFi test')
  const [proxyEnabled, setProxyEnabled] = useState(false)
  const [proxyAddress, setProxyAddress] = useState('')
  const [proxyUsername, setProxyUsername] = useState('')
  const [proxyPassword, setProxyPassword] = useState('')
  const [proxyHasPassword, setProxyHasPassword] = useState(false)
  const [proxyLoaded, setProxyLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [tunnelResponse, verificationResponse] = await Promise.all([
        api.getVowifiTunnelStatus(),
        api.getVowifiVerificationStatus(),
      ])
      const nextTunnel = tunnelResponse.data ?? null
      setTunnel(nextTunnel)
      if (nextTunnel?.phone_number) setSmsPhoneNumber((current) => current || nextTunnel.phone_number)
      setVerification(verificationResponse.data ?? null)
      if (!nextTunnel?.running) {
        const imsResponse = await api.getImsStatus()
        setStatus(imsResponse.data ?? null)
      }
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '读取 IMS / VoWiFi 状态失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load, selectedModem?.id])

  useEffect(() => {
    if (!tunnel || proxyLoaded) return
    setProxyEnabled(Boolean(tunnel.proxy_enabled))
    setProxyAddress(tunnel.proxy_address || '')
    setProxyUsername(tunnel.proxy_username || '')
    setProxyHasPassword(Boolean(tunnel.proxy_has_password))
    setProxyLoaded(true)
  }, [proxyLoaded, tunnel])

  useEffect(() => {
    if (!tunnel?.running) return
    let cancelled = false
    const poll = async () => {
      try {
        const [response, verificationResponse] = await Promise.all([
          api.getVowifiTunnelStatus(),
          api.getVowifiVerificationStatus(),
        ])
        if (!cancelled) {
          setTunnel(response.data ?? null)
          if (response.data?.phone_number) setSmsPhoneNumber((current) => current || response.data?.phone_number || '')
          setVerification(verificationResponse.data ?? null)
          setError(null)
        }
      } catch (cause) {
        if (!cancelled) setError(cause instanceof Error ? cause.message : '读取 SWu 隧道状态失败')
      }
    }
    const timer = window.setInterval(() => void poll(), 1000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [tunnel?.running])

  const requestRegistration = async () => {
    setRegistering(true)
    try {
      const response = await api.startImsRegistration()
      setStatus(response.data ?? null)
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'IMS 驻网请求失败')
    } finally {
      setRegistering(false)
    }
  }

  const startTunnel = async () => {
    if (proxyEnabled && !proxyAddress.trim()) {
      setError('启用 VoWiFi 上游代理时必须填写 SOCKS5 地址')
      return
    }
    setTunnelBusy(true)
    try {
      const response = await api.startVowifiTunnel({
        enabled: proxyEnabled,
        address: proxyAddress.trim(),
        username: proxyUsername.trim(),
        password: proxyPassword || undefined,
      })
      setTunnel(response.data ?? null)
      if (proxyPassword) {
        setProxyHasPassword(true)
        setProxyPassword('')
      }
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'ePDG SWu 隧道建立失败')
    } finally {
      setTunnelBusy(false)
    }
  }

  const stopTunnel = async () => {
    setTunnelBusy(true)
    try {
      const response = await api.stopVowifiTunnel()
      setTunnel(response.data ?? null)
      const verificationResponse = await api.getVowifiVerificationStatus()
      setVerification(verificationResponse.data ?? null)
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'ePDG SWu 隧道停止失败')
    } finally {
      setTunnelBusy(false)
    }
  }

  const verifySmsPath = async (direction: 'send' | 'receive') => {
    if (direction === 'send' && (!smsPhoneNumber.trim() || !smsContent.trim())) {
      setError('请输入接收号码和短信内容')
      return
    }
    setSmsVerifying(direction)
    try {
      const response = await api.verifyVowifiSmsPath(direction === 'send'
        ? {
            direction,
            phone_number: smsPhoneNumber.trim(),
            content: smsContent,
            encoding: 'auto',
            timeout_seconds: 30,
          }
	        : {
	            direction,
	            timeout_seconds: 60,
	            // A pending/failed SMS already updates sms_last_rx_id but is not
	            // a successful receive cursor. Baseline only from RP-ACKed history.
	            after_id: tunnel?.sms_received_messages.at(-1)?.id || '',
	          })
      const [tunnelResponse, verificationResponse] = await Promise.all([
        api.getVowifiTunnelStatus(),
        api.getVowifiVerificationStatus(),
      ])
      setSmsResult((current) => ({ ...current, [direction]: response.data ?? null }))
      setTunnel(tunnelResponse.data ?? null)
      setVerification(verificationResponse.data ?? null)
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'VoWiFi 短信路径验证失败')
    } finally {
      setSmsVerifying(null)
    }
  }

  const severity = verification?.enabled
    ? 'success'
    : verification?.verdict === 'failed'
      ? 'error'
      : verification?.verdict === 'tunnel_only' || verification?.verdict === 'connecting'
        ? 'warning'
        : 'info'

  const summaryTitle = verification?.enabled ? 'VoWiFi 已启用成功' : 'VoWiFi 未启用成功'
  const summary = verification?.verdict === 'tunnel_only'
    ? '当前只证明 ePDG/SWu 隧道已建立；尚无已鉴权 IMS 注册证据，不能认定 VoWiFi 成功。'
    : verification?.verdict === 'connecting'
      ? `正在建立 ePDG/SWu 隧道：${tunnelStageLabel(tunnel?.stage)}`
      : verification?.verdict === 'failed'
        ? `VoWiFi 验证失败：${tunnel?.error || verification.summary}`
        : verification?.enabled
          ? verification.summary
          : '当前没有经过 SWu 的 IMS 注册，VoWiFi 未启用。'
  const locationRejected = tunnel?.ims_registration_state === 'register_location_rejected'
    || tunnel?.error?.toLowerCase().includes('service not allowed in this location')

  return (
    <Box>
      <PageHeader
        title="VoWiFi"
        meta={selectedModem && <Typography variant="body2" color="text.secondary">模块 {selectedModem.path_id} · {selectedModem.network_interface || selectedModem.primary_port}</Typography>}
        actions={(
          <>
            <Button variant="outlined" startIcon={<RefreshIcon />} onClick={() => void load()} disabled={loading || registering || tunnelBusy}>刷新</Button>
            <Button
              variant="contained"
              startIcon={registering ? <CircularProgress color="inherit" size={18} /> : <RegisterIcon />}
              onClick={() => void requestRegistration()}
              disabled={loading || registering || tunnelBusy || tunnel?.running === true || status?.registered === true}
            >
              {status?.registered ? '已驻网' : '请求 IMS 驻网'}
            </Button>
          </>
        )}
      />

      {error && <Alert severity="error" onClose={() => setError(null)} sx={{ mb: 2 }}>{error}</Alert>}
      {loading && !status && !tunnel ? (
        <Box display="flex" justifyContent="center" py={8}><CircularProgress /></Box>
      ) : (
        <>
          <Alert severity={severity} sx={{ mb: 3 }}>
            <Typography variant="subtitle1" fontWeight={800}>{summaryTitle}</Typography>
            <Typography variant="body2" sx={{ mt: 0.25 }}>{summary}</Typography>
          </Alert>

          {verification && (
            <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, borderRadius: 1, mb: 2 }}>
              <Box display="flex" alignItems="center" justifyContent="space-between" gap={2}>
                <Typography variant="h6" component="h2">启用证据</Typography>
                <Chip
                  color={verification.enabled ? 'success' : 'error'}
                  variant={verification.enabled ? 'filled' : 'outlined'}
                  label={verification.enabled ? '启用成功' : '未启用成功'}
                />
              </Box>
              <Box sx={{ mt: 1 }}>
                <VerificationRow label="1. ePDG / SWu Child SA" check={verification.swu} />
                <VerificationRow label="2. P-CSCF SIP 响应" check={verification.pcscf} />
                <VerificationRow label="3. IMS-AKA 注册与信令保护" check={verification.ims} />
              </Box>
            </Paper>
          )}

          <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, borderRadius: 1 }}>
            <Box display="flex" alignItems={{ xs: 'stretch', sm: 'center' }} justifyContent="space-between" gap={2} flexDirection={{ xs: 'column', sm: 'row' }}>
              <Box>
                <Typography variant="h6" component="h2">ePDG SWu 隧道</Typography>
                <Typography variant="body2" color="text.secondary">真实发送 UDP 500/4500 IKEv2，使用 SIM 的 AT+CSIM 完成 EAP-AKA。</Typography>
              </Box>
              {tunnel?.running ? (
                <Button
                  color="error"
                  variant="outlined"
                  startIcon={tunnelBusy ? <CircularProgress color="inherit" size={18} /> : <StopIcon />}
                  onClick={() => void stopTunnel()}
                  disabled={tunnelBusy}
                >
                  停止隧道
                </Button>
              ) : (
                <Button
                  variant="contained"
                  startIcon={tunnelBusy ? <CircularProgress color="inherit" size={18} /> : <StartIcon />}
                  onClick={() => void startTunnel()}
                  disabled={tunnelBusy || loading || !selectedModem || tunnel?.helper_available === false || status?.host_tunnel_supported === false}
                >
                  建立 ePDG 隧道
                </Button>
              )}
            </Box>

            {!tunnel?.running && (
              <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'auto minmax(180px, 1fr) minmax(140px, 0.65fr) minmax(140px, 0.65fr)' }, gap: 1.5, alignItems: 'center', mt: 2 }}>
                <FormControlLabel
                  control={<Switch checked={proxyEnabled} onChange={(event) => setProxyEnabled(event.target.checked)} />}
                  label="上游 SOCKS5 UDP"
                  sx={{ m: 0 }}
                />
                <TextField
                  size="small"
                  label="代理地址"
                  placeholder="host:port"
                  value={proxyAddress}
                  onChange={(event) => setProxyAddress(event.target.value)}
                  disabled={!proxyEnabled}
                />
                <TextField
                  size="small"
                  label="用户名"
                  value={proxyUsername}
                  onChange={(event) => setProxyUsername(event.target.value)}
                  disabled={!proxyEnabled}
                />
                <TextField
                  size="small"
                  type="password"
                  label={proxyHasPassword ? '密码（已保存）' : '密码'}
                  value={proxyPassword}
                  onChange={(event) => setProxyPassword(event.target.value)}
                  disabled={!proxyEnabled}
                />
              </Box>
            )}

            <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' }, columnGap: 3, mt: 1 }}>
              <StatusField label="SWu 会话"><BoolChip value={tunnel?.established} yes="已建立" no={tunnelStageLabel(tunnel?.stage)} /></StatusField>
              <StatusField label="Helper"><BoolChip value={tunnel?.helper_available ?? status?.host_tunnel_supported} yes="已安装" no="不可用" /></StatusField>
              <StatusField label="接入链路"><MonoValue>{tunnel?.access_interface ? `${tunnel.access_interface} · ${tunnel.local_ip}` : status?.access_interface || '未知'}</MonoValue></StatusField>
              <StatusField label="ePDG 外层出口"><MonoValue>{tunnel?.proxy_enabled ? `SOCKS5 UDP · ${tunnel.proxy_address}` : '直连'}</MonoValue></StatusField>
              <StatusField label="ePDG"><MonoValue>{tunnel?.epdg_ip ? `${tunnel.epdg_fqdn} · ${tunnel.epdg_ip}` : status?.epdg_fqdn || '未知'}</MonoValue></StatusField>
              <StatusField label="SIM-AKA 接口"><BoolChip value={status?.sim_aka_command_available ?? tunnel?.running} yes={tunnel?.serial_device || 'AT+CSIM 可用'} no="不可用" /></StatusField>
              <StatusField label="隧道地址"><MonoValue>{tunnel?.tunnel_ipv6 || tunnel?.tunnel_ipv4 || '尚未分配'}</MonoValue></StatusField>
              <StatusField label="P-CSCF IPv6"><MonoValue>{tunnel?.pcscf_v6?.join(', ') || '尚未获取'}</MonoValue></StatusField>
              <StatusField label="P-CSCF IPv4"><MonoValue>{tunnel?.pcscf_v4?.join(', ') || '尚未获取'}</MonoValue></StatusField>
              <StatusField label="IMS P-CSCF"><MonoValue>{tunnel?.pcscf_address || '尚未选定'}</MonoValue></StatusField>
              <StatusField label="P-CSCF 策略"><MonoValue>{tunnel?.pcscf_override || '使用 IKE 下发地址'}</MonoValue></StatusField>
              <StatusField label="运营商策略">
                <Box display="flex" alignItems="center" gap={1} flexWrap="wrap">
                  <MonoValue>{tunnel?.carrier_preset_id || '3gpp-default'}</MonoValue>
                  <Chip size="small" variant="outlined" color={tunnel?.carrier_overrides_loaded ? 'success' : 'default'} label={tunnel?.carrier_overrides_loaded ? '外部覆盖已加载' : '使用内置配置'} />
                </Box>
              </StatusField>
              <StatusField label="IMS 注册小区"><MonoValue>{tunnel?.ims_cell_id ? `${tunnel.ims_cell_id} · ${cellIDSourceLabel(tunnel.ims_cell_id_source)}` : cellIDSourceLabel(tunnel?.ims_cell_id_source)}</MonoValue></StatusField>
              <StatusField label="REGISTER 画像"><MonoValue>{tunnel?.ims_register_profile || 'default'} · {tunnel?.ims_user_agent || 'SimAdmin VoWiFi'}</MonoValue></StatusField>
              <StatusField label="IMS Security-Agreement">
                <BoolChip value={tunnel?.ims_ipsec_established} yes="IPsec 已建立" no={tunnel?.ims_registered ? 'IPsec 未建立' : '等待注册'} />
              </StatusField>
              <StatusField label="IMS 信令保护">
                <MonoValue>
                  {tunnel?.ims_security_mode === 'plain' && tunnel?.ims_registered
                    ? 'SWu 隧道保护（无独立 IMS IPsec）'
                    : tunnel?.ims_security_mode || '尚未协商'}
                </MonoValue>
              </StatusField>
              <StatusField label="短信中心"><MonoValue>{tunnel?.smsc || '未读取'}</MonoValue></StatusField>
              <StatusField label="P-CSCF SIP 探测">
                <Box display="flex" alignItems="center" gap={1} flexWrap="wrap">
                  <BoolChip value={tunnel?.pcscf_reachable} yes="已响应" no={tunnel?.pcscf_probe_state === 'sent' ? '等待响应' : '未响应'} />
                  {!!tunnel?.pcscf_sip_code && <Chip size="small" variant="outlined" label={`SIP ${tunnel.pcscf_sip_code}`} />}
                </Box>
              </StatusField>
              <StatusField label="隧道内层数据"><MonoValue>TX {tunnel?.inner_tx_packets ?? 0} · RX {tunnel?.inner_rx_packets ?? 0}</MonoValue></StatusField>
            </Box>
            {tunnel?.error && <Alert severity="error" sx={{ mt: 2 }}>{tunnel.error}</Alert>}
            {locationRejected && (
              <Alert severity="error" sx={{ mt: 2 }}>
                运营商已收到 IMS REGISTER，但拒绝当前网络位置。外层出口：{tunnel?.proxy_enabled ? tunnel.proxy_address : '直连'}；IMS 小区：{tunnel?.ims_cell_id || '全零占位'}（{cellIDSourceLabel(tunnel?.ims_cell_id_source)}）。
              </Alert>
            )}
            {tunnel?.pcscf_probe_error && <Alert severity="warning" sx={{ mt: 2 }}>{tunnel.pcscf_probe_error}</Alert>}
            {!tunnel?.running && status?.host_tunnel_reason && !status.host_tunnel_supported && (
              <Alert severity="warning" sx={{ mt: 2 }}>{status.host_tunnel_reason}</Alert>
            )}
          </Paper>

          {status ? (
            <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, borderRadius: 1, mt: 2 }}>
              <Typography variant="h6" component="h2">IMS 驻网状态</Typography>
              <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' }, columnGap: 3, mt: 1 }}>
                <StatusField label="IMS 配置"><BoolChip value={status.ims_enabled} yes="已启用" no="未启用" /></StatusField>
                <StatusField label="IMS 注册"><BoolChip value={status.registered} yes="已注册" no="未注册" /></StatusField>
                <StatusField label="模块原生 VoWiFi"><BoolChip value={status.vowifi_supported} yes="支持" no="不支持" /></StatusField>
                <StatusField label="原生 VoWiFi 开关"><BoolChip value={status.vowifi_enabled} yes="已开启" no="未开启" /></StatusField>
                <StatusField label="当前承载"><Typography variant="body2" fontWeight={600}>{transportLabel(status.transport)}</Typography></StatusField>
                <StatusField label="运营商配置"><MonoValue>{status.carrier_config || '未知'}</MonoValue></StatusField>
                <StatusField label="ePDG DNS"><BoolChip value={status.epdg_resolved} yes="可解析" no="不可解析" /></StatusField>
                <StatusField label="基带型号"><MonoValue>{status.modem_model || '未知'}</MonoValue></StatusField>
              </Box>
            </Paper>
          ) : (
            <Alert severity="info" sx={{ mt: 2 }}>SWu 会话运行时暂停读取模块 AT 状态，以免与 SIM-AKA 串口通信冲突。</Alert>
          )}

          {verification && (
            <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, borderRadius: 1, mt: 2 }}>
              <Box display="flex" alignItems="center" justifyContent="space-between" gap={2} flexWrap="wrap">
                <Box>
                  <Typography variant="h6" component="h2">VoWiFi 短信验证</Typography>
                </Box>
                <BoolChip value={tunnel?.sms_over_ims_ready} yes="SMS over IMS 已就绪" no="SMS over IMS 未就绪" />
              </Box>

              <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'minmax(220px, 0.7fr) minmax(0, 1.3fr)' }, gap: 2, mt: 2 }}>
                <TextField
                  size="small"
                  label="接收号码"
                  value={smsPhoneNumber}
                  onChange={(event) => setSmsPhoneNumber(event.target.value)}
                  disabled={smsVerifying !== null}
                  fullWidth
                />
                <TextField
                  size="small"
                  label="短信内容"
                  value={smsContent}
                  onChange={(event) => setSmsContent(event.target.value)}
                  disabled={smsVerifying !== null}
                  fullWidth
                />
              </Box>

              <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'repeat(2, minmax(0, 1fr))' }, columnGap: 3, mt: 1 }}>
                <Box sx={{ py: 1.5, borderBottom: 1, borderColor: 'divider' }}>
                  <Box display="flex" alignItems="center" gap={1}>
                    <SendIcon color="action" />
                    <Typography variant="subtitle2">发送路径</Typography>
                    <Chip size="small" variant="outlined" color={verificationStateColor(verification.sms_send.state)} label={verificationStateLabel(verification.sms_send.state)} />
                  </Box>
                  <Typography variant="body2" color="text.secondary" sx={{ minHeight: 40, mt: 1 }}>{smsResult.send?.evidence || verification.sms_send.evidence}</Typography>
                  {(smsResult.send?.message_id || tunnel?.sms_last_tx_message_id) && (
                    <MonoValue>
                      SIP {smsResult.send?.sip_code || tunnel?.sms_last_tx_sip_code || '-'} · {smsResult.send?.rp_state || tunnel?.sms_last_tx_rp_state || '等待 RP-ACK'} · {smsResult.send?.message_id || tunnel?.sms_last_tx_message_id}
                    </MonoValue>
                  )}
                  <Button
                    size="small"
                    variant="contained"
                    startIcon={smsVerifying === 'send' ? <CircularProgress size={16} color="inherit" /> : <SendIcon />}
                    onClick={() => void verifySmsPath('send')}
                    disabled={smsVerifying !== null || !verification.enabled || !smsPhoneNumber.trim() || !smsContent.trim()}
                    sx={{ mt: 1 }}
                  >
                    发送并验证
                  </Button>
                </Box>
                <Box sx={{ py: 1.5, borderBottom: 1, borderColor: 'divider' }}>
                  <Box display="flex" alignItems="center" gap={1}>
                    <ReceiveIcon color="action" />
                    <Typography variant="subtitle2">接收路径</Typography>
                    <Chip size="small" variant="outlined" color={verificationStateColor(verification.sms_receive.state)} label={verificationStateLabel(verification.sms_receive.state)} />
                  </Box>
                  <Typography variant="body2" color="text.secondary" sx={{ minHeight: 40, mt: 1 }}>{smsResult.receive?.evidence || verification.sms_receive.evidence}</Typography>
                  {(smsResult.receive?.message_id || tunnel?.sms_last_rx_id) && (
                    <MonoValue>
                      {smsResult.receive?.from || tunnel?.sms_last_rx_from || '未知发送方'} · RP-MR {smsResult.receive?.rp_mr ?? tunnel?.sms_last_rx_rp_mr ?? '-'} · RP-ACK SIP {smsResult.receive?.rp_ack_sip_code || tunnel?.sms_last_rx_rp_ack_sip_code || '-'}
                    </MonoValue>
                  )}
                  <Button
                    size="small"
                    variant="outlined"
                    startIcon={smsVerifying === 'receive' ? <CircularProgress size={16} /> : <ReceiveIcon />}
                    onClick={() => void verifySmsPath('receive')}
                    disabled={smsVerifying !== null || !verification.enabled}
                    sx={{ mt: 1 }}
                  >
                    等待新短信
                  </Button>
                </Box>
              </Box>
              {!verification.enabled && (
                <Alert severity="warning" sx={{ mt: 2 }}>IMS 尚未通过 SWu 完成鉴权注册，当前不能进行 VoWiFi 短信收发；普通蜂窝短信结果不会计入。</Alert>
              )}
            </Paper>
          )}
        </>
      )}
    </Box>
  )
}
