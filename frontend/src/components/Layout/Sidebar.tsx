import type { ElementType } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import {
  Box,
  Divider,
  Drawer,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  AutoMode as AutomationIcon,
  Dashboard as DashboardIcon,
  Memory as ModemIcon,
  NotificationsNone as NotificationsIcon,
  MultipleStop as ProxyIcon,
  Phone as PhoneIcon,
  Settings as SettingsIcon,
  SettingsBackupRestore as BackupRestoreIcon,
  SignalCellularAlt as SignalIcon,
  SimCard as SimIcon,
  SmsOutlined as SmsIcon,
  SystemUpdateAlt as OtaIcon,
  WifiCalling3 as VoWifiIcon,
} from '@mui/icons-material'

const SIDEBAR_TRANSITION = '180ms ease'

interface SidebarProps {
  drawerWidth: number
  miniWidth: number
  mobileOpen: boolean
  desktopOpen: boolean
  onClose: () => void
  isMobile: boolean
}

interface NavItem {
  path: string
  label: string
  icon: ElementType
}

interface NavGroup {
  label?: string
  items: NavItem[]
}

const navGroups: NavGroup[] = [
  {
    items: [
      { path: '/', label: '概览', icon: DashboardIcon },
      { path: '/modems', label: '模块管理', icon: ModemIcon },
      { path: '/sim', label: 'SIM 卡', icon: SimIcon },
      { path: '/sms', label: '短信', icon: SmsIcon },
      { path: '/phone', label: '电话', icon: PhoneIcon },
    ],
  },
  {
    label: '网络',
    items: [
      { path: '/network', label: '蜂窝网络', icon: SignalIcon },
      { path: '/vowifi', label: 'VoWiFi', icon: VoWifiIcon },
      { path: '/proxy', label: '网络代理', icon: ProxyIcon },
    ],
  },
  {
    label: '流程',
    items: [
      { path: '/automation', label: '自动化', icon: AutomationIcon },
      { path: '/notifications', label: '通知', icon: NotificationsIcon },
    ],
  },
  {
    label: '系统',
    items: [
      { path: '/config', label: '基本配置', icon: SettingsIcon },
      { path: '/config/backup', label: '备份与恢复', icon: BackupRestoreIcon },
      { path: '/ota', label: 'OTA 更新', icon: OtaIcon },
    ],
  },
]

function isSelected(pathname: string, path: string) {
  if (path === '/') return pathname === '/'
  return pathname === path || pathname.startsWith(`${path}/`)
}

export default function Sidebar({
  drawerWidth,
  miniWidth,
  mobileOpen,
  desktopOpen,
  onClose,
  isMobile,
}: SidebarProps) {
  const navigate = useNavigate()
  const location = useLocation()

  const goTo = (path: string) => {
    void navigate(path)
    if (isMobile) onClose()
  }

  const renderDrawer = (compact: boolean) => (
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%', minWidth: 0 }}>
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          height: 52,
          px: compact ? 1 : 1.5,
          gap: 1,
          flexShrink: 0,
        }}
      >
        <Box component="img" src="/simadmin-logo.svg" alt="" sx={{ width: 26, height: 26, flex: '0 0 auto' }} />
        {!compact && (
          <Box minWidth={0}>
            <Typography variant="subtitle2" noWrap fontWeight={600}>SimAdmin</Typography>
            <Typography variant="caption" color="text.secondary" noWrap>SIM / eSIM 控制台</Typography>
          </Box>
        )}
      </Box>

      <Divider />

      <Box sx={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', py: 1 }}>
        {navGroups.map((group, groupIndex) => (
          <Box key={group.label ?? 'primary'} sx={{ mt: groupIndex === 0 ? 0 : 1.5 }}>
            {!compact && group.label && (
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: 'block', px: 2, py: 0.5, fontWeight: 500 }}
              >
                {group.label}
              </Typography>
            )}
            {compact && groupIndex > 0 && <Divider sx={{ mx: 1, my: 1 }} />}
            <List disablePadding sx={{ px: compact ? 0.75 : 1 }}>
              {group.items.map((item) => {
                const selected = isSelected(location.pathname, item.path)
                const Icon = item.icon
                const button = (
                  <ListItemButton
                    selected={selected}
                    onClick={() => goTo(item.path)}
                    aria-current={selected ? 'page' : undefined}
                    sx={{
                      minHeight: 36,
                      borderRadius: 1,
                      px: compact ? 0 : 1,
                      justifyContent: compact ? 'center' : 'flex-start',
                      color: selected ? 'text.primary' : 'text.secondary',
                      '&.Mui-selected': { bgcolor: 'action.selected' },
                      '&.Mui-selected:hover': { bgcolor: 'action.selected' },
                    }}
                  >
                    <ListItemIcon
                      sx={{
                        minWidth: compact ? 0 : 30,
                        color: 'inherit',
                        justifyContent: 'center',
                      }}
                    >
                      <Icon sx={{ fontSize: 18 }} />
                    </ListItemIcon>
                    {!compact && (
                      <ListItemText
                        primary={item.label}
                        primaryTypographyProps={{ noWrap: true, fontSize: 14, fontWeight: selected ? 600 : 400 }}
                      />
                    )}
                  </ListItemButton>
                )

                return (
                  <ListItem key={item.path} disablePadding sx={{ mb: 0.25 }}>
                    {compact ? <Tooltip title={item.label} placement="right">{button}</Tooltip> : button}
                  </ListItem>
                )
              })}
            </List>
          </Box>
        ))}
      </Box>

      <Divider />
      <Box sx={{ p: compact ? 1 : 1.5, minWidth: 0 }}>
        {!compact && (
          <Typography variant="caption" color="text.disabled" sx={{ display: 'block', fontFamily: '"Geist Mono", monospace' }}>
            v{__APP_VERSION__} · {__GIT_COMMIT__}
          </Typography>
        )}
      </Box>
    </Box>
  )

  const paperSx = {
    width: desktopOpen ? drawerWidth : miniWidth,
    overflowX: 'hidden',
    borderRight: '1px solid',
    borderColor: 'divider',
    borderRadius: 0,
    bgcolor: 'background.default',
    backgroundImage: 'none',
    boxShadow: 'none',
    transition: `width ${SIDEBAR_TRANSITION}`,
  } as const

  return (
    <Box
      component="nav"
      aria-label="主导航"
      sx={{
        width: { xs: 0, sm: desktopOpen ? drawerWidth : miniWidth },
        flexShrink: 0,
        transition: `width ${SIDEBAR_TRANSITION}`,
      }}
    >
      <Drawer
        variant="temporary"
        open={mobileOpen}
        onClose={onClose}
        ModalProps={{ keepMounted: true }}
        sx={{ display: { xs: 'block', sm: 'none' }, '& .MuiDrawer-paper': { ...paperSx, width: drawerWidth } }}
      >
        {renderDrawer(false)}
      </Drawer>
      <Drawer
        variant="persistent"
        open
        sx={{ display: { xs: 'none', sm: 'block' }, '& .MuiDrawer-paper': paperSx }}
      >
        {renderDrawer(!desktopOpen)}
      </Drawer>
    </Box>
  )
}
