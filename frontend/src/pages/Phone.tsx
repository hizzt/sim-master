import { useCallback, useEffect, useState, type ChangeEvent, type ReactNode, type SyntheticEvent } from 'react'
import {
  Alert,
  Avatar,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Fade,
  IconButton,
  List,
  ListItem,
  ListItemAvatar,
  ListItemSecondaryAction,
  ListItemText,
  Paper,
  Snackbar,
  Tab,
  Tabs,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  Backspace,
  AddCircleOutline,
  Call,
  CallEnd,
  CallMade,
  CallReceived,
  Delete,
  DeleteSweep,
  Dialpad,
  History,
  Phone as PhoneIcon,
  PhoneCallback,
  PhoneMissed,
  Refresh,
  SettingsInputAntenna,
  WifiCalling3,
} from '@mui/icons-material'
import {
  api,
  type CallInfo,
  type CallRecord,
  type CallStats,
  type ImsStatusResponse,
  type VowifiCallAudioFormat,
  type VowifiCallAudioStats,
  type VowifiCallStatus,
  type VowifiTunnelStatus,
} from '../api/current'
import { PageHeader } from '../components/PageFrame'

const dialpadButtons = [
  ['1', '2', '3'],
  ['4', '5', '6'],
  ['7', '8', '9'],
  ['*', '0', '#'],
]

// IMS status is intentionally visible on the phone page. VoWiFi call control
// is gated by the host SWu/IMS path and a successful helper capability probe;
// the independent baseband voice_capable flag is not part of that decision.
const SHOW_IMS_VOWIFI = true
const MAX_VOWIFI_AUDIO_BYTES = 1_920_000

const emptyStats: CallStats = {
  total: 0,
  incoming: 0,
  outgoing: 0,
  missed: 0,
  total_duration: 0,
}

function getStateLabel(state: string): string {
  const labels: Record<string, string> = {
    active: '通话中',
    dialing: '拨号中',
    alerting: '响铃中',
    incoming: '来电',
    waiting: '等待接听',
    held: '保持',
    terminated: '已结束',
  }
  return labels[state] || state
}

function directionLabel(direction: string): string {
  if (direction === 'incoming') return '来电'
  if (direction === 'outgoing') return '拨出'
  if (direction === 'missed') return '未接'
  return '未知'
}

function formatDuration(seconds: number): string {
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
}

function formatTime(timestamp: string): string {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) return timestamp
  const now = new Date()
  if (date.toDateString() === now.toDateString()) {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return date.toLocaleDateString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

function getCallIcon(direction: string, answered: boolean) {
  if (direction === 'missed') return <PhoneMissed color="error" />
  if (direction === 'incoming') return answered ? <CallReceived color="success" /> : <PhoneMissed color="error" />
  return <CallMade color="primary" />
}

function getVowifiCallStateLabel(state: string): string {
  const labels: Record<string, string> = {
    dialing: '正在发送 INVITE',
    proceeding: '网络处理中',
    ringing: '对端振铃',
    signaling_established: '信令已建立',
    terminating: '正在挂断',
    terminated: '已结束',
    failed: '信令失败',
  }
  return labels[state] || state
}

function isVowifiCallTerminal(state: string): boolean {
  return state === 'terminated' || state === 'failed'
}

function hasReceivedVowifiRtp(call: VowifiCallStatus): boolean {
  return call.media_ready || call.rtp_packets_received > 0
}

function readFileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error ?? new Error('读取音频文件失败'))
    reader.onload = () => {
      const result = typeof reader.result === 'string' ? reader.result : ''
      const separator = result.indexOf(',')
      if (separator < 0) {
        reject(new Error('音频文件编码失败'))
        return
      }
      resolve(result.slice(separator + 1))
    }
    reader.readAsDataURL(file)
  })
}

function downloadBase64Audio(dataBase64: string, contentType: string, filename: string) {
  const decoded = window.atob(dataBase64)
  const bytes = new Uint8Array(decoded.length)
  for (let index = 0; index < decoded.length; index += 1) {
    bytes[index] = decoded.charCodeAt(index)
  }
  const url = URL.createObjectURL(new Blob([bytes], { type: contentType || 'application/octet-stream' }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

function BooleanStatusChip({
  value,
  trueLabel,
  falseLabel,
}: {
  value?: boolean | null
  trueLabel: string
  falseLabel: string
}) {
  if (value === undefined || value === null) {
    return <Chip label="未知" size="small" variant="outlined" />
  }
  return (
    <Chip
      label={value ? trueLabel : falseLabel}
      color={value ? 'success' : 'default'}
      size="small"
      variant="outlined"
    />
  )
}

function ImsStatusItem({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box sx={{ minWidth: 0, py: 1.5, borderBottom: 1, borderColor: 'divider' }}>
      <Typography variant="caption" color="text.secondary" display="block" mb={0.75}>
        {label}
      </Typography>
      {children}
    </Box>
  )
}

export default function PhonePage() {
  const [tabValue, setTabValue] = useState(0)
  const [calls, setCalls] = useState<CallInfo[]>([])
  const [, setCallsLoading] = useState(false)
  const [dialNumber, setDialNumber] = useState('')
  const [dialLoading, setDialLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [callHistory, setCallHistory] = useState<CallRecord[]>([])
  const [callStats, setCallStats] = useState<CallStats>(emptyStats)
  const [historyLoading, setHistoryLoading] = useState(false)
  const [clearDialogOpen, setClearDialogOpen] = useState(false)
  const [imsStatus, setImsStatus] = useState<ImsStatusResponse | null>(null)
  const [vowifiStatus, setVowifiStatus] = useState<VowifiTunnelStatus | null>(null)
  const [vowifiCalls, setVowifiCalls] = useState<VowifiCallStatus[]>([])
  const [vowifiCallControlSupported, setVowifiCallControlSupported] = useState(false)
  const [vowifiDialLoading, setVowifiDialLoading] = useState(false)
  const [vowifiHangupCallId, setVowifiHangupCallId] = useState<string | null>(null)
  const [vowifiAudioBusy, setVowifiAudioBusy] = useState<{ callId: string; action: 'play' | 'recording' | 'stats' } | null>(null)
  const [vowifiAudioStats, setVowifiAudioStats] = useState<Record<string, VowifiCallAudioStats>>({})
  const [imsLoading, setImsLoading] = useState(false)
  const [imsRegistering, setImsRegistering] = useState(false)

  const fetchCalls = useCallback(async () => {
    setCallsLoading(true)
    try {
      const response = await api.getCalls()
      setCalls(response.data?.calls ?? [])
    } catch (err) {
      console.warn('获取通话列表失败:', err)
    } finally {
      setCallsLoading(false)
    }
  }, [])

  const fetchCallHistory = useCallback(async () => {
    setHistoryLoading(true)
    try {
      const response = await api.getCallHistory({ limit: 100, offset: 0 })
      setCallHistory(response.data?.records ?? [])
      setCallStats(response.data?.stats ?? emptyStats)
    } catch (err) {
      console.warn('获取通话记录失败:', err)
    } finally {
      setHistoryLoading(false)
    }
  }, [])

  const fetchImsStatus = useCallback(async () => {
    setImsLoading(true)
    setError(null)
    try {
      const [imsResult, tunnelResult] = await Promise.allSettled([
        api.getImsStatus(),
        api.getVowifiTunnelStatus(),
      ])
      if (imsResult.status === 'fulfilled') {
        setImsStatus(imsResult.value.data ?? null)
      } else {
        console.warn('基带 IMS 状态读取失败:', imsResult.reason)
      }
      if (tunnelResult.status === 'fulfilled') {
        setVowifiStatus(tunnelResult.value.data ?? null)
      } else {
        throw tunnelResult.reason
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Host VoWiFi 状态读取失败')
    } finally {
      setImsLoading(false)
    }
  }, [])

  const fetchVowifiCalls = useCallback(async () => {
    try {
      const response = await api.getVowifiCallStatuses()
      setVowifiCalls(response.data ?? [])
      setVowifiCallControlSupported(true)
    } catch {
      setVowifiCallControlSupported(false)
    }
  }, [])

  const handleImsRegistration = async () => {
    setImsRegistering(true)
    setError(null)
    setSuccess(null)
    try {
      const response = await api.startImsRegistration()
      setImsStatus(response.data ?? null)
      setSuccess(response.data?.registered ? 'IMS 已驻网' : '已发起 IMS 驻网，正在等待运营商注册')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'IMS 驻网失败')
    } finally {
      setImsRegistering(false)
    }
  }

  useEffect(() => {
    void fetchCalls()
    void fetchCallHistory()
    void fetchImsStatus()
    void fetchVowifiCalls()
    const timer = window.setInterval(() => {
      void fetchCalls()
      void fetchVowifiCalls()
    }, 3000)
    return () => window.clearInterval(timer)
  }, [fetchCalls, fetchCallHistory, fetchImsStatus, fetchVowifiCalls])

  const handleDialpadPress = (digit: string) => {
    setDialNumber((prev) => prev + digit)
  }

  const handleDial = async (number = dialNumber) => {
    const target = number.trim()
    if (!target) {
      setError('请输入电话号码')
      return
    }

    setDialLoading(true)
    setError(null)
    setSuccess(null)
    try {
      const response = await api.dialCall(target)
      const path = response.data?.path
      if (path) {
        setCalls((prev) => (
          prev.some((call) => call.path === path)
            ? prev
            : [{ path, phone_number: target, state: 'dialing', direction: 'outgoing' }, ...prev]
        ))
      }
      setSuccess(`正在拨打 ${target}`)
      setDialNumber('')
      void fetchCallHistory()
    } catch (err) {
      setError(err instanceof Error ? err.message : '拨号失败')
    } finally {
      setDialLoading(false)
    }
  }

  const handleVowifiDial = async (number = dialNumber) => {
    const target = number.trim()
    if (!target) {
      setError('请输入电话号码')
      return
    }

    setVowifiDialLoading(true)
    setError(null)
    setSuccess(null)
    try {
      const response = await api.dialVowifiCall(target)
      const startedCall = response.data
      if (startedCall) {
        setVowifiCalls((previous) => [
          startedCall,
          ...previous.filter((call) => call.call_id !== startedCall.call_id),
        ])
      }
      setSuccess(`已通过 VoWiFi 发起 ${target}（IMS INVITE 已发送，等待 RTP）`)
      setDialNumber('')
      void fetchVowifiCalls()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'VoWiFi 拨号失败')
    } finally {
      setVowifiDialLoading(false)
    }
  }

  const handleVowifiHangup = async (call: VowifiCallStatus) => {
    setVowifiHangupCallId(call.call_id)
    setError(null)
    setSuccess(null)
    try {
      const response = await api.hangupVowifiCall(call.call_id)
      const updatedCall = response.data
      if (updatedCall) {
        setVowifiCalls((previous) => previous.map((current) => (
          current.call_id === updatedCall.call_id ? updatedCall : current
        )))
      }
      setSuccess(`已挂断 VoWiFi 呼叫 ${call.phone_number}`)
      void fetchVowifiCalls()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'VoWiFi 挂断失败')
    } finally {
      setVowifiHangupCallId(null)
    }
  }

  const handleVowifiAudioFile = async (call: VowifiCallStatus, event: ChangeEvent<HTMLInputElement>) => {
    const file = event.currentTarget.files?.[0]
    event.currentTarget.value = ''
    if (!file) return
    const audioFormat: VowifiCallAudioFormat = file.name.toLowerCase().endsWith('.wav') ? 'wav' : 'pcm_s16le'
    const maxFileBytes = audioFormat === 'wav' ? MAX_VOWIFI_AUDIO_BYTES + 64 * 1024 : MAX_VOWIFI_AUDIO_BYTES
    if (file.size > maxFileBytes) {
      setError('音频文件超过 1,920,000 字节（8 kHz 单声道 PCM 最长 120 秒）')
      return
    }
    setVowifiAudioBusy({ callId: call.call_id, action: 'play' })
    setError(null)
    setSuccess(null)
    try {
      const audioBase64 = await readFileAsBase64(file)
      const response = await api.playVowifiCallAudio(call.call_id, audioFormat, audioBase64)
      const stats = response.data?.stats
      if (stats) {
        setVowifiAudioStats((previous) => ({ ...previous, [call.call_id]: stats }))
      }
      setSuccess(`已向 ${call.phone_number || call.call_id} 播放完 ${audioFormat === 'wav' ? 'WAV' : 'PCM'} 音频`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'VoWiFi 音频播放失败')
    } finally {
      setVowifiAudioBusy(null)
    }
  }

  const handleVowifiRecording = async (call: VowifiCallStatus) => {
    setVowifiAudioBusy({ callId: call.call_id, action: 'recording' })
    setError(null)
    setSuccess(null)
    try {
      const response = await api.getVowifiCallRecording(call.call_id, 'wav')
      const audio = response.data
      if (!audio?.data_base64 || audio.stats.recording_bytes === 0) {
        throw new Error('当前呼叫还没有可下载的录音')
      }
      setVowifiAudioStats((previous) => ({ ...previous, [call.call_id]: audio.stats }))
      downloadBase64Audio(audio.data_base64, audio.content_type, `vowifi-${call.call_id}.wav`)
      setSuccess(`已下载 ${call.phone_number || call.call_id} 的 VoWiFi 接收音频`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'VoWiFi 录音下载失败')
    } finally {
      setVowifiAudioBusy(null)
    }
  }

  const handleVowifiAudioStats = async (call: VowifiCallStatus) => {
    setVowifiAudioBusy({ callId: call.call_id, action: 'stats' })
    setError(null)
    try {
      const response = await api.getVowifiCallAudioStats(call.call_id)
      const stats = response.data?.stats
      if (stats) {
        setVowifiAudioStats((previous) => ({ ...previous, [call.call_id]: stats }))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'VoWiFi 音频统计读取失败')
    } finally {
      setVowifiAudioBusy(null)
    }
  }

  const handleHangupAll = async () => {
    setError(null)
    setSuccess(null)
    const currentCalls = calls
    try {
      if (currentCalls.length === 1) {
        await api.hangupCall(currentCalls[0].path)
      } else {
        await api.hangupAllCalls()
      }
      setCalls([])
      setSuccess('已挂断所有通话')
      void fetchCalls()
      void fetchCallHistory()
    } catch (err) {
      setError(err instanceof Error ? err.message : '挂断失败')
    }
  }

  const handleAnswer = async (call: CallInfo) => {
    setError(null)
    setSuccess(null)
    try {
      await api.answerCall(call.path)
      setSuccess(`已接听 ${call.phone_number || '来电'}`)
      void fetchCalls()
    } catch (err) {
      setError(err instanceof Error ? err.message : '接听失败')
    }
  }

  const handleDeleteRecord = async (id: number) => {
    setError(null)
    try {
      await api.deleteCallRecord(id)
      setSuccess('记录已删除')
      void fetchCallHistory()
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除记录失败')
    }
  }

  const handleClearHistory = async () => {
    setClearDialogOpen(false)
    setError(null)
    try {
      await api.clearCallHistory()
      setSuccess('所有通话记录已清空')
      void fetchCallHistory()
    } catch (err) {
      setError(err instanceof Error ? err.message : '清空记录失败')
    }
  }

  const handleFillFromHistory = (phoneNumber: string) => {
    setDialNumber(phoneNumber)
    setTabValue(0)
  }

  const singleCall = calls.length === 1 ? calls[0] : null
  const imsRegistration = imsStatus?.source.includes('at-qimscfg') || imsStatus?.source.includes('at-qcfg')
    ? imsStatus.registered
    : null
  const imsTransport = imsStatus?.transport === 'cellular'
    ? '蜂窝网络'
    : imsStatus?.transport === 'wifi'
      ? 'Wi-Fi'
      : imsStatus?.transport === 'none'
        ? '未注册'
        : '未知'
  const vowifiSecurityAgreement = vowifiStatus?.ims_security_mode === 'ipsec3gpp'
    && vowifiStatus.ims_ipsec_established
  const vowifiVoiceReady = vowifiCallControlSupported
    && vowifiStatus?.helper_available === true
    && vowifiStatus.running
    && vowifiStatus.established
    && vowifiStatus.ims_registered
    && vowifiStatus.ims_authenticated
    && vowifiStatus.ims_transport.toLowerCase() === 'swu'
  const vowifiUnavailableReason = !vowifiCallControlSupported
    ? '当前 helper 未暴露 IMS INVITE 呼叫控制'
    : !vowifiStatus?.running || !vowifiStatus.established
      ? 'SWu 隧道尚未建立'
      : !vowifiStatus.ims_registered || !vowifiStatus.ims_authenticated
        ? 'Host IMS 尚未通过 SWu 完成注册和鉴权'
        : vowifiStatus.ims_transport.toLowerCase() !== 'swu'
          ? '当前 IMS 注册不是 SWu 承载'
          : 'VoWiFi 呼叫暂不可用'

  const handleTabChange = (_event: SyntheticEvent, value: number) => {
    setTabValue(value)
    if (value === 2) void fetchImsStatus()
  }

  return (
    <Box>
      <PageHeader title="电话管理" />

      <Snackbar open={!!error} autoHideDuration={4000} resumeHideDuration={3000} onClose={() => setError(null)} anchorOrigin={{ vertical: 'top', horizontal: 'center' }}>
        <Alert severity="error" onClose={() => setError(null)} variant="filled">{error}</Alert>
      </Snackbar>
      <Snackbar open={!!success} autoHideDuration={3000} resumeHideDuration={3000} onClose={() => setSuccess(null)} anchorOrigin={{ vertical: 'top', horizontal: 'center' }}>
        <Alert severity="success" onClose={() => setSuccess(null)} variant="filled">{success}</Alert>
      </Snackbar>

      <Fade in={calls.length > 0}>
        <Paper
          elevation={6}
          sx={{
            mb: 2,
            p: 2,
            bgcolor: 'success.main',
            color: 'white',
            display: calls.length > 0 ? 'block' : 'none',
          }}
        >
          <Box display="flex" justifyContent="space-between" alignItems="center" gap={2}>
            <Box display="flex" alignItems="center" gap={2}>
              <PhoneCallback />
              <Box>
                <Typography variant="h6" fontWeight={600}>
                  {singleCall ? (singleCall.phone_number || '未知号码') : `${calls.length} 个通话中`}
                </Typography>
                {singleCall && (
                  <Typography variant="body2" sx={{ opacity: 0.9 }}>
                    {getStateLabel(singleCall.state)} - {directionLabel(singleCall.direction)}
                  </Typography>
                )}
              </Box>
            </Box>
            <Box display="flex" gap={1}>
              {singleCall && (singleCall.state === 'incoming' || singleCall.state === 'waiting') && (
                <Button
                  variant="contained"
                  color="inherit"
                  sx={{ color: 'success.main', bgcolor: 'white' }}
                  startIcon={<Call />}
                  onClick={() => void handleAnswer(singleCall)}
                >
                  接听
                </Button>
              )}
              <Button variant="contained" color="error" startIcon={<CallEnd />} onClick={() => void handleHangupAll()}>
                挂断{calls.length > 1 ? '全部' : ''}
              </Button>
            </Box>
          </Box>
        </Paper>
      </Fade>

      <Tabs value={tabValue} onChange={handleTabChange} sx={{ mb: 2 }} variant="scrollable" scrollButtons="auto">
        <Tab icon={<Dialpad />} label="拨号" iconPosition="start" />
        <Tab icon={<History />} label="通话记录" iconPosition="start" />
        {SHOW_IMS_VOWIFI && <Tab icon={<WifiCalling3 />} label="IMS / VoWiFi" iconPosition="start" />}
      </Tabs>

      {tabValue === 0 && (
        <Card>
          <CardContent>
            <Alert severity="info" sx={{ mb: 2 }}>
              VoWiFi 通过独立的 Host SWu / IMS 路径发送 INVITE。G.711 通话可上传 8 kHz 单声道 WAV/PCM 播放并下载接收录音；实时麦克风/扬声器尚未接入，失败不会自动回退到蜂窝拨号。
            </Alert>
            <Box display="flex" gap={1} mb={2} flexWrap="wrap" justifyContent="center">
              <Chip
                size="small"
                variant="outlined"
                color={vowifiStatus?.established ? 'success' : 'default'}
                label={vowifiStatus?.established ? 'SWu 已建立' : 'SWu 未建立'}
              />
              <Chip
                size="small"
                variant="outlined"
                color={vowifiStatus?.ims_registered && vowifiStatus.ims_authenticated ? 'success' : 'default'}
                label={vowifiStatus?.ims_registered && vowifiStatus.ims_authenticated ? 'Host IMS 已鉴权注册' : 'Host IMS 未就绪'}
              />
              <Chip
                size="small"
                variant="outlined"
                color={vowifiCallControlSupported ? 'success' : 'default'}
                label={vowifiCallControlSupported ? 'INVITE 控制可用' : 'INVITE 控制不可用'}
              />
            </Box>
            <Box display="flex" flexDirection="column" alignItems="center" maxWidth={320} mx="auto">
              <TextField
                fullWidth
                variant="standard"
                value={dialNumber}
                onChange={(event) => setDialNumber(event.target.value)}
                placeholder="输入电话号码"
                inputProps={{ inputMode: 'tel', style: { textAlign: 'center', fontSize: '1.5rem' } }}
                InputProps={{
                  endAdornment: dialNumber ? (
                    <IconButton size="small" onClick={() => setDialNumber((prev) => prev.slice(0, -1))}>
                      <Backspace />
                    </IconButton>
                  ) : null,
                }}
                sx={{ mb: 3 }}
              />

              <Box sx={{ width: '100%' }}>
                {dialpadButtons.map((row) => (
                  <Box key={row.join('')} display="flex" justifyContent="center" gap={2} mb={1.5}>
                    {row.map((digit) => (
                      <Button
                        key={digit}
                        variant="outlined"
                        onClick={() => handleDialpadPress(digit)}
                        sx={{ width: 72, height: 72, borderRadius: '50%', fontSize: '1.5rem', fontWeight: 500 }}
                      >
                        {digit}
                      </Button>
                    ))}
                  </Box>
                ))}
              </Box>

              <Box display="flex" gap={1.5} mt={2} flexWrap="wrap" justifyContent="center">
                <Button
                  variant="contained"
                  color="success"
                  size="large"
                  startIcon={dialLoading ? <CircularProgress size={20} color="inherit" /> : <PhoneIcon />}
                  onClick={() => void handleDial()}
                  disabled={dialLoading || vowifiDialLoading || !dialNumber.trim()}
                  sx={{ minWidth: 150, height: 56, borderRadius: 1 }}
                >
                  {dialLoading ? '拨号中' : '蜂窝拨号'}
                </Button>
                <Tooltip title={vowifiVoiceReady ? '通过 Host SWu / IMS 发起 INVITE；G.711 建立后可用文件播放和录音' : vowifiUnavailableReason}>
                  <span>
                    <Button
                      variant="outlined"
                      size="large"
                      startIcon={vowifiDialLoading ? <CircularProgress size={20} /> : <WifiCalling3 />}
                      onClick={() => void handleVowifiDial()}
                      disabled={dialLoading || vowifiDialLoading || !dialNumber.trim() || !vowifiVoiceReady}
                      sx={{ minWidth: 150, height: 56, borderRadius: 1 }}
                    >
                      {vowifiDialLoading ? '发送 INVITE' : 'VoWiFi 拨号'}
                    </Button>
                  </span>
                </Tooltip>
              </Box>
            </Box>

            {vowifiCalls.length > 0 && (
              <Box mt={3}>
                <Typography variant="subtitle1" fontWeight={600} mb={1}>VoWiFi 呼叫状态</Typography>
                <Box display="flex" flexDirection="column" gap={1}>
                  {vowifiCalls.map((call) => (
                    <Paper key={call.call_id} variant="outlined" sx={{ p: 1.5 }}>
                      <Box display="flex" justifyContent="space-between" alignItems="center" gap={2} flexWrap="wrap">
                        <Box minWidth={0}>
                          <Typography fontWeight={600} sx={{ wordBreak: 'break-word' }}>{call.phone_number || '未知号码'}</Typography>
                          <Box display="flex" gap={0.75} mt={0.75} flexWrap="wrap">
                            <Chip
                              size="small"
                              color={call.state === 'failed' ? 'error' : isVowifiCallTerminal(call.state) ? 'default' : 'primary'}
                              label={getVowifiCallStateLabel(call.state)}
                            />
                            {call.sip_code > 0 && <Chip size="small" variant="outlined" label={`SIP ${call.sip_code}`} />}
                            <Chip
                              size="small"
                              color={hasReceivedVowifiRtp(call) ? 'success' : isVowifiCallTerminal(call.state) ? 'default' : 'warning'}
                              variant="outlined"
                              label={hasReceivedVowifiRtp(call) ? '已收到 RTP' : isVowifiCallTerminal(call.state) ? '未收到 RTP' : '等待 RTP'}
                            />
                            <Chip
                              size="small"
                              color={call.audio_ready ? 'success' : 'default'}
                              variant="outlined"
                              label={call.audio_ready ? (call.audio_mode === 'g711_file_io' ? 'G.711 文件音频就绪' : '本机音频就绪') : call.audio_mode === 'codec_not_supported' ? '当前编解码不支持音频' : '本机音频未就绪'}
                            />
                            {call.media_codec && <Chip size="small" variant="outlined" label={call.media_codec} />}
                            {call.media_direction && <Chip size="small" variant="outlined" label={call.media_direction} />}
                          </Box>
                          {call.reason && (
                            <Typography variant="caption" color="text.secondary" display="block" mt={0.75} sx={{ wordBreak: 'break-word' }}>
                              {call.reason}
                            </Typography>
                          )}
                          {(call.rtp_packets_received > 0 || call.rtcp_packets_received > 0) && (
                            <Typography variant="caption" color="text.secondary" display="block" mt={0.5}>
                              RTP {call.rtp_packets_received.toLocaleString()} 包 / {call.rtp_bytes_received.toLocaleString()} B；RTCP {call.rtcp_packets_received.toLocaleString()} 包 / {call.rtcp_bytes_received.toLocaleString()} B
                            </Typography>
                          )}
                          {vowifiAudioStats[call.call_id] && (
                            <Typography variant="caption" color="text.secondary" display="block" mt={0.5}>
                              音频发送 {vowifiAudioStats[call.call_id].rtp_packets_sent.toLocaleString()} 包；录音 {(vowifiAudioStats[call.call_id].recording_duration_ms / 1000).toFixed(1)} 秒；丢包 {vowifiAudioStats[call.call_id].rtp_packets_lost.toLocaleString()}；乱序 {vowifiAudioStats[call.call_id].rtp_packets_out_of_order.toLocaleString()}
                              {vowifiAudioStats[call.call_id].recording_truncated ? '；录音已达到 120 秒上限' : ''}
                            </Typography>
                          )}
                          {vowifiAudioStats[call.call_id]?.last_playback_error && (
                            <Typography variant="caption" color="error" display="block" mt={0.5}>
                              {vowifiAudioStats[call.call_id].last_playback_error}
                            </Typography>
                          )}
                        </Box>
                        <Box display="flex" gap={1} flexWrap="wrap" justifyContent="flex-end">
                          <Button
                            component="label"
                            size="small"
                            variant="outlined"
                            disabled={!call.audio_ready || isVowifiCallTerminal(call.state) || vowifiAudioBusy !== null}
                          >
                            {vowifiAudioBusy?.callId === call.call_id && vowifiAudioBusy.action === 'play' ? '上传中' : '播放 WAV/PCM'}
                            <input
                              hidden
                              type="file"
                              accept=".wav,.pcm,audio/wav,audio/x-wav,application/octet-stream"
                              onChange={(event) => void handleVowifiAudioFile(call, event)}
                            />
                          </Button>
                          <Button
                            size="small"
                            variant="outlined"
                            onClick={() => void handleVowifiRecording(call)}
                            disabled={!call.audio_ready || vowifiAudioBusy !== null}
                          >
                            {vowifiAudioBusy?.callId === call.call_id && vowifiAudioBusy.action === 'recording' ? '读取中' : '下载录音'}
                          </Button>
                          <Button
                            size="small"
                            variant="text"
                            onClick={() => void handleVowifiAudioStats(call)}
                            disabled={vowifiAudioBusy !== null}
                          >
                            {vowifiAudioBusy?.callId === call.call_id && vowifiAudioBusy.action === 'stats' ? '刷新中' : '音频统计'}
                          </Button>
                          {!isVowifiCallTerminal(call.state) && (
                            <Button
                              size="small"
                              color="error"
                              variant="contained"
                              startIcon={vowifiHangupCallId === call.call_id ? <CircularProgress size={18} color="inherit" /> : <CallEnd />}
                              onClick={() => void handleVowifiHangup(call)}
                              disabled={vowifiHangupCallId !== null}
                            >
                              {vowifiHangupCallId === call.call_id ? '挂断中' : '挂断呼叫'}
                            </Button>
                          )}
                        </Box>
                      </Box>
                    </Paper>
                  ))}
                </Box>
              </Box>
            )}
          </CardContent>
        </Card>
      )}

      {tabValue === 1 && (
        <Card>
          <CardContent>
            <Box display="flex" gap={2} mb={2} flexWrap="wrap">
              <Paper sx={{ p: 1.5, flex: 1, minWidth: 80 }}>
                <Typography variant="h6" color="primary" fontWeight={600}>{callStats.total}</Typography>
                <Typography variant="caption" color="text.secondary">总计</Typography>
              </Paper>
              <Paper sx={{ p: 1.5, flex: 1, minWidth: 80 }}>
                <Typography variant="h6" color="success.main" fontWeight={600}>{callStats.incoming}</Typography>
                <Typography variant="caption" color="text.secondary">来电</Typography>
              </Paper>
              <Paper sx={{ p: 1.5, flex: 1, minWidth: 80 }}>
                <Typography variant="h6" color="info.main" fontWeight={600}>{callStats.outgoing}</Typography>
                <Typography variant="caption" color="text.secondary">拨出</Typography>
              </Paper>
              <Paper sx={{ p: 1.5, flex: 1, minWidth: 80 }}>
                <Typography variant="h6" color="error.main" fontWeight={600}>{callStats.missed}</Typography>
                <Typography variant="caption" color="text.secondary">未接</Typography>
              </Paper>
            </Box>

            <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
              <Typography variant="subtitle1" fontWeight={600}>通话记录 ({callHistory.length})</Typography>
              <Box display="flex" gap={1}>
                <IconButton color="primary" onClick={() => void fetchCallHistory()} disabled={historyLoading}>
                  {historyLoading ? <CircularProgress size={20} /> : <Refresh />}
                </IconButton>
                {callHistory.length > 0 && (
                  <Button variant="outlined" color="error" size="small" startIcon={<DeleteSweep />} onClick={() => setClearDialogOpen(true)}>
                    清空
                  </Button>
                )}
              </Box>
            </Box>

            {historyLoading && callHistory.length === 0 ? (
              <Box display="flex" justifyContent="center" py={4}>
                <CircularProgress />
              </Box>
            ) : callHistory.length === 0 ? (
              <Alert severity="info">暂无通话记录</Alert>
            ) : (
              <List sx={{ maxHeight: 460, overflow: 'auto' }}>
                {callHistory.map((record) => (
                  <ListItem key={record.id} divider>
                    <ListItemAvatar>
                      <Avatar sx={{ bgcolor: record.direction === 'missed' ? 'error.light' : record.direction === 'incoming' ? 'success.light' : 'primary.light' }}>
                        {getCallIcon(record.direction, record.answered)}
                      </Avatar>
                    </ListItemAvatar>
                    <ListItemText
                      primary={
                        <Box display="flex" alignItems="center" gap={1} flexWrap="wrap">
                          <Typography variant="body1" fontWeight={600}>{record.phone_number || '未知号码'}</Typography>
                          <Chip label={directionLabel(record.direction)} size="small" variant="outlined" />
                          {record.transport === 'vowifi' && <Chip label="VoWiFi" size="small" color="info" variant="outlined" />}
                          {record.duration > 0 && <Chip label={formatDuration(record.duration)} size="small" variant="outlined" />}
                        </Box>
                      }
                      secondary={formatTime(record.start_time)}
                    />
                    <ListItemSecondaryAction>
                      <Box display="flex" gap={0.5}>
                        <Tooltip title="重拨">
                          <IconButton size="small" color="primary" onClick={() => void handleDial(record.phone_number)} disabled={dialLoading || !record.phone_number}>
                            <Call fontSize="small" />
                          </IconButton>
                        </Tooltip>
                        <Tooltip title="填入拨号盘">
                          <IconButton size="small" color="info" onClick={() => handleFillFromHistory(record.phone_number)} disabled={!record.phone_number}>
                            <AddCircleOutline fontSize="small" />
                          </IconButton>
                        </Tooltip>
                        <Tooltip title="删除">
                          <IconButton size="small" color="error" onClick={() => void handleDeleteRecord(record.id)}>
                            <Delete fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </Box>
                    </ListItemSecondaryAction>
                  </ListItem>
                ))}
              </List>
            )}
          </CardContent>
        </Card>
      )}

      {SHOW_IMS_VOWIFI && tabValue === 2 && (
        <Card>
          <CardContent>
            <Box display="flex" alignItems="center" justifyContent="space-between" gap={2} mb={2} flexWrap="wrap">
              <Box display="flex" alignItems="center" gap={1} minWidth={0}>
                <SettingsInputAntenna color="primary" />
                <Typography variant="subtitle1" fontWeight={600}>IMS / VoWiFi 驻网</Typography>
              </Box>
              <Box display="flex" alignItems="center" gap={1}>
                <Button
                  variant="contained"
                  size="small"
                  startIcon={imsRegistering ? <CircularProgress color="inherit" size={16} /> : <SettingsInputAntenna />}
                  onClick={() => void handleImsRegistration()}
                  disabled={imsRegistering || imsRegistration === true}
                >
                  {imsRegistration === true ? '已驻网' : imsStatus?.ims_enabled ? '重新驻网' : '开始驻网'}
                </Button>
                <Tooltip title="刷新 IMS 状态">
                  <span>
                    <IconButton color="primary" onClick={() => void fetchImsStatus()} disabled={imsLoading || imsRegistering}>
                      {imsLoading ? <CircularProgress size={20} /> : <Refresh />}
                    </IconButton>
                  </span>
                </Tooltip>
              </Box>
            </Box>

            {imsLoading && !imsStatus && !vowifiStatus ? (
              <Box display="flex" justifyContent="center" py={6}>
                <CircularProgress />
              </Box>
            ) : imsStatus || vowifiStatus ? (
              <>
                <Alert severity={vowifiStatus?.ims_registered ? 'success' : imsStatus?.ims_enabled ? 'warning' : 'info'} sx={{ mb: 2 }}>
                  {vowifiStatus?.ims_registered
                    ? 'Host IMS 已通过 SWu 驻网。Security-Agreement、Voice 和 SMS 是独立能力；信令拨号不依赖基带 voice_capable。'
                    : imsStatus?.ims_enabled
                      ? 'IMS 功能已启用，Host SWu / IMS 当前仍未完成注册。'
                      : 'IMS 尚未就绪，请先建立 SWu 并完成 Host IMS 注册。'}
                </Alert>

                <Box
                  sx={{
                    display: 'grid',
                    gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' },
                    columnGap: 3,
                  }}
                >
                  <ImsStatusItem label="IMS 驻网功能">
                    <BooleanStatusChip value={imsStatus?.ims_enabled} trueLabel="已启用" falseLabel="未启用" />
                  </ImsStatusItem>
                  <ImsStatusItem label="Host IMS 驻网状态">
                    <BooleanStatusChip value={vowifiStatus?.ims_registered} trueLabel="已驻网" falseLabel="未驻网" />
                  </ImsStatusItem>
                  <ImsStatusItem label="VoWiFi 驻网接口">
                    <BooleanStatusChip value={imsStatus?.vowifi_supported} trueLabel="可用" falseLabel="不可用" />
                  </ImsStatusItem>
                  <ImsStatusItem label="VoWiFi / IMS 驻网模式">
                    <BooleanStatusChip value={imsStatus?.vowifi_enabled} trueLabel="已启用" falseLabel="未启用" />
                  </ImsStatusItem>
                  <ImsStatusItem label="IMS Security-Agreement">
                    <BooleanStatusChip value={vowifiSecurityAgreement} trueLabel="已建立" falseLabel="未建立" />
                    <Typography variant="caption" color="text.secondary" display="block" mt={0.5}>
                      需要 P-CSCF 返回 Security-Server 并完成独立 3GPP IMS IPsec；SWu Child SA 不等同于该协商。
                    </Typography>
                  </ImsStatusItem>
                  <ImsStatusItem label="Voice over IMS">
                    <BooleanStatusChip value={vowifiVoiceReady} trueLabel="可拨号" falseLabel="未就绪" />
                    <Typography variant="caption" color="text.secondary" display="block" mt={0.5}>
                      支持 INVITE、CANCEL/BYE、RTP/RTCP 与 G.711 文件播放/录音；未接入实时麦克风和扬声器。
                    </Typography>
                  </ImsStatusItem>
                  <ImsStatusItem label="SMS over IMS">
                    <BooleanStatusChip value={vowifiStatus?.sms_over_ims_ready} trueLabel="独立就绪" falseLabel="未就绪" />
                  </ImsStatusItem>
                  <ImsStatusItem label="Host IMS 承载">
                    <Typography variant="body2" fontWeight={600}>{vowifiStatus?.ims_transport || '未注册'}</Typography>
                  </ImsStatusItem>
                  <ImsStatusItem label="基带 IMS 承载">
                    <Typography variant="body2" fontWeight={600}>{imsTransport}</Typography>
                  </ImsStatusItem>
                  <ImsStatusItem label="基带型号">
                    <Typography variant="body2" fontFamily={'"Geist Mono", monospace'} sx={{ wordBreak: 'break-word' }}>
                      {imsStatus?.modem_model || '未知'}
                    </Typography>
                  </ImsStatusItem>
                  <ImsStatusItem label="运营商配置">
                    <Typography variant="body2" fontFamily={'"Geist Mono", monospace'} sx={{ wordBreak: 'break-word' }}>
                      {imsStatus?.carrier_config || '未知'}
                    </Typography>
                  </ImsStatusItem>
                </Box>
                <Alert severity={vowifiVoiceReady ? 'success' : 'warning'} sx={{ mt: 2 }}>
                  {vowifiVoiceReady
                    ? 'Host SWu / IMS 与 helper 呼叫控制均已就绪，可以发起 VoWiFi 呼叫；协商 G.711 后可使用 WAV/PCM 文件播放与录音。'
                    : `${vowifiUnavailableReason}。SMS over IMS 的就绪状态不会单独启用拨号。`}
                </Alert>
              </>
            ) : (
              <Alert severity="info">暂无 IMS 状态</Alert>
            )}
          </CardContent>
        </Card>
      )}

      <Dialog open={clearDialogOpen} onClose={() => setClearDialogOpen(false)}>
        <DialogTitle>确认清空</DialogTitle>
        <DialogContent>
          <Typography>确定要清空所有通话记录吗？此操作不可撤销。</Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setClearDialogOpen(false)}>取消</Button>
          <Button onClick={() => void handleClearHistory()} color="error" variant="contained">确认清空</Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}
