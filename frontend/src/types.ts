export type DashboardPeriod = '24h' | '7d' | '30d'

export interface UsageStats {
  requests: number | null
  successful_requests: number | null
  failed_requests: number | null
  success_rate: number | null
  quota: number | null
  prompt_tokens: number | null
  completion_tokens: number | null
  active_users: number | null
  avg_duration_seconds: number | null
}

export interface TrendPoint {
  at: string
  requests: number
  errors: number
  quota: number
}

export interface ModelUsage {
  model: string
  requests: number
  quota: number
  tokens: number
}

export interface DataSourceStatus {
  id: 'main' | 'logs'
  name: string
  status: 'ok' | 'error' | 'demo' | 'unconfigured'
  message: string
}

export interface DashboardData {
  mode: 'demo' | 'live' | 'unconfigured'
  period: DashboardPeriod
  timezone: string
  generated_at: string
  range: { start: string; end: string }
  quota_per_unit: number
  inventory: {
    total_users: number | null
    total_tokens: number | null
    active_tokens: number | null
    total_channels: number | null
    active_channels: number | null
    total_models: number | null
  }
  usage: UsageStats
  previous_usage: UsageStats
  trend: TrendPoint[]
  models: ModelUsage[]
  sources: DataSourceStatus[]
  warnings: string[]
}

export type SystemMeta = {
  mode: 'demo' | 'live' | 'unconfigured'
  auth_required: boolean
  quota_per_unit?: number
  timezone?: string
  finance_configured?: boolean
  probe_configured?: boolean
  newapi_management_configured?: boolean
}

export type OwnerIdentity = {
  id: number
  username: string
  display_name?: string
  role: number
}

export type OwnerSession = {
  authenticated: boolean
  user?: OwnerIdentity
  csrf_token?: string
  mode: 'demo' | 'live' | 'unconfigured'
  login_configured: boolean
  login_source: 'newapi' | 'demo'
  captcha_required?: boolean
  captcha_site_key?: string
  login_ready?: boolean
  message?: string
}

export type OwnerAuthResult = Partial<OwnerSession> & {
  authenticated: boolean
  requires_verification?: boolean
  challenge_id?: string
  methods?: string[]
  message?: string
}
