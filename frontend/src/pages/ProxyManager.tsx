import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  IconButton,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  Add as AddIcon,
  DeleteOutline as DeleteIcon,
  EditOutlined as EditIcon,
  PlayArrow as StartIcon,
  Refresh as RefreshIcon,
  Stop as StopIcon,
} from '@mui/icons-material'
import { api, type ProxyProtocol, type ProxyStatus, type ProxyUpsertRequest } from '../api/current'
import { PageHeader, StatusDot } from '../components/PageFrame'
import { useModems } from '../contexts/ModemContext'

interface FormState {
  name: string
  protocol: ProxyProtocol
  listen_host: string
  listen_port: string
  modem_id: string
  username: string
  password: string
  enabled: boolean
}

function emptyForm(modemId: string): FormState {
  return {
    name: '',
    protocol: 'socks5',
    listen_host: '127.0.0.1',
    listen_port: '1080',
    modem_id: modemId,
    username: '',
    password: '',
    enabled: false,
  }
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB`
  return `${(value / 1024 ** 3).toFixed(1)} GiB`
}

export default function ProxyManager() {
  const { modems, selectedModemId } = useModems()
  const [proxies, setProxies] = useState<ProxyStatus[]>([])
  const [loading, setLoading] = useState(true)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<ProxyStatus | null>(null)
  const [form, setForm] = useState<FormState>(() => emptyForm(selectedModemId))

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true)
    try {
      const response = await api.getProxies()
      setProxies(response.data?.proxies ?? [])
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '读取代理实例失败')
    } finally {
      if (!quiet) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = window.setInterval(() => void load(true), 3000)
    return () => window.clearInterval(timer)
  }, [load])

  const modemLabels = useMemo(
    () => new Map(modems.map((modem) => [modem.id, `模块 ${modem.path_id} · ${modem.network_interface || modem.primary_port || '无端口'}`])),
    [modems],
  )

  const openCreate = () => {
    setEditing(null)
    setForm(emptyForm(selectedModemId || modems[0]?.id || ''))
    setDialogOpen(true)
  }

  const openEdit = (proxy: ProxyStatus) => {
    setEditing(proxy)
    setForm({
      name: proxy.name,
      protocol: proxy.protocol,
      listen_host: proxy.listen_host,
      listen_port: String(proxy.listen_port),
      modem_id: proxy.modem_id,
      username: proxy.username,
      password: '',
      enabled: proxy.enabled,
    })
    setDialogOpen(true)
  }

  const save = async () => {
    const port = Number(form.listen_port)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      setError('监听端口必须是 1 到 65535 之间的整数')
      return
    }
    const payload: ProxyUpsertRequest = {
      name: form.name,
      protocol: form.protocol,
      listen_host: form.listen_host,
      listen_port: port,
      modem_id: form.modem_id,
      username: form.username || undefined,
      password: form.password || undefined,
      enabled: form.enabled,
    }
    setBusyId(editing?.id ?? 'create')
    try {
      if (editing) await api.updateProxy(editing.id, payload)
      else await api.createProxy(payload)
      setDialogOpen(false)
      await load(true)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '保存代理实例失败')
    } finally {
      setBusyId(null)
    }
  }

  const toggleRuntime = async (proxy: ProxyStatus) => {
    setBusyId(proxy.id)
    try {
      if (proxy.running) await api.stopProxy(proxy.id)
      else await api.startProxy(proxy.id)
      await load(true)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '代理操作失败')
    } finally {
      setBusyId(null)
    }
  }

  const remove = async (proxy: ProxyStatus) => {
    if (!window.confirm(`删除代理“${proxy.name}”？`)) return
    setBusyId(proxy.id)
    try {
      await api.deleteProxy(proxy.id)
      await load(true)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '删除代理失败')
    } finally {
      setBusyId(null)
    }
  }

  return (
    <Box>
      <PageHeader
        title="网络代理"
        meta={<Typography variant="body2" color="text.secondary">{proxies.length} 个实例，{proxies.filter((proxy) => proxy.running).length} 个运行中</Typography>}
        actions={(
          <>
            <Button variant="outlined" startIcon={<RefreshIcon />} onClick={() => void load()} disabled={loading}>刷新</Button>
            <Button variant="contained" startIcon={<AddIcon />} onClick={openCreate} disabled={modems.length === 0}>新增代理</Button>
          </>
        )}
      />

      {error && <Alert severity="error" onClose={() => setError(null)} sx={{ mb: 2 }}>{error}</Alert>}

      <TableContainer component={Paper} variant="outlined" sx={{ borderRadius: 1 }}>
        <Table size="small" sx={{ minWidth: 980 }}>
          <TableHead>
            <TableRow>
              <TableCell>实例</TableCell>
              <TableCell>监听</TableCell>
              <TableCell>出口模块</TableCell>
              <TableCell>认证</TableCell>
              <TableCell align="right">连接</TableCell>
              <TableCell align="right">上传</TableCell>
              <TableCell align="right">下载</TableCell>
              <TableCell align="right">操作</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {loading && proxies.length === 0 ? (
              <TableRow><TableCell colSpan={8} align="center" sx={{ py: 6 }}><CircularProgress size={28} /></TableCell></TableRow>
            ) : proxies.length === 0 ? (
              <TableRow><TableCell colSpan={8} align="center" sx={{ py: 6, color: 'text.secondary' }}>暂无代理实例</TableCell></TableRow>
            ) : proxies.map((proxy) => (
              <TableRow key={proxy.id} hover>
                <TableCell>
                  <Stack direction="row" spacing={1} alignItems="center">
                    <StatusDot active={proxy.running} />
                    <Box>
                      <Typography variant="body2" fontWeight={600}>{proxy.name}</Typography>
                      <Chip size="small" variant="outlined" label={proxy.protocol.toUpperCase()} sx={{ mt: 0.5, height: 20 }} />
                    </Box>
                  </Stack>
                  {proxy.last_error && <Typography variant="caption" color="error" display="block" sx={{ mt: 0.5, maxWidth: 220 }}>{proxy.last_error}</Typography>}
                </TableCell>
                <TableCell sx={{ fontFamily: '"Geist Mono", monospace' }}>{proxy.listen_host}:{proxy.listen_port}</TableCell>
                <TableCell>
                  <Typography variant="body2">{modemLabels.get(proxy.modem_id) ?? proxy.modem_id}</Typography>
                  <Typography variant="caption" color="text.secondary">{proxy.network_interface || '未启动'}</Typography>
                </TableCell>
                <TableCell>{proxy.username && proxy.has_password ? proxy.username : '无'}</TableCell>
                <TableCell align="right">{proxy.active_connections} / {proxy.total_connections}</TableCell>
                <TableCell align="right">{formatBytes(proxy.bytes_uploaded)}</TableCell>
                <TableCell align="right">{formatBytes(proxy.bytes_downloaded)}</TableCell>
                <TableCell align="right">
                  <Tooltip title={proxy.running ? '停止' : '启动'}>
                    <span>
                      <IconButton size="small" color={proxy.running ? 'warning' : 'success'} onClick={() => void toggleRuntime(proxy)} disabled={busyId === proxy.id}>
                        {busyId === proxy.id ? <CircularProgress size={18} /> : proxy.running ? <StopIcon fontSize="small" /> : <StartIcon fontSize="small" />}
                      </IconButton>
                    </span>
                  </Tooltip>
                  <Tooltip title="编辑"><IconButton size="small" onClick={() => openEdit(proxy)} disabled={busyId === proxy.id}><EditIcon fontSize="small" /></IconButton></Tooltip>
                  <Tooltip title="删除"><IconButton size="small" color="error" onClick={() => void remove(proxy)} disabled={busyId === proxy.id}><DeleteIcon fontSize="small" /></IconButton></Tooltip>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>

      <Dialog open={dialogOpen} onClose={() => !busyId && setDialogOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>{editing ? '编辑代理' : '新增代理'}</DialogTitle>
        <DialogContent>
          <Stack spacing={2.25} sx={{ pt: 1 }}>
            <TextField label="名称" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} fullWidth />
            <ToggleButtonGroup
              exclusive
              fullWidth
              size="small"
              value={form.protocol}
              onChange={(_, value: ProxyProtocol | null) => {
                if (!value) return
                setForm({ ...form, protocol: value, listen_port: form.listen_port === '1080' || form.listen_port === '8080' ? (value === 'socks5' ? '1080' : '8080') : form.listen_port })
              }}
            >
              <ToggleButton value="socks5">SOCKS5</ToggleButton>
              <ToggleButton value="http">HTTP / HTTPS</ToggleButton>
            </ToggleButtonGroup>
            <FormControl fullWidth>
              <InputLabel id="proxy-modem-label">出口模块</InputLabel>
              <Select labelId="proxy-modem-label" label="出口模块" value={form.modem_id} onChange={(event) => setForm({ ...form, modem_id: event.target.value })}>
                {modems.map((modem) => <MenuItem key={modem.id} value={modem.id}>{modemLabels.get(modem.id)}</MenuItem>)}
              </Select>
            </FormControl>
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
              <FormControl fullWidth>
                <InputLabel id="proxy-listen-label">监听地址</InputLabel>
                <Select labelId="proxy-listen-label" label="监听地址" value={form.listen_host} onChange={(event) => setForm({ ...form, listen_host: event.target.value })}>
                  <MenuItem value="127.0.0.1">127.0.0.1</MenuItem>
                  <MenuItem value="::1">::1</MenuItem>
                  <MenuItem value="0.0.0.0">0.0.0.0</MenuItem>
                  <MenuItem value="::">::</MenuItem>
                </Select>
              </FormControl>
              <TextField label="端口" type="number" value={form.listen_port} onChange={(event) => setForm({ ...form, listen_port: event.target.value })} fullWidth inputProps={{ min: 1, max: 65535 }} />
            </Stack>
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
              <TextField label="用户名" value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} fullWidth autoComplete="off" />
              <TextField label="密码" type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} fullWidth autoComplete="new-password" />
            </Stack>
            <Stack direction="row" alignItems="center" justifyContent="space-between">
              <Typography variant="body2">保存后启动</Typography>
              <Switch checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} />
            </Stack>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDialogOpen(false)} disabled={busyId !== null}>取消</Button>
          <Button variant="contained" onClick={() => void save()} disabled={busyId !== null || !form.name.trim() || !form.modem_id}>
            {busyId ? <CircularProgress size={20} /> : '保存'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}
