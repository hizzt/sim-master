import { Box, Card, CardContent, Chip, CircularProgress, Stack, Typography } from '@mui/material'
import Grid from '@mui/material/Grid'
import {
  CheckCircle,
  FlightTakeoff,
  SignalCellularAlt,
  TimerOutlined,
  WifiTethering,
} from '@mui/icons-material'
import { useRefreshInterval } from '@/contexts/RefreshContext'
import ErrorSnackbar from '@/components/ErrorSnackbar'
import { PageHeader, PageSection, StatusDot } from '@/components/PageFrame'
import { getCarrierLogo, formatCarrierName } from '@/utils/carriers'
import {
  QuickControls,
  SystemResources,
  SimCardInfo,
  DeviceInfoCard,
} from './components'
import { useDashboardData, type DashboardData } from './hooks/useDashboardData'
import { useModems } from '@/contexts/ModemContext'

function getNetworkTech(data: DashboardData) {
  if (data.cellsInfo?.serving_cell?.tech) return data.cellsInfo.serving_cell.tech.toUpperCase()
  const preference = data.networkInfo?.technology_preference?.toLowerCase()
  if (preference?.includes('nr')) return '5G'
  if (preference?.includes('lte')) return 'LTE'
  return 'N/A'
}

function getRegistrationLabel(status?: string) {
  if (status === 'registered') return '已注册'
  if (status === 'roaming') return '漫游'
  return status || '未知'
}

function latencyLabel(value?: number) {
  return typeof value === 'number' ? `${value.toFixed(0)}ms` : '-'
}

function StatusBar({ data }: { data: DashboardData }) {
  const signal = data.networkInfo?.signal_strength ?? 0
  const networkTech = getNetworkTech(data)
  const carrierLogo = getCarrierLogo(data.networkInfo?.mcc, data.networkInfo?.mnc)
  const carrierName = formatCarrierName(data.networkInfo?.mcc, data.networkInfo?.mnc)
  const isAirplaneMode = data.airplaneMode?.enabled ?? false
  const ipValueSx = {
    minWidth: 0,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    fontSize: '0.75rem',
  } as const
  const ipLabelSx = { ...ipValueSx, flexShrink: 0 } as const

  return (
    <Box
      component="section"
      sx={{
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: 2,
        pb: { xs: 2.5, sm: 3 },
        borderBottom: 1,
        borderColor: 'divider',
      }}
    >
      <Stack direction="row" spacing={{ xs: 1, md: 2 }} alignItems="center" flexWrap="wrap" useFlexGap>
        <Box display="flex" alignItems="center" gap={1}>
          <StatusDot active={Boolean(data.deviceInfo?.online || data.vowifiStatus?.running)} />
          <Typography variant="subtitle2" fontWeight={600}>
            {data.deviceInfo?.online ? '系统在线' : data.vowifiStatus?.running ? 'VoWiFi 在线（蜂窝离线）' : '系统离线'}
          </Typography>
        </Box>

        {!isAirplaneMode && (
          <>
            <Box display="flex" alignItems="center" gap={1}>
              {carrierLogo ? (
                <Box component="img" src={carrierLogo} alt={carrierName} sx={{ height: 24, maxWidth: 92, objectFit: 'contain' }} />
              ) : (
                <Chip label={carrierName} size="small" variant="outlined" />
              )}
              <Chip
                icon={<SignalCellularAlt />}
                label={`${signal}%`}
                color={signal > 70 ? 'success' : signal > 35 ? 'primary' : 'warning'}
                size="small"
                variant="outlined"
              />
            </Box>
            <Chip icon={<WifiTethering />} label={networkTech} color={networkTech === '5G' ? 'success' : 'primary'} size="small" />
            <Chip
              icon={<CheckCircle />}
              label={getRegistrationLabel(data.networkInfo?.registration_status)}
              color={data.networkInfo?.registration_status === 'registered' ? 'success' : 'default'}
              size="small"
              variant="outlined"
            />
          </>
        )}
        {isAirplaneMode && <Chip icon={<FlightTakeoff />} label="飞行模式" color="warning" size="small" />}
        <Chip
          icon={<WifiTethering />}
          label={data.vowifiStatus?.ims_registered ? 'VoWiFi 已驻网' : data.vowifiStatus?.running ? 'VoWiFi 连接中' : 'VoWiFi 未启用'}
          color={data.vowifiStatus?.ims_registered ? 'success' : 'default'}
          size="small"
        />
        <Typography variant="caption" color="text.secondary">
          运行 {data.systemStats?.uptime?.uptime_formatted || '-'}
        </Typography>
      </Stack>

      <Stack spacing={0.75} sx={{ minWidth: { xs: '100%', md: 360 }, ml: { md: 'auto' } }}>
        <Box display="flex" alignItems="center" justifyContent="flex-end" gap={1}>
          <Typography variant="body2" sx={ipLabelSx}>IPv4：</Typography>
          <Typography variant="body2" sx={ipValueSx}>
            {data.connectionAddresses.ipv4[0] || '-'}
          </Typography>
          <Box display="flex" alignItems="center" gap={0.35} color={data.connectivity?.ipv4?.success ? 'success.main' : 'text.disabled'}>
            <TimerOutlined sx={{ fontSize: 14 }} />
            <Typography variant="caption" sx={{ fontFamily: '"Geist Mono", monospace' }}>
              {latencyLabel(data.connectivity?.ipv4?.latency_ms)}
            </Typography>
          </Box>
        </Box>
        <Box display="flex" alignItems="center" justifyContent="flex-end" gap={1}>
          <Typography variant="body2" sx={ipLabelSx}>IPv6：</Typography>
          <Typography variant="body2" sx={ipValueSx}>
            {data.connectionAddresses.ipv6[0] || '-'}
          </Typography>
          <Box display="flex" alignItems="center" gap={0.35} color={data.connectivity?.ipv6?.success ? 'success.main' : 'text.disabled'}>
            <TimerOutlined sx={{ fontSize: 14 }} />
            <Typography variant="caption" sx={{ fontFamily: '"Geist Mono", monospace' }}>
              {latencyLabel(data.connectivity?.ipv6?.latency_ms)}
            </Typography>
          </Box>
        </Box>
      </Stack>
    </Box>
  )
}

export default function DashboardPage() {
  const { refreshInterval, refreshKey } = useRefreshInterval()
  const { initialLoading, error, setError, data, actions } = useDashboardData(refreshInterval, refreshKey)
  const { modems, selectedModemId } = useModems()

  if (initialLoading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="60vh">
        <CircularProgress />
      </Box>
    )
  }

  return (
    <Box>
      <ErrorSnackbar error={error} onClose={() => setError(null)} />

      <PageHeader title="设备概览" />

      <Stack spacing={{ xs: 3, sm: 4 }}>
        <StatusBar data={data} />

        <Stack spacing={2}>
          {(modems.length > 0 ? modems : [{ id: selectedModemId || 'device-a', path_id: 'A', manufacturer: data.deviceInfo?.manufacturer || '', model: data.deviceInfo?.model || '', primary_port: '', state: data.deviceInfo?.online ? '在线' : '离线' }]).map((modem, index) => {
            const selected = modems.length === 0 || modem.id === selectedModemId
            return (
              <Box key={modem.id}>
                <Typography variant="subtitle1" fontWeight={700} sx={{ mb: 1 }}>设备{String.fromCharCode(65 + index)}</Typography>
                <Grid container spacing={2}>
                  <Grid size={{ xs: 12, md: 4 }}>
                    <PageSection>
                      {selected ? (
                        <DeviceInfoCard deviceInfo={data.deviceInfo} systemStats={data.systemStats} />
                      ) : (
                        <Card sx={{ height: '100%' }}><CardContent><Typography variant="subtitle2" fontWeight={700}>设备信息</Typography><Typography variant="body2" mt={1}>{modem.manufacturer} {modem.model}</Typography><Typography variant="caption" color="text.secondary">{modem.primary_port || modem.path_id} · {modem.state}</Typography></CardContent></Card>
                      )}
                    </PageSection>
                  </Grid>
                  <Grid size={{ xs: 12, md: 4 }}>
                    <PageSection>
                      <QuickControls
                        dataStatus={selected ? data.dataStatus : false}
                        airplaneMode={selected ? data.airplaneMode : null}
                        roaming={selected ? data.roaming : null}
                        vowifiStatus={selected ? data.vowifiStatus : null}
                        onToggleData={() => void actions.toggleData()}
                        onToggleAirplaneMode={() => void actions.toggleAirplaneMode()}
                        onToggleRoaming={() => void actions.toggleRoaming()}
                        onToggleVowifi={() => void actions.toggleVowifi()}
                        disabled={!selected}
                      />
                    </PageSection>
                  </Grid>
                  <Grid size={{ xs: 12, md: 4 }}>
                    <PageSection>
                      {selected ? <SimCardInfo simInfo={data.simInfo} onRefresh={() => void actions.loadData()} /> : <Card sx={{ height: '100%' }}><CardContent><Typography variant="subtitle2" fontWeight={700}>SIM 卡信息</Typography><Typography variant="body2" mt={1}>请在顶部选择此模块后查看完整 SIM 信息</Typography></CardContent></Card>}
                    </PageSection>
                  </Grid>
                </Grid>
              </Box>
            )
          })}
        </Stack>

        <Grid container spacing={2}>
          <Grid size={12}><PageSection><SystemResources systemStats={data.systemStats} /></PageSection></Grid>
        </Grid>
      </Stack>
    </Box>
  )
}
