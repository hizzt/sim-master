import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Paper,
  Stack,
  Typography,
} from '@mui/material'
import {
  CheckCircle as SelectedIcon,
  Memory as ModemIcon,
  Refresh as RefreshIcon,
  Tune as SelectIcon,
} from '@mui/icons-material'
import { PageHeader, StatusDot } from '../components/PageFrame'
import { useModems } from '../contexts/ModemContext'
import type { ModemSummary } from '../api/current'

const stateLabels: Record<string, string> = {
  failed: '故障',
  unknown: '未知',
  initializing: '初始化',
  locked: 'SIM 已锁定',
  disabled: '已禁用',
  disabling: '正在禁用',
  enabling: '正在启用',
  enabled: '已启用',
  searching: '搜网中',
  registered: '已注册',
  disconnecting: '正在断开',
  connecting: '正在连接',
  connected: '已连接',
}

function Detail({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <Box sx={{ minWidth: 0 }}>
      <Typography variant="caption" color="text.secondary">{label}</Typography>
      <Typography
        variant="body2"
        sx={{ mt: 0.25, wordBreak: 'break-word', fontFamily: mono ? '"Geist Mono", monospace' : undefined }}
      >
        {value || '未知'}
      </Typography>
    </Box>
  )
}

function ModemPanel({ modem }: { modem: ModemSummary }) {
  const { selectedModemId, selectModem } = useModems()
  const selected = modem.id === selectedModemId

  return (
    <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, borderRadius: 1, minWidth: 0 }}>
      <Stack direction="row" alignItems="flex-start" justifyContent="space-between" spacing={2}>
        <Stack direction="row" spacing={1.25} alignItems="center" minWidth={0}>
          <ModemIcon color={modem.enabled ? 'primary' : 'disabled'} />
          <Box minWidth={0}>
            <Typography variant="subtitle1" fontWeight={600} noWrap>模块 {modem.path_id}</Typography>
            <Stack direction="row" spacing={0.75} alignItems="center">
              <StatusDot active={modem.connected || modem.enabled} />
              <Typography variant="caption" color="text.secondary">
                {stateLabels[modem.state] ?? modem.state}
              </Typography>
            </Stack>
          </Box>
        </Stack>
        {selected && <Chip size="small" color="primary" icon={<SelectedIcon />} label="当前模块" />}
      </Stack>

      <Box
        sx={{
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' },
          gap: 2,
          mt: 2.5,
        }}
      >
        <Detail label="型号" value={`${modem.manufacturer} ${modem.model}`.trim()} />
        <Detail label="固件" value={modem.revision} mono />
        <Detail label="设备标识" value={modem.equipment_identifier} mono />
        <Detail label="数据接口" value={modem.network_interface} mono />
        <Detail label="控制端口" value={modem.primary_port} mono />
        <Detail label="运营商" value={modem.operator_name} />
        <Detail label="信号" value={`${modem.signal_quality}%`} />
        <Detail label="SIM" value={modem.sim_present ? '已插入' : '未检测到'} />
      </Box>

      <Box sx={{ display: 'flex', justifyContent: 'flex-end', mt: 2.5 }}>
        <Button
          variant={selected ? 'outlined' : 'contained'}
          startIcon={selected ? <SelectedIcon /> : <SelectIcon />}
          disabled={selected}
          onClick={() => selectModem(modem.id)}
        >
          {selected ? '正在管理' : '管理此模块'}
        </Button>
      </Box>
    </Paper>
  )
}

export default function Modems() {
  const { modems, loading, error, refreshModems } = useModems()

  return (
    <Box>
      <PageHeader
        title="模块管理"
        meta={<Typography variant="body2" color="text.secondary">已发现 {modems.length} 个蜂窝模块</Typography>}
        actions={(
          <Button variant="outlined" startIcon={<RefreshIcon />} onClick={() => void refreshModems()} disabled={loading}>
            刷新
          </Button>
        )}
      />

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
      {loading && modems.length === 0 ? (
        <Box display="flex" justifyContent="center" py={8}><CircularProgress /></Box>
      ) : modems.length === 0 ? (
        <Alert severity="info">未发现 ModemManager 蜂窝模块</Alert>
      ) : (
        <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'repeat(2, minmax(0, 1fr))' }, gap: 2 }}>
          {modems.map((modem) => <ModemPanel key={modem.id} modem={modem} />)}
        </Box>
      )}
    </Box>
  )
}
