export type CheckinCategory = 'pure_free' | 'unpaid_usage' | 'reward_only' | 'unused' | 'paid' | 'unknown'

export const checkinCategoryLabels: Record<CheckinCategory, string> = {
  pure_free: '纯白嫖',
  unpaid_usage: '未付费使用',
  reward_only: '只领未用',
  unused: '未充值未使用',
  paid: '已付费',
  unknown: '待核实',
}

export type CheckinSource = {
  id: string
  status: 'ok' | 'demo' | 'unavailable' | 'unsupported' | 'partial' | string
  message: string
  complete: boolean
}

export type CheckinCommon = {
  as_of: number
  start_date: string
  end_date: string
  end_date_exclusive: string
  timezone: string
  quota_per_unit: number
  mode: 'demo' | 'live'
  source_status: CheckinSource[]
  warnings: string[]
}

export type CheckinReview = {
  paid_status: 'unknown' | 'paid' | 'none'
  funding: 'unknown' | 'free_only'
  history_verified: boolean
  reason: string
  updated_at: number
}

export type CheckinReviewInput = Omit<CheckinReview, 'updated_at'>

export type CheckinPayment = {
  wallet_money: string | null
  subscription_money: string | null
  window_wallet_money: string | null
  window_subscription_money: string | null
  paid_count: number | null
  history_complete: boolean
  redemption_count: number | null
  redemption_quota: number | null
  redemption_status: string
}

export type CheckinUser = {
  id: number
  username: string
  display_name: string
  group: string
  status: number
  quota: number
  created_at: number
  last_used_at: number
  checkin_days: number | null
  checkin_count: number | null
  awarded_quota: number | null
  participation_rate: number | null
  participation_basis: string
  lifetime_checkin_days: number | null
  lifetime_checkin_count: number | null
  lifetime_awarded_quota: number | null
  requests: number | null
  consumed_quota: number | null
  lifetime_requests: number | null
  lifetime_consumed_quota: number | null
  last_checkin_date: string
  category: CheckinCategory
  category_reasons: string[]
  payment: CheckinPayment
  review: CheckinReview | null
}

export type CheckinSummaryStats = {
  today_users: number | null
  users: number | null
  records: number | null
  awarded_quota: number | null
  avg_quota_per_user: number | null
  avg_quota_per_checkin: number | null
  active_users: number | null
  active_rate: number | null
  paid_users: number | null
  paid_rate: number | null
  lifetime_users: number | null
  lifetime_records: number | null
  lifetime_awarded_quota: number | null
  pure_free_users: number | null
  pure_free_consumed_quota: number | null
  reward_only_users: number | null
  unknown_users: number | null
}

export type CheckinSummary = CheckinCommon & {
  summary: CheckinSummaryStats
  trend: { date: string; users: number | null; records: number | null; awarded_quota: number | null }[]
  categories: Record<CheckinCategory, number>
  quality: Record<string, number>
}

export type CheckinUsers = CheckinCommon & {
  items: CheckinUser[]
  total: number
  page: number
  page_size: number
  total_pages: number
  totals: {
    users: number
    checkin_days: number | null
    checkin_count: number | null
    awarded_quota: number | null
    requests: number | null
    consumed_quota: number | null
  }
  categories: Record<CheckinCategory, number>
  groups: string[]
}

export type CheckinRecord = {
  id: number
  user_id: number
  checkin_date: string
  quota_awarded: number
  created_at: number
}

export type CheckinUserDetail = CheckinCommon & {
  user: CheckinUser
  month: string
  calendar: { date: string; count: number; awarded_quota: number }[]
  records: CheckinRecord[]
  total: number
  page: number
  page_size: number
  total_pages: number
  current_streak: number | null
  longest_streak: number | null
  streak_complete: boolean
  review: CheckinReview | null
}
