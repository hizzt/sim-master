import type { ReactNode } from 'react'
import { Box, Stack, Typography } from '@mui/material'

interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  meta?: ReactNode
}

export function PageHeader({ title, description, actions, meta }: PageHeaderProps) {
  return (
    <Box
      component="header"
      sx={{
        display: 'grid',
        gridTemplateColumns: { xs: 'minmax(0, 1fr)', sm: actions ? 'minmax(0, 1fr) auto' : 'minmax(0, 1fr)' },
        alignItems: 'start',
        gap: 2,
        mb: { xs: 3, sm: 4 },
      }}
    >
      <Box sx={{ minWidth: 0, maxWidth: 760 }}>
        <Typography component="h1" variant="h2">
          {title}
        </Typography>
        {description && (
          <Typography color="text.secondary" sx={{ mt: 0.75, maxWidth: 680 }}>
            {description}
          </Typography>
        )}
        {meta && <Box sx={{ mt: 1.25 }}>{meta}</Box>}
      </Box>
      {actions && (
        <Stack
          direction="row"
          spacing={1}
          useFlexGap
          flexWrap="wrap"
          sx={{ justifyContent: { xs: 'flex-start', sm: 'flex-end' } }}
        >
          {actions}
        </Stack>
      )}
    </Box>
  )
}

interface SectionHeaderProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
}

export function SectionHeader({ title, description, actions }: SectionHeaderProps) {
  return (
    <Box
      sx={{
        display: 'flex',
        alignItems: { xs: 'flex-start', sm: 'center' },
        justifyContent: 'space-between',
        flexDirection: { xs: 'column', sm: 'row' },
        gap: 1.5,
        mb: 2,
      }}
    >
      <Box sx={{ minWidth: 0 }}>
        <Typography component="h2" variant="h6">{title}</Typography>
        {description && (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 0.25 }}>
            {description}
          </Typography>
        )}
      </Box>
      {actions && <Box sx={{ flexShrink: 0 }}>{actions}</Box>}
    </Box>
  )
}

interface PageSectionProps {
  children: ReactNode
  topBorder?: boolean
}

export function PageSection({ children, topBorder = true }: PageSectionProps) {
  return (
    <Box
      component="section"
      sx={{
        borderTop: topBorder ? 1 : 0,
        borderColor: 'divider',
        pt: topBorder ? { xs: 2.5, sm: 3 } : 0,
      }}
    >
      {children}
    </Box>
  )
}

export function StatusDot({ active }: { active: boolean }) {
  return (
    <Box
      component="span"
      aria-hidden="true"
      sx={{
        width: 7,
        height: 7,
        borderRadius: '50%',
        bgcolor: active ? 'success.main' : 'error.main',
        flex: '0 0 auto',
      }}
    />
  )
}

