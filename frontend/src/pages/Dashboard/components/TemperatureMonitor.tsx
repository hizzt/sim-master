import { Box, Card, CardContent, LinearProgress, Typography } from '@mui/material'
import { LocalFireDepartment } from '@mui/icons-material'
import type { SystemStatsResponse } from '@/api/types'
import { getTempPercent, getTempBarColor } from '../utils'

interface TemperatureMonitorProps {
  systemStats: SystemStatsResponse | null
}

export function TemperatureMonitor({ systemStats }: TemperatureMonitorProps) {
  return (
    <Card sx={{ height: '100%' }}>
      <CardContent>
        <Box display="flex" alignItems="center" gap={1} mb={2} pb={1.25} borderBottom={1} borderColor="divider">
          <LocalFireDepartment color="primary" />
          <Typography variant="subtitle1" fontWeight={700}>温度监控</Typography>
        </Box>

        {systemStats?.temperature && systemStats.temperature.length > 0 ? (
          <Box display="flex" flexDirection="column" gap={1.5}>
            {systemStats.temperature.map((sensor, idx) => {
              const percent = getTempPercent(sensor.temperature)
              const color = getTempBarColor(sensor.temperature)
              return (
                <Box key={`${sensor.type}-${idx}`} display="flex" alignItems="center" justifyContent="space-between" gap={1.5}>
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    noWrap
                    sx={{ minWidth: 0, flex: '1 1 auto', fontSize: '0.82rem' }}
                  >
                    {sensor.label || sensor.type}
                  </Typography>
                  <Box display="flex" alignItems="center" gap={1.25} sx={{ flex: '0 0 auto' }}>
                    <LinearProgress
                      variant="determinate"
                      value={percent}
                      sx={{
                        width: 96,
                        height: 5,
                        borderRadius: 0,
                        '& .MuiLinearProgress-bar': { bgcolor: color, borderRadius: 0 },
                      }}
                    />
                    <Typography
                      variant="body2"
                      fontFamily={'"Geist Mono", monospace'}
                      fontWeight={700}
                      sx={{ color, width: 48, textAlign: 'right' }}
                    >
                      {sensor.temperature.toFixed(1)}°
                    </Typography>
                  </Box>
                </Box>
              )
            })}
          </Box>
        ) : (
          <Typography variant="body2" color="text.secondary">暂无数据</Typography>
        )}
      </CardContent>
    </Card>
  )
}
