import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import {
  Alert,
  AppBar,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControl,
  IconButton,
  ListItemIcon,
  ListItemText,
  Menu,
  MenuItem,
  Select,
  Snackbar,
  Stack,
  Toolbar,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  Menu as MenuIcon,
  MoreHoriz as MoreIcon,
  PowerSettingsNew as RebootIcon,
  Refresh as RefreshIcon,
  RestartAlt as RestartIcon,
  Router as RouterIcon,
  Speed as SpeedIcon,
} from '@mui/icons-material'
import { useRefreshInterval } from '../../contexts/RefreshContext'
import { useModems } from '../../contexts/ModemContext'
import { api } from '../../api/current'
import type { BasebandRestartResponse, BasebandRestartStep } from '../../api/types'

type RestartConfirmTarget = 'baseband' | 'service' | 'device'

interface TopBarProps {
  onMenuClick: () => void
  refreshInterval: number
  onRefreshIntervalChange: (interval: number) => void
}

const pageTitles: Array<[string, string]> = [
  ['/config/security', '安全性'],
  ['/config/backup', '备份与恢复'],
  ['/device-network', '设备网络'],
  ['/modems', '模块管理'],
  ['/notifications', '通知'],
  ['/automation', '自动化'],
  ['/network', '蜂窝网络'],
  ['/vowifi', 'VoWiFi'],
  ['/proxy', '网络代理'],
  ['/config', '基本配置'],
  ['/sim', 'SIM 卡'],
  ['/sms', '短信'],
  ['/phone', '电话'],
  ['/ota', 'OTA 更新'],
  ['/', '概览'],
]

export default function TopBar({ onMenuClick, refreshInterval, onRefreshIntervalChange }: TopBarProps) {
  const location = useLocation()
  const { triggerRefresh } = useRefreshInterval()
  const { modems, selectedModemId, selectedModem, selectModem } = useModems()
  const [systemMenuAnchor, setSystemMenuAnchor] = useState<HTMLElement | null>(null)
  const [refreshMenuAnchor, setRefreshMenuAnchor] = useState<HTMLElement | null>(null)
  const [basebandRestarting, setBasebandRestarting] = useState(false)
  const [basebandProgressOpen, setBasebandProgressOpen] = useState(false)
  const [basebandSteps, setBasebandSteps] = useState<BasebandRestartStep[]>([])
  const [basebandCurrentRegistration, setBasebandCurrentRegistration] = useState<string | null>(null)
  const [systemActionLoading, setSystemActionLoading] = useState<'service' | 'device' | null>(null)
  const [systemActionMessage, setSystemActionMessage] = useState<string | null>(null)
  const [systemActionSeverity, setSystemActionSeverity] = useState<'info' | 'success' | 'error'>('info')
  const [deviceRebootProgressOpen, setDeviceRebootProgressOpen] = useState(false)
  const [deviceRebootSteps, setDeviceRebootSteps] = useState<BasebandRestartStep[]>([])
  const [restartConfirmTarget, setRestartConfirmTarget] = useState<RestartConfirmTarget | null>(null)
  const deviceRebootTimersRef = useRef<number[]>([])
  const title = pageTitles.find(([path]) => path === '/' ? location.pathname === '/' : location.pathname.startsWith(path))?.[1] ?? 'SimAdmin'

  useEffect(() => () => {
    deviceRebootTimersRef.current.forEach((timer) => window.clearTimeout(timer))
    deviceRebootTimersRef.current = []
  }, [])

  const applyBasebandProgress = (data?: BasebandRestartResponse) => {
    if (!data) return
    setBasebandSteps(data.steps ?? [])
    setBasebandCurrentRegistration(data.current_registration ?? null)
  }

  const getBasebandErrorStep = () => basebandSteps.find((step) => step.status === 'error')
  const getCurrentBasebandMessage = () => {
    const errorStep = getBasebandErrorStep()
    if (errorStep) return errorStep.detail || `${errorStep.step}失败`
    if (!basebandRestarting && basebandSteps.length > 0) return '基带与网络连接已恢复'
    if (basebandSteps.length === 0) return '正在启动基带重启程序'
    const lastStep = basebandSteps[basebandSteps.length - 1]
    return lastStep.status === 'running' ? `正在进行：${lastStep.step}` : `已完成：${lastStep.step}`
  }

  const getDeviceRebootErrorStep = () => deviceRebootSteps.find((step) => step.status === 'error')
  const getCurrentDeviceRebootMessage = () => {
    const errorStep = getDeviceRebootErrorStep()
    if (errorStep) return errorStep.detail || `${errorStep.step}失败`
    if (systemActionLoading !== 'device' && deviceRebootSteps.length > 0) return '设备已请求重启，即将离线'
    const activeStep = deviceRebootSteps.find((step) => step.status === 'running')
    return activeStep ? `${activeStep.step}：${activeStep.detail}` : '正在执行系统重启'
  }

  const loadBasebandProgress = async () => {
    const response = await api.getBasebandRestartStatus()
    applyBasebandProgress(response.data)
  }

  const handleRestartBaseband = async () => {
    if (basebandRestarting) return
    setBasebandRestarting(true)
    setBasebandProgressOpen(true)
    setBasebandSteps([])
    setBasebandCurrentRegistration(null)
    let progressTimer: number | undefined
    try {
      progressTimer = window.setInterval(() => void loadBasebandProgress(), 1000)
      const response = await api.restartBaseband()
      applyBasebandProgress(response.data)
      triggerRefresh()
    } catch (error) {
      await loadBasebandProgress().catch(() => undefined)
      setBasebandSteps((steps) => steps.some((step) => step.status === 'error')
        ? steps
        : [...steps, { step: '重启基带', status: 'error', detail: error instanceof Error ? error.message : '未知错误' }])
    } finally {
      if (progressTimer) window.clearInterval(progressTimer)
      await loadBasebandProgress().catch(() => undefined)
      setBasebandRestarting(false)
    }
  }

  const handleRestartService = async () => {
    if (systemActionLoading) return
    setSystemActionLoading('service')
    setSystemActionSeverity('info')
    setSystemActionMessage('正在重启 SimAdmin 服务')
    try {
      await api.restartService()
      setSystemActionSeverity('success')
      setSystemActionMessage('SimAdmin 服务正在重启')
    } catch (error) {
      setSystemActionSeverity('error')
      setSystemActionMessage(error instanceof Error ? error.message : '重启服务失败')
    } finally {
      setSystemActionLoading(null)
    }
  }

  const handleRebootDevice = async () => {
    if (systemActionLoading) return
    setSystemActionLoading('device')
    setDeviceRebootProgressOpen(true)
    setDeviceRebootSteps([
      { step: '提交安全重启请求', status: 'running', detail: '等待后端接管重启序列' },
      { step: '关闭射频模块', status: 'running', detail: '等待射频进入低功耗状态' },
      { step: '停止 ModemManager', status: 'running', detail: '释放通信链路' },
      { step: '同步文件系统', status: 'running', detail: '等待写入完成' },
      { step: '执行系统重启', status: 'running', detail: '设备即将离线' },
    ])
    deviceRebootTimersRef.current.forEach((timer) => window.clearTimeout(timer))
    deviceRebootTimersRef.current = []
    try {
      await api.rebootSystem(1)
      const scheduleStep = (index: number, status: BasebandRestartStep['status'], detail: string, delay: number) => {
        const timer = window.setTimeout(() => {
          setDeviceRebootSteps((steps) => steps.map((step, stepIndex) => stepIndex === index ? { ...step, status, detail } : step))
        }, delay)
        deviceRebootTimersRef.current.push(timer)
      }
      scheduleStep(0, 'ok', '后端已接收请求', 0)
      scheduleStep(1, 'ok', '射频关闭命令已下发', 900)
      scheduleStep(2, 'ok', 'ModemManager 停止命令已下发', 1600)
      scheduleStep(3, 'ok', '文件系统同步完成', 2600)
      scheduleStep(4, 'ok', '重启命令已下发', 4200)
      const doneTimer = window.setTimeout(() => setSystemActionLoading(null), 5000)
      deviceRebootTimersRef.current.push(doneTimer)
    } catch (error) {
      setDeviceRebootSteps((steps) => steps.map((step, index) => index === 0
        ? { ...step, status: 'error', detail: error instanceof Error ? error.message : '重启设备失败' }
        : step))
      setSystemActionLoading(null)
    }
  }

  const restartCopy = {
    baseband: ['确认重启基带', '网络注册和数据连接会短暂中断。'],
    service: ['确认重启服务', '管理页面会短暂不可用。'],
    device: ['确认重启设备', '设备会离线并中断当前连接。'],
  } as const

  const confirmRestart = () => {
    const target = restartConfirmTarget
    setRestartConfirmTarget(null)
    if (target === 'baseband') void handleRestartBaseband()
    if (target === 'service') void handleRestartService()
    if (target === 'device') void handleRebootDevice()
  }

  const openRestartConfirmation = (target: RestartConfirmTarget) => {
    setSystemMenuAnchor(null)
    setRestartConfirmTarget(target)
  }

  return (
    <AppBar position="static" sx={{ flexShrink: 0, borderBottom: 1, borderColor: 'divider' }}>
      <Toolbar sx={{ minHeight: '48px !important', px: { xs: 1, sm: 1.5 } }}>
        <IconButton aria-label="切换侧边栏" onClick={onMenuClick} size="small" sx={{ mr: 1 }}>
          <MenuIcon fontSize="small" />
        </IconButton>
        <Typography variant="subtitle2" component="div" noWrap sx={{ flex: 1, fontWeight: 600 }}>
          {title}
        </Typography>
        {modems.length > 0 && (
          <Tooltip title={selectedModem ? `${selectedModem.manufacturer} ${selectedModem.model}`.trim() : '选择模块'}>
            <FormControl size="small" sx={{ mr: 0.75, minWidth: { xs: 104, sm: 148 }, maxWidth: { xs: 132, sm: 220 } }}>
              <Select
                value={selectedModemId}
                onChange={(event) => selectModem(event.target.value)}
                inputProps={{ 'aria-label': '当前模块' }}
                renderValue={(value) => {
                  const modem = modems.find((item) => item.id === value)
                  return modem ? `模块 ${modem.path_id}${modem.network_interface ? ` · ${modem.network_interface}` : ''}` : '选择模块'
                }}
                sx={{ height: 32, fontSize: 13, '& .MuiSelect-select': { py: 0.5 } }}
              >
                {modems.map((modem) => (
                  <MenuItem key={modem.id} value={modem.id}>
                    模块 {modem.path_id} · {modem.network_interface || modem.primary_port || '无端口'}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          </Tooltip>
        )}
        <Stack direction="row" spacing={0.25} alignItems="center">
          <Tooltip title="立即刷新">
            <IconButton aria-label="立即刷新" onClick={triggerRefresh} size="small">
              <RefreshIcon fontSize="small" />
            </IconButton>
          </Tooltip>
          <Tooltip title={`自动刷新：${refreshInterval === 0 ? '手动' : `${refreshInterval / 1000} 秒`}`}>
            <IconButton aria-label="设置自动刷新" onClick={(event) => setRefreshMenuAnchor(event.currentTarget)} size="small">
              <SpeedIcon fontSize="small" />
            </IconButton>
          </Tooltip>
          <Tooltip title="系统操作">
            <IconButton aria-label="系统操作" onClick={(event) => setSystemMenuAnchor(event.currentTarget)} size="small">
              <MoreIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Stack>
      </Toolbar>

      <Menu anchorEl={refreshMenuAnchor} open={Boolean(refreshMenuAnchor)} onClose={() => setRefreshMenuAnchor(null)}>
        {[1000, 3000, 5000, 10000].map((interval) => (
          <MenuItem key={interval} selected={refreshInterval === interval} onClick={() => {
            onRefreshIntervalChange(interval)
            setRefreshMenuAnchor(null)
          }}>
            每 {interval / 1000} 秒刷新
          </MenuItem>
        ))}
        <Divider />
        <MenuItem selected={refreshInterval === 0} onClick={() => {
          onRefreshIntervalChange(0)
          setRefreshMenuAnchor(null)
        }}>
          手动刷新
        </MenuItem>
      </Menu>

      <Menu anchorEl={systemMenuAnchor} open={Boolean(systemMenuAnchor)} onClose={() => setSystemMenuAnchor(null)}>
        <MenuItem onClick={() => openRestartConfirmation('baseband')} disabled={basebandRestarting || systemActionLoading !== null}>
          <ListItemIcon><RouterIcon fontSize="small" /></ListItemIcon>
          <ListItemText>重启基带</ListItemText>
        </MenuItem>
        <MenuItem onClick={() => openRestartConfirmation('service')} disabled={basebandRestarting || systemActionLoading !== null}>
          <ListItemIcon><RestartIcon fontSize="small" /></ListItemIcon>
          <ListItemText>重启服务</ListItemText>
        </MenuItem>
        <Divider />
        <MenuItem onClick={() => openRestartConfirmation('device')} disabled={basebandRestarting || systemActionLoading !== null}>
          <ListItemIcon><RebootIcon fontSize="small" color="error" /></ListItemIcon>
          <ListItemText slotProps={{ primary: { color: 'error.main' } }}>重启设备</ListItemText>
        </MenuItem>
      </Menu>

      <Dialog open={basebandProgressOpen} onClose={() => { if (!basebandRestarting) setBasebandProgressOpen(false) }} maxWidth="xs" fullWidth>
        <DialogTitle>重启基带</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 1 }}>
            {basebandRestarting && !getBasebandErrorStep() && <CircularProgress size={24} />}
            <Alert severity={getBasebandErrorStep() ? 'error' : !basebandRestarting && basebandSteps.length > 0 ? 'success' : 'info'}>
              {getCurrentBasebandMessage()}
            </Alert>
            {basebandCurrentRegistration && basebandRestarting && (
              <Typography variant="caption" color="text.secondary">当前注册状态：{basebandCurrentRegistration}</Typography>
            )}
          </Stack>
        </DialogContent>
        <DialogActions><Button disabled={basebandRestarting} onClick={() => setBasebandProgressOpen(false)}>关闭</Button></DialogActions>
      </Dialog>

      <Dialog open={deviceRebootProgressOpen} onClose={() => { if (systemActionLoading !== 'device') setDeviceRebootProgressOpen(false) }} maxWidth="xs" fullWidth>
        <DialogTitle>重启设备</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 1 }}>
            {systemActionLoading === 'device' && !getDeviceRebootErrorStep() && <CircularProgress size={24} />}
            <Alert severity={getDeviceRebootErrorStep() ? 'error' : systemActionLoading !== 'device' && deviceRebootSteps.length > 0 ? 'success' : 'info'}>
              {getCurrentDeviceRebootMessage()}
            </Alert>
          </Stack>
        </DialogContent>
        <DialogActions><Button disabled={systemActionLoading === 'device'} onClick={() => setDeviceRebootProgressOpen(false)}>关闭</Button></DialogActions>
      </Dialog>

      <Dialog open={Boolean(restartConfirmTarget)} onClose={() => setRestartConfirmTarget(null)} maxWidth="xs" fullWidth>
        <DialogTitle>{restartConfirmTarget ? restartCopy[restartConfirmTarget][0] : ''}</DialogTitle>
        <DialogContent><Typography color="text.secondary">{restartConfirmTarget ? restartCopy[restartConfirmTarget][1] : ''}</Typography></DialogContent>
        <DialogActions>
          <Button onClick={() => setRestartConfirmTarget(null)}>取消</Button>
          <Button onClick={confirmRestart} color="error" variant="contained">确认重启</Button>
        </DialogActions>
      </Dialog>

      <Snackbar
        open={Boolean(systemActionMessage)}
        autoHideDuration={systemActionLoading ? null : 3000}
        onClose={() => { if (!systemActionLoading) setSystemActionMessage(null) }}
        anchorOrigin={{ vertical: 'top', horizontal: 'center' }}
      >
        <Alert
          severity={systemActionSeverity}
          icon={systemActionLoading ? <CircularProgress size={16} color="inherit" /> : undefined}
          onClose={systemActionLoading ? undefined : () => setSystemActionMessage(null)}
        >
          {systemActionMessage}
        </Alert>
      </Snackbar>
    </AppBar>
  )
}
