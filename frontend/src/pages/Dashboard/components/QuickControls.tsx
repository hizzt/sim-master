import { Box, Card, CardContent, Typography, Stack, Switch, Chip } from '@mui/material'
import { NetworkCheck, FlightTakeoff, TravelExplore, Tune, WifiCalling3 } from '@mui/icons-material'
import type { AirplaneModeResponse, RoamingResponse, VowifiTunnelStatus } from '@/api/types'

interface QuickControlsProps {
  dataStatus: boolean
  airplaneMode: AirplaneModeResponse | null
  roaming: RoamingResponse | null
  vowifiStatus: VowifiTunnelStatus | null
  onToggleData: () => void
  onToggleAirplaneMode: () => void
  onToggleRoaming: () => void
  onToggleVowifi: () => void
  disabled?: boolean
}

export function QuickControls({
  dataStatus,
  airplaneMode,
  roaming,
  onToggleData,
  onToggleAirplaneMode,
  onToggleRoaming,
  vowifiStatus,
  onToggleVowifi,
  disabled = false,
}: QuickControlsProps) {
  return (
    <Card sx={{ height: '100%' }}>
      <CardContent>
        <Box display="flex" alignItems="center" gap={1} mb={2}>
          <Tune color="primary" />
          <Typography variant="subtitle1" fontWeight={700}>快捷控制</Typography>
        </Box>

        <Stack spacing={2}>
          <Box display="flex" alignItems="center" justifyContent="space-between">
            <Box display="flex" alignItems="center" gap={1}>
              <WifiCalling3 color={vowifiStatus?.running ? 'success' : 'disabled'} />
              <Typography variant="body2">VoWiFi</Typography>
              <Chip label={vowifiStatus?.ims_registered ? '已驻网' : vowifiStatus?.running ? '连接中' : '未启用'} size="small" color={vowifiStatus?.ims_registered ? 'success' : 'default'} sx={{ height: 18, fontSize: '0.65rem' }} />
            </Box>
            <Switch checked={Boolean(vowifiStatus?.running)} onChange={() => void onToggleVowifi()} color="success" size="small" disabled={disabled} />
          </Box>
          <Box display="flex" alignItems="center" justifyContent="space-between">
            <Box display="flex" alignItems="center" gap={1}>
              <NetworkCheck color={dataStatus ? 'success' : 'disabled'} />
              <Typography variant="body2">数据连接</Typography>
            </Box>
            <Switch
              checked={dataStatus}
              onChange={() => {
                void onToggleData()
              }}
              color="success"
              size="small"
              disabled={disabled}
            />
          </Box>

          <Box display="flex" alignItems="center" justifyContent="space-between">
            <Box display="flex" alignItems="center" gap={1}>
              <TravelExplore color={roaming?.roaming_allowed ? 'info' : 'disabled'} />
              <Typography variant="body2">漫游数据</Typography>
              {roaming?.is_roaming && (
                <Chip label="漫游中" size="small" color="warning" sx={{ height: 18, fontSize: '0.65rem' }} />
              )}
            </Box>
            <Switch
              checked={roaming?.roaming_allowed || false}
              onChange={() => {
                void onToggleRoaming()
              }}
              color="info"
              size="small"
              disabled={disabled}
            />
          </Box>

          <Box display="flex" alignItems="center" justifyContent="space-between">
            <Box display="flex" alignItems="center" gap={1}>
              <FlightTakeoff color={airplaneMode?.enabled ? 'warning' : 'disabled'} />
              <Typography variant="body2">飞行模式</Typography>
            </Box>
            <Switch
              checked={airplaneMode?.enabled || false}
              onChange={() => {
                void onToggleAirplaneMode()
              }}
              color="warning"
              size="small"
              disabled={disabled}
            />
          </Box>
        </Stack>
      </CardContent>
    </Card>
  )
}
