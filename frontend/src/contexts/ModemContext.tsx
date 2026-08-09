/* eslint-disable react-refresh/only-export-components */
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, MODEM_STORAGE_KEY, type ModemSummary } from '../api/current'
import { queryClient } from '../lib/queryClient'
import { MODEM_CHANGED_EVENT } from './modemEvents'

interface ModemContextValue {
  modems: ModemSummary[]
  selectedModemId: string
  selectedModem?: ModemSummary
  loading: boolean
  error: string | null
  selectModem: (id: string) => void
  refreshModems: () => Promise<void>
}

const ModemContext = createContext<ModemContextValue | null>(null)

export function ModemProvider({ children }: { children: ReactNode }) {
  const [modems, setModems] = useState<ModemSummary[]>([])
  const [selectedModemId, setSelectedModemId] = useState(
    () => window.localStorage.getItem(MODEM_STORAGE_KEY)?.trim() ?? '',
  )
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refreshModems = useCallback(async () => {
    try {
      const response = await api.getModems()
      const next = response.data?.modems ?? []
      setModems(next)
      setError(null)
      setSelectedModemId((current) => {
        if (current && next.some((modem) => modem.id === current)) return current
        const fallback = next.find((modem) => modem.selected)?.id ?? next[0]?.id ?? ''
        if (fallback) window.localStorage.setItem(MODEM_STORAGE_KEY, fallback)
        else window.localStorage.removeItem(MODEM_STORAGE_KEY)
        return fallback
      })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '读取模块列表失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refreshModems()
    const timer = window.setInterval(() => void refreshModems(), 10000)
    return () => window.clearInterval(timer)
  }, [refreshModems])

  const selectModem = useCallback((id: string) => {
    if (!modems.some((modem) => modem.id === id)) return
    window.localStorage.setItem(MODEM_STORAGE_KEY, id)
    setSelectedModemId(id)
    void queryClient.invalidateQueries()
    window.dispatchEvent(new CustomEvent(MODEM_CHANGED_EVENT, { detail: id }))
  }, [modems])

  const value = useMemo<ModemContextValue>(() => ({
    modems,
    selectedModemId,
    selectedModem: modems.find((modem) => modem.id === selectedModemId),
    loading,
    error,
    selectModem,
    refreshModems,
  }), [error, loading, modems, refreshModems, selectModem, selectedModemId])

  return <ModemContext.Provider value={value}>{children}</ModemContext.Provider>
}

export function useModems() {
  const context = useContext(ModemContext)
  if (!context) throw new Error('useModems must be used inside ModemProvider')
  return context
}
