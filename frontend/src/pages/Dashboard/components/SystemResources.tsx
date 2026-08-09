import { Box, Card, CardContent, LinearProgress, Typography } from '@mui/material'
import Grid from '@mui/material/Grid'
import { DeveloperBoardOutlined, Memory, Speed as CpuIcon, Storage, Thermostat } from '@mui/icons-material'
import { formatBytes, getCpuColor, getMemoryColor, getTempPercent, getTempBarColor } from '../utils'
import type { SystemStatsResponse, ThermalZone } from '@/api/types'

interface SystemResourcesProps {
  systemStats: SystemStatsResponse | null
}

function getTempDotColor(sensor: ThermalZone | null) {
  if (!sensor) return 'text.disabled'
  return getTempBarColor(sensor.temperature)
}

function getFriendlyTemperatureLabel(sensor: ThermalZone) {
  if (sensor.label?.trim()) return sensor.label.trim()
  const rawType = sensor.type.trim()
  const source = rawType || sensor.zone || 'unknown'
  const normalized = source.toLowerCase().replace(/_/g, '-')

  if (/(modem|baseband|wwan|qmi|mhi)/.test(normalized)) return '基带'
  if (/(gpu|adreno)/.test(normalized)) return 'GPU'
  if (/(camera|cam|isp)/.test(normalized)) return '摄像头'
  if (/(wifi|wlan)/.test(normalized)) return 'Wi-Fi'
  if (/(battery|batt)/.test(normalized)) return '电池'
  if (/(charger|charge)/.test(normalized)) return '充电'
  if (/(pmic|power)/.test(normalized)) return '电源管理'
  if (/(soc|tsens)/.test(normalized)) return 'SoC'
  if (/(skin|shell|case)/.test(normalized)) return '外壳'
  if (/(ambient|board)/.test(normalized)) return '环境'

  const cpuRange = normalized.match(/cpu[^0-9]*(\d+)(?:[^0-9]+(\d+))?/)
  if (cpuRange) {
    return cpuRange[2] ? `CPU ${cpuRange[1]}-${cpuRange[2]}` : `CPU ${cpuRange[1]}`
  }
  if (normalized.includes('cpu')) return 'CPU'

  const coreRange = normalized.match(/core[^0-9]*(\d+)(?:[^0-9]+(\d+))?/)
  if (coreRange) {
    return coreRange[2] ? `核心 ${coreRange[1]}-${coreRange[2]}` : `核心 ${coreRange[1]}`
  }
  if (normalized.includes('core')) return '核心'

  const cleaned = source
    .replace(/[-_\s]*(thermal|therm|temperature|temp|sensor|zone)[-_\s]*/gi, ' ')
    .trim()

  return cleaned || source
}

function formatTemperatureSource(sensor: ThermalZone | null) {
  if (!sensor) return '-'
  return `${getFriendlyTemperatureLabel(sensor)}: ${sensor.temperature.toFixed(1)}°`
}

export function SystemResources({ systemStats }: SystemResourcesProps) {
  const rootDisk = systemStats?.disk?.find((disk) => disk.mount_point === '/') ?? systemStats?.disk?.[0]
  const sortedTemperatureSensors = [...(systemStats?.temperature ?? [])]
    .filter((sensor) => Number.isFinite(sensor.temperature))
    .sort((a, b) => b.temperature - a.temperature)
  const hottestSensor = sortedTemperatureSensors[0] ?? null
  const coldestSensor = sortedTemperatureSensors.length > 0 ? sortedTemperatureSensors[sortedTemperatureSensors.length - 1] : null
  const hottestPercent = hottestSensor ? getTempPercent(hottestSensor.temperature) : 0
  const hottestColor = hottestSensor ? getTempBarColor(hottestSensor.temperature) : getTempBarColor(0)
  const progressSx = {
    height: 5,
    borderRadius: 0,
  }
  const temperatureProgressSx = {
    ...progressSx,
    '& .MuiLinearProgress-bar': {
      bgcolor: hottestSensor ? hottestColor : 'text.disabled',
      borderRadius: 0,
    },
  }

  return (
    <Card sx={{ height: '100%' }}>
      <CardContent>
        <Box display="flex" alignItems="center" justifyContent="space-between" mb={2}>
          <Box display="flex" alignItems="center" gap={1}>
            <DeveloperBoardOutlined color="primary" />
            <Typography variant="subtitle1" fontWeight={700}>系统资源</Typography>
          </Box>
          <Typography variant="caption" color="text.disabled">
            架构: {systemStats?.system_info?.machine || '-'}
          </Typography>
        </Box>

        <Grid container spacing={2}>
          <Grid size={{ xs: 12, sm: 6 }}>
            <Box>
              <Box display="flex" justifyContent="space-between" alignItems="center" mb={0.75}>
                <Box display="flex" alignItems="center" gap={0.75}>
                  <CpuIcon fontSize="small" sx={{ color: 'text.secondary' }} />
                  <Typography variant="caption" fontWeight={700}>
                    CPU ({systemStats?.cpu_load?.core_count || '-'}核)
                  </Typography>
                </Box>
                <Typography variant="caption" fontFamily={'"Geist Mono", monospace'} fontWeight={700}>
                  {systemStats?.cpu_load ? `${systemStats.cpu_load.load_percent.toFixed(0)}%` : '-'}
                </Typography>
              </Box>
              <LinearProgress
                variant="determinate"
                value={systemStats?.cpu_load?.load_percent || 0}
                color={getCpuColor(systemStats?.cpu_load?.load_percent || 0)}
                sx={progressSx}
              />
              <Typography variant="caption" color="text.disabled" sx={{ display: 'block', mt: 0.5, textAlign: 'right' }}>
                负载: {systemStats?.cpu_load?.load_1min.toFixed(2) || '-'} / {systemStats?.cpu_load?.load_5min.toFixed(2) || '-'} / {systemStats?.cpu_load?.load_15min.toFixed(2) || '-'}
              </Typography>
            </Box>
          </Grid>

          <Grid size={{ xs: 12, sm: 6 }}>
            <Box>
              <Box display="flex" justifyContent="space-between" alignItems="center" mb={0.75}>
                <Box display="flex" alignItems="center" gap={0.75}>
                  <Memory fontSize="small" sx={{ color: 'text.secondary' }} />
                  <Typography variant="caption" fontWeight={700}>内存</Typography>
                </Box>
                <Typography variant="caption" fontFamily={'"Geist Mono", monospace'} fontWeight={700}>
                  {systemStats?.memory ? `${systemStats.memory.used_percent.toFixed(0)}%` : '-'}
                </Typography>
              </Box>
              <LinearProgress
                variant="determinate"
                value={systemStats?.memory?.used_percent || 0}
                color={getMemoryColor(systemStats?.memory?.used_percent || 0)}
                sx={progressSx}
              />
              <Typography variant="caption" color="text.disabled" sx={{ display: 'block', mt: 0.5, textAlign: 'right' }}>
                {systemStats?.memory ? `已用 ${formatBytes(systemStats.memory.used_bytes)} / 可用 ${formatBytes(systemStats.memory.available_bytes)}` : '-'}
              </Typography>
            </Box>
          </Grid>

          <Grid size={{ xs: 12, sm: 6 }}>
            <Box>
              <Box display="flex" justifyContent="space-between" alignItems="center" mb={0.75}>
                <Box display="flex" alignItems="center" gap={0.75}>
                  <Storage fontSize="small" sx={{ color: 'text.secondary' }} />
                  <Typography variant="caption" fontWeight={700}>磁盘 (根目录)</Typography>
                </Box>
                <Typography variant="caption" fontFamily={'"Geist Mono", monospace'} fontWeight={700}>
                  {rootDisk ? `${rootDisk.used_percent.toFixed(0)}%` : '-'}
                </Typography>
              </Box>
              <LinearProgress
                variant="determinate"
                value={rootDisk?.used_percent || 0}
                color={getMemoryColor(rootDisk?.used_percent || 0)}
                sx={progressSx}
              />
              <Typography variant="caption" color="text.disabled" sx={{ display: 'block', mt: 0.5, textAlign: 'right' }}>
                {rootDisk ? `${formatBytes(rootDisk.used_bytes)} / ${formatBytes(rootDisk.total_bytes)}` : '-'}
              </Typography>
            </Box>
          </Grid>

          <Grid size={{ xs: 12, sm: 6 }}>
            <Box>
              <Box display="flex" justifyContent="space-between" alignItems="center" mb={0.75}>
                <Box display="flex" alignItems="center" gap={0.75}>
                  <Thermostat fontSize="small" sx={{ color: 'text.secondary' }} />
                  <Typography variant="caption" fontWeight={700}>最高温</Typography>
                </Box>
                <Typography variant="caption" fontFamily={'"Geist Mono", monospace'} fontWeight={700} sx={{ color: hottestSensor ? hottestColor : 'text.secondary' }}>
                  {hottestSensor ? `${hottestSensor.temperature.toFixed(1)}°C` : '-'}
                </Typography>
              </Box>
              <LinearProgress
                variant="determinate"
                value={hottestPercent}
                sx={temperatureProgressSx}
              />
              <Box display="flex" alignItems="center" justifyContent="space-between" gap={1.5} mt={0.5}>
                <Box display="flex" alignItems="center" gap={0.5} minWidth={0}>
                  <Box
                    sx={{
                      width: 5,
                      height: 5,
                      borderRadius: '50%',
                      bgcolor: getTempDotColor(coldestSensor),
                      flex: '0 0 auto',
                    }}
                  />
                  <Typography variant="caption" fontFamily={'"Geist Mono", monospace'} color="text.secondary" noWrap>
                    {formatTemperatureSource(coldestSensor)}
                  </Typography>
                </Box>
                <Box display="flex" alignItems="center" justifyContent="flex-end" gap={0.5} minWidth={0}>
                  <Box
                    sx={{
                      width: 5,
                      height: 5,
                      borderRadius: '50%',
                      bgcolor: getTempDotColor(hottestSensor),
                      flex: '0 0 auto',
                    }}
                  />
                  <Typography variant="caption" fontFamily={'"Geist Mono", monospace'} color="text.secondary" noWrap textAlign="right">
                    {formatTemperatureSource(hottestSensor)}
                  </Typography>
                </Box>
              </Box>
            </Box>
          </Grid>
        </Grid>
      </CardContent>
    </Card>
  )
}
