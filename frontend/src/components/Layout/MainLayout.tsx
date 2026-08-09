import { useEffect, useState } from 'react'
import { Outlet } from 'react-router-dom'
import { Box, useMediaQuery, useTheme, type Theme } from '@mui/material'
import Sidebar from './Sidebar'
import TopBar from './TopBar'
import { RefreshContext } from '../../contexts/RefreshContext'
import { LAYOUT_BOTTOM_ACTION_BAR_HEIGHT, LAYOUT_BOTTOM_ACTION_BAR_ID } from './layoutConstants'
import { api } from '../../api/current'
import { MODEM_CHANGED_EVENT } from '../../contexts/modemEvents'

const DRAWER_WIDTH = 224
const DRAWER_MINI_WIDTH = 64
const CONTENT_MAX_WIDTH = 1440

export default function MainLayout() {
  const theme = useTheme<Theme>()
  const isMobile = useMediaQuery(theme.breakpoints.down('sm'))
  const [mobileOpen, setMobileOpen] = useState(false)
  const [desktopOpen, setDesktopOpen] = useState(true) // 桌面端侧边栏状态，默认展开
  const [refreshInterval, setRefreshInterval] = useState(3000) // 默认 3 秒（移动端友好）
  const [refreshKey, setRefreshKey] = useState(0)

  useEffect(() => {
    let cancelled = false
    void api.getUiPreferences()
      .then((response) => {
        if (!cancelled && response.data) {
          setRefreshInterval(response.data.refresh_interval_ms)
        }
      })
      .catch((error) => console.warn('读取刷新频率失败:', error))
    return () => {
      cancelled = true
    }
  }, [])

  const handleDrawerToggle = () => {
    if (isMobile) {
      setMobileOpen(!mobileOpen)
    } else {
      setDesktopOpen(!desktopOpen)
    }
  }

  const triggerRefresh = () => {
    setRefreshKey((prev) => prev + 1)
  }

  useEffect(() => {
    const handleModemChanged = () => triggerRefresh()
    window.addEventListener(MODEM_CHANGED_EVENT, handleModemChanged)
    return () => window.removeEventListener(MODEM_CHANGED_EVENT, handleModemChanged)
  }, [])

  const updateRefreshInterval = (interval: number) => {
    const previous = refreshInterval
    setRefreshInterval(interval)
    void api.setUiPreferences({ refresh_interval_ms: interval as 0 | 1000 | 3000 | 5000 | 10000 })
      .catch((error) => {
        console.warn('保存刷新频率失败:', error)
        setRefreshInterval(previous)
      })
  }

  return (
    <RefreshContext.Provider
      value={{ refreshInterval, setRefreshInterval, refreshKey, triggerRefresh }}
    >
      <Box
        sx={{
          display: 'flex',
          height: '100vh',
          overflow: 'hidden',
          bgcolor: 'background.default',
        }}
      >
        <Box
          component="a"
          href="#main-content"
          sx={{
            position: 'fixed',
            top: 8,
            left: 8,
            zIndex: (currentTheme) => currentTheme.zIndex.tooltip + 1,
            px: 1.5,
            py: 0.75,
            border: 1,
            borderColor: 'divider',
            borderRadius: 1,
            bgcolor: 'background.paper',
            color: 'text.primary',
            transform: 'translateY(-150%)',
            '&:focus': { transform: 'translateY(0)' },
          }}
        >
          跳到主内容
        </Box>

        <Sidebar
          drawerWidth={DRAWER_WIDTH}
          miniWidth={DRAWER_MINI_WIDTH}
          mobileOpen={mobileOpen}
          desktopOpen={desktopOpen}
          onClose={handleDrawerToggle}
          isMobile={isMobile}
        />

        <Box
          sx={{
            display: 'flex',
            flexDirection: 'column',
            flexGrow: 1,
            minWidth: 0,
            height: '100vh',
          }}
        >
          <TopBar
            onMenuClick={handleDrawerToggle}
            refreshInterval={refreshInterval}
            onRefreshIntervalChange={updateRefreshInterval}
          />

          <Box
            component="main"
            id="main-content"
            tabIndex={-1}
            sx={{
              flexGrow: 1,
              minHeight: 0,
              overflow: 'auto',
              px: { xs: 2, sm: 3, lg: 4 },
              py: { xs: 2.5, sm: 3.5 },
            }}
          >
            <Box sx={{ width: '100%', maxWidth: CONTENT_MAX_WIDTH, mx: 'auto' }}>
              <Outlet />
            </Box>
          </Box>

          <Box
            id={LAYOUT_BOTTOM_ACTION_BAR_ID}
            sx={{
              flexShrink: 0,
              '&:empty': {
                display: 'none',
              },
              '&:not(:empty)': {
                alignItems: 'center',
                bgcolor: 'transparent',
                borderTop: '1px solid',
                borderColor: 'divider',
                display: 'flex',
                height: LAYOUT_BOTTOM_ACTION_BAR_HEIGHT,
                minHeight: LAYOUT_BOTTOM_ACTION_BAR_HEIGHT,
                px: { xs: 2, sm: 3 },
              },
            }}
          />
        </Box>
      </Box>
    </RefreshContext.Provider>
  )
}
