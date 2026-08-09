/* eslint-disable react-refresh/only-export-components */
import { createContext, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { alpha, createTheme, ThemeProvider as MuiThemeProvider } from '@mui/material/styles'
import CssBaseline from '@mui/material/CssBaseline'
import { api } from '../api/current'

type ThemeMode = 'light' | 'dark'

interface ThemeContextType {
  mode: ThemeMode
  toggleTheme: () => void
}

const ThemeContext = createContext<ThemeContextType | undefined>(undefined)

export function useTheme() {
  const context = useContext(ThemeContext)
  if (!context) throw new Error('useTheme must be used within ThemeProvider')
  return context
}

interface ThemeProviderProps {
  children: ReactNode
}

function createSimAdminTheme(mode: ThemeMode) {
  const dark = mode === 'dark'
  const surface = dark ? '#000000' : '#ffffff'
  const raised = dark ? '#0a0a0a' : '#ffffff'
  const text = dark ? '#ededed' : '#1d1d1f'
  const secondaryText = dark ? '#a1a1a1' : '#666666'
  const subtle = dark ? '#1f1f1f' : '#f2f2f2'
  const divider = dark ? '#2e2e2e' : '#e5e5e5'
  const primary = dark ? '#ededed' : '#171717'
  const success = dark ? '#46a758' : '#18794e'
  const warning = dark ? '#f2a65a' : '#ad5700'
  const error = dark ? '#ff6369' : '#d93036'
  const info = dark ? '#52a9ff' : '#0969da'
  const semanticContrast = dark ? '#000000' : '#ffffff'

  return createTheme({
    palette: {
      mode,
      primary: {
        main: primary,
        contrastText: dark ? '#000000' : '#ffffff',
      },
      secondary: {
        main: secondaryText,
        contrastText: dark ? '#000000' : '#ffffff',
      },
      success: { main: success, contrastText: semanticContrast },
      warning: { main: warning, contrastText: semanticContrast },
      error: { main: error, contrastText: semanticContrast },
      info: { main: info, contrastText: semanticContrast },
      background: { default: surface, paper: raised },
      text: {
        primary: text,
        secondary: secondaryText,
        disabled: dark ? '#666666' : '#999999',
      },
      divider,
      action: {
        hover: dark ? '#171717' : '#f5f5f5',
        selected: dark ? '#242424' : '#eeeeee',
        disabledBackground: dark ? '#171717' : '#f2f2f2',
      },
    },
    shape: { borderRadius: 6 },
    typography: {
      fontFamily: '"Geist", "PingFang SC", "Microsoft YaHei", sans-serif',
      fontSize: 14,
      h1: { fontSize: '2rem', lineHeight: 1.2, fontWeight: 600, letterSpacing: 0 },
      h2: { fontSize: '1.5rem', lineHeight: 1.25, fontWeight: 600, letterSpacing: 0 },
      h3: { fontSize: '1.25rem', lineHeight: 1.3, fontWeight: 600, letterSpacing: 0 },
      h4: { fontSize: '1.25rem', lineHeight: 1.3, fontWeight: 600, letterSpacing: 0 },
      h5: { fontSize: '1.5rem', lineHeight: 1.25, fontWeight: 600, letterSpacing: 0 },
      h6: { fontSize: '1rem', lineHeight: 1.4, fontWeight: 600, letterSpacing: 0 },
      subtitle1: { fontSize: '1rem', lineHeight: 1.5, fontWeight: 500, letterSpacing: 0 },
      subtitle2: { fontSize: '0.875rem', lineHeight: 1.45, fontWeight: 500, letterSpacing: 0 },
      body1: { fontSize: '0.9375rem', lineHeight: 1.6, fontWeight: 400, letterSpacing: 0 },
      body2: { fontSize: '0.875rem', lineHeight: 1.55, fontWeight: 400, letterSpacing: 0 },
      button: { fontSize: '0.875rem', lineHeight: 1.25, fontWeight: 500, letterSpacing: 0, textTransform: 'none' },
      caption: { fontSize: '0.75rem', lineHeight: 1.5, fontWeight: 400, letterSpacing: 0 },
      overline: { fontSize: '0.75rem', lineHeight: 1.5, fontWeight: 500, letterSpacing: 0, textTransform: 'none' },
    },
    components: {
      MuiCssBaseline: {
        styleOverrides: {
          html: { minWidth: 320, minHeight: '100%' },
          body: {
            minWidth: 320,
            minHeight: '100vh',
            backgroundColor: surface,
            color: text,
            scrollbarColor: `${dark ? '#4b4b4b' : '#c7c7c7'} transparent`,
            '& #root': { minHeight: '100vh' },
            '&::-webkit-scrollbar, & *::-webkit-scrollbar': { width: 8, height: 8 },
            '&::-webkit-scrollbar-thumb, & *::-webkit-scrollbar-thumb': {
              borderRadius: 4,
              backgroundColor: dark ? '#4b4b4b' : '#c7c7c7',
              border: `2px solid ${surface}`,
            },
            '&::-webkit-scrollbar-track, & *::-webkit-scrollbar-track': { background: 'transparent' },
            '& :focus-visible': {
              outline: `2px solid ${dark ? '#ffffff' : '#000000'}`,
              outlineOffset: 2,
            },
          },
        },
      },
      MuiAppBar: {
        defaultProps: { elevation: 0 },
        styleOverrides: { root: { color: text, background: surface, backgroundImage: 'none', boxShadow: 'none' } },
      },
      MuiPaper: {
        defaultProps: { elevation: 0 },
        styleOverrides: {
          root: {
            backgroundImage: 'none',
            boxShadow: 'none',
            '&.MuiPaper-outlined': { borderColor: divider },
          },
        },
      },
      MuiCard: {
        defaultProps: { elevation: 0 },
        styleOverrides: {
          root: {
            minWidth: 0,
            overflow: 'visible',
            borderRadius: 0,
            border: 0,
            background: 'transparent',
            backgroundImage: 'none',
            boxShadow: 'none',
            '&.MuiCard-outlined': { border: `1px solid ${divider}`, borderRadius: 6, background: raised },
          },
        },
      },
      MuiCardHeader: {
        styleOverrides: {
          root: { padding: '20px 0 8px' },
          avatar: { marginRight: 10, color: secondaryText },
          action: { alignSelf: 'center', margin: 0 },
          title: { fontSize: '1rem', lineHeight: 1.4, fontWeight: 600 },
          subheader: { marginTop: 2, fontSize: '0.8125rem', lineHeight: 1.45 },
        },
      },
      MuiCardContent: {
        styleOverrides: { root: { padding: '12px 0 20px', '&:last-child': { paddingBottom: 20 } } },
      },
      MuiButton: {
        defaultProps: { disableElevation: true },
        styleOverrides: {
          root: { minHeight: 34, borderRadius: 6, paddingInline: 14, boxShadow: 'none' },
          contained: { boxShadow: 'none', '&:hover': { boxShadow: 'none' } },
          outlined: { borderColor: divider, '&:hover': { borderColor: dark ? '#666' : '#999', background: subtle } },
          text: { '&:hover': { background: subtle } },
          sizeSmall: { minHeight: 30, paddingInline: 10 },
        },
      },
      MuiIconButton: {
        styleOverrides: {
          root: { borderRadius: 6, '&:hover': { background: subtle } },
          sizeSmall: { padding: 6 },
        },
      },
      MuiChip: {
        defaultProps: { size: 'small' },
        styleOverrides: {
          root: {
            height: 24,
            borderRadius: 4,
            fontWeight: 500,
            background: subtle,
            '&.MuiChip-filled.MuiChip-colorPrimary': { backgroundColor: primary, color: dark ? '#000000' : '#ffffff' },
            '&.MuiChip-filled.MuiChip-colorSecondary': { backgroundColor: secondaryText, color: semanticContrast },
            '&.MuiChip-filled.MuiChip-colorSuccess': { backgroundColor: success, color: semanticContrast },
            '&.MuiChip-filled.MuiChip-colorWarning': { backgroundColor: warning, color: semanticContrast },
            '&.MuiChip-filled.MuiChip-colorError': { backgroundColor: error, color: semanticContrast },
            '&.MuiChip-filled.MuiChip-colorInfo': { backgroundColor: info, color: semanticContrast },
          },
          outlined: { background: 'transparent', borderColor: divider },
          label: { paddingInline: 8 },
          icon: { fontSize: 14, color: 'inherit' },
        },
      },
      MuiDivider: { styleOverrides: { root: { borderColor: divider } } },
      MuiTabs: {
        styleOverrides: {
          root: { minHeight: 40, borderBottom: `1px solid ${divider}` },
          indicator: { height: 2, background: text },
        },
      },
      MuiTab: {
        styleOverrides: {
          root: {
            minHeight: 40,
            minWidth: 0,
            padding: '8px 12px',
            color: secondaryText,
            fontSize: '0.875rem',
            fontWeight: 500,
            textTransform: 'none',
            '&.Mui-selected': { color: text },
          },
          icon: { fontSize: 17 },
        },
      },
      MuiTableCell: {
        styleOverrides: {
          root: { padding: '11px 12px', borderColor: divider, fontSize: '0.8125rem' },
          head: { color: secondaryText, background: 'transparent', fontWeight: 500, verticalAlign: 'bottom' },
        },
      },
      MuiTableRow: { styleOverrides: { root: { '&:hover': { backgroundColor: alpha(text, 0.025) } } } },
      MuiTableContainer: {
        styleOverrides: { root: { borderRadius: 0, boxShadow: 'none' } },
      },
      MuiTextField: { defaultProps: { size: 'small' } },
      MuiFormControl: { defaultProps: { size: 'small' } },
      MuiOutlinedInput: {
        styleOverrides: {
          root: {
            borderRadius: 6,
            background: surface,
            '& .MuiOutlinedInput-notchedOutline': { borderColor: divider },
            '&:hover .MuiOutlinedInput-notchedOutline': { borderColor: dark ? '#666' : '#999' },
          },
          input: { paddingBlock: 8.5 },
        },
      },
      MuiInputLabel: { styleOverrides: { root: { fontSize: '0.875rem' } } },
      MuiSwitch: {
        styleOverrides: {
          switchBase: { '&.Mui-checked': { color: dark ? '#fff' : '#111' } },
          track: { backgroundColor: dark ? '#555' : '#b7b7b7', opacity: 1 },
        },
      },
      MuiAlert: {
        defaultProps: { variant: 'outlined' },
        styleOverrides: {
          root: { borderRadius: 6, background: 'transparent', alignItems: 'center' },
          message: { paddingBlock: 2 },
        },
      },
      MuiDialog: {
        styleOverrides: {
          paper: { border: `1px solid ${divider}`, borderRadius: 8, background: raised, boxShadow: 'none' },
        },
      },
      MuiDialogTitle: { styleOverrides: { root: { fontSize: '1.125rem', fontWeight: 600, padding: '20px 24px 8px' } } },
      MuiDialogActions: { styleOverrides: { root: { padding: '12px 24px 20px' } } },
      MuiMenu: {
        styleOverrides: { paper: { marginTop: 4, border: `1px solid ${divider}`, borderRadius: 6, background: raised, boxShadow: 'none' } },
      },
      MuiMenuItem: { styleOverrides: { root: { minHeight: 36, borderRadius: 4, marginInline: 4, fontSize: '0.875rem' } } },
      MuiTooltip: { styleOverrides: { tooltip: { borderRadius: 4, fontSize: '0.75rem' } } },
      MuiLinearProgress: {
        styleOverrides: {
          root: { height: 5, borderRadius: 2, backgroundColor: subtle },
          bar: { borderRadius: 2 },
        },
      },
      MuiSkeleton: { styleOverrides: { root: { borderRadius: 4 } } },
      MuiAvatar: { styleOverrides: { root: { borderRadius: 6, background: subtle, color: text } } },
    },
  })
}

export function ThemeProvider({ children }: ThemeProviderProps) {
  const [mode, setMode] = useState<ThemeMode>('light')

  useEffect(() => {
    let cancelled = false
    void api.getUiPreferences()
      .then((response) => {
        if (!cancelled && response.data) setMode(response.data.theme_mode)
      })
      .catch((error) => console.warn('读取界面设置失败:', error))
    return () => { cancelled = true }
  }, [])

  const toggleTheme = () => {
    const nextMode = mode === 'light' ? 'dark' : 'light'
    setMode(nextMode)
    void api.setUiPreferences({ theme_mode: nextMode }).catch((error) => {
      console.warn('保存界面设置失败:', error)
      setMode(mode)
    })
  }

  const theme = useMemo(() => createSimAdminTheme(mode), [mode])

  return (
    <ThemeContext.Provider value={{ mode, toggleTheme }}>
      <MuiThemeProvider theme={theme}>
        <CssBaseline />
        {children}
      </MuiThemeProvider>
    </ThemeContext.Provider>
  )
}
