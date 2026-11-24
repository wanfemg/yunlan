import { createContext, useContext } from 'react'
import './ip-details.css'

export type IPEventSource = 'api_call' | 'login' | 'registration'
export type UserDetailTab = 'overview' | 'usage' | 'login' | 'risk' | 'checkins'
export type IPDetailsTarget = {
  ip: string
  source: IPEventSource
  period?: string
  userId?: number
  tokenId?: number
  start?: number
  end?: number
  view?: 'users' | 'tokens' | 'events' | 'devices' | 'profile'
  page?: number
}
export type IPDetailsActions = {
  openIP: (target: IPDetailsTarget) => void
  openUser: (id: number, username?: string, tab?: UserDetailTab) => void
  openToken: (id: number, name?: string, period?: string, range?: { start: number; end: number }) => void
}

export const IPDetailsContext = createContext<IPDetailsActions | null>(null)

export function useIPDetails() {
  const actions = useContext(IPDetailsContext)
  if (!actions) throw new Error('IPDetailsProvider is required')
  return actions
}
