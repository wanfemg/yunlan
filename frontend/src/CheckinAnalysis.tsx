import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import {
  Activity, CalendarCheck, CalendarDays, ChevronDown, ChevronLeft, ChevronRight,
  Coins, Download, Eye, Filter, Gift, Info, Loader2, RefreshCw, Search,
  ShieldCheck, TrendingUp, UserCheck, Users, Wallet, X,
} from 'lucide-react'
import { ApiError, downloadCSV, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, EmptyState, Notice } from './ModuleUI'
import { StatCard } from './components/StatCard'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import type { CheckinCategory, CheckinCommon, CheckinSummary, CheckinUser, CheckinUsers } from './checkin-types'
import { checkinCategoryLabels } from './checkin-types'
import UserCheckinPanel from './UserCheckinPanel'
import { cn } from './lib/utils'
import { UserIdentityButton } from './components/UserIdentityButton'
import './checkin-analysis.css'

const DashboardChart = lazy(() => import('./DashboardChart'))

type WindowPreset = '7' | '30' | '90' | 'custom'
type Filters = {
  period: WindowPreset
  startDate: string
  endDate: string
  scope: 'participants' | 'all'
  category: CheckinCategory | 'all'
  search: string
  group: string
  status: 'all' | 'enabled' | 'disabled'
  sort: 'consumed_quota' | 'awarded_quota' | 'checkin_days' | 'lifetime_consumed_quota' | 'created_at'
  order: 'asc' | 'desc'
  minCheckinDays: string
  maxRequests: string
}
type AppliedFilters = { filters: Filters; asOf: number }
type QuerySnapshot = {
  key: string
  sessionToken: string | null
  summary?: CheckinSummary
  users?: CheckinUsers
  error?: string
  unauthorized?: boolean
}
type Props = { tokenVersion?: number; quotaPerUnit?: number; onUnauthorized: () => void }
type TrendMetric = 'users' | 'records' | 'awarded_quota'

const countFormat = new Intl.NumberFormat('zh-CN')
const categoryKeys: CheckinCategory[] = ['pure_free', 'paid', 'reward_only', 'unpaid_usage', 'unused', 'unknown']
const categoryColors: Record<CheckinCategory, string> = {
  pure_free: 'orange', paid: 'blue', reward_only: 'purple', unpaid_usage: 'amber', unused: 'gray', unknown: 'cyan',
}
const categoryDescriptions: Record<CheckinCategory, string> = {
  pure_free: '免费来源及历史已核实，有额度消耗',
  paid: '全历史存在已确认的付费记录',
  reward_only: '已领免费奖励，完整历史未见调用',
  unpaid_usage: '有已观测消费或零额度调用，付费来源待核实',
  unused: '未确认付费或奖励，完整观测无调用',
  unknown: '来源或历史覆盖不足，待人工核实',
}
const sourceNames: Record<string, string> = {
  checkins: '签到记录', users: '用户资料', main_users: '用户资料', logs: '调用日志', wallet: '钱包付款',
  top_ups: '钱包付款', subscription: '订阅付款', subscriptions: '订阅付款', subscription_orders: '订阅付款',
  redemptions: '兑换码', review: '来源核实', reviews: '来源核实',
}
const sourceStates: Record<string, string> = {
  ok: '可用', demo: '演示', unavailable: '不可用', unsupported: '未提供', partial: '部分可用',
  missing: '缺失', error: '读取失败',
}

function dateInZone(date: Date, timezone = 'Asia/Shanghai') {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit',
  }).formatToParts(date)
  const getPart = (type: string) => parts.find(part => part.type === type)?.value || ''
  return `${getPart('year')}-${getPart('month')}-${getPart('day')}`
}

function createFilters(): Filters {
  const today = dateInZone(new Date())
  const beginning = new Date(`${today}T00:00:00Z`)
  beginning.setUTCDate(beginning.getUTCDate() - 29)
  return {
    period: '30', startDate: beginning.toISOString().slice(0, 10), endDate: today,
    scope: 'participants', category: 'all', search: '', group: '', status: 'all',
    sort: 'consumed_quota', order: 'desc', minCheckinDays: '', maxRequests: '',
  }
}

function count(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '未知' : countFormat.format(value)
}

function quota(value: number | null | undefined, unit: number) {
  if (value == null || !Number.isFinite(value) || !Number.isFinite(unit) || unit <= 0) return '未知'
  const amount = value / unit
  if (amount !== 0 && Math.abs(amount) < 0.00000001) return `${amount < 0 ? '-' : ''}US$<0.00000001`
  return new Intl.NumberFormat('zh-CN', {
    style: 'currency', currency: 'USD', minimumFractionDigits: 2,
    maximumFractionDigits: Math.abs(amount) < 1 ? 8 : 4,
  }).format(amount)
}

function paymentMoney(value: string | null) {
  if (value == null) return '未知'
  const match = /^([+-]?)(\d+)(?:\.(\d+))?$/.exec(value)
  if (!match) return value
  const fraction = (match[3] || '').replace(/0+$/, '').padEnd(2, '0')
  return `${match[1]}${match[2].replace(/\B(?=(\d{3})+(?!\d))/g, ',')}.${fraction}`
}

function percent(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return '未知'
  return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 }).format(value * 100)}%`
}

function timestamp(value: number | null | undefined, timezone: string) {
  if (!value || !Number.isFinite(value)) return '未观测'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).format(value * 1000)
}

function rangeParams(applied: AppliedFilters) {
  const params = new URLSearchParams({ as_of: String(applied.asOf) })
  if (applied.filters.period === 'custom') {
    params.set('start_date', applied.filters.startDate)
    params.set('end_date', applied.filters.endDate)
  } else params.set('days', applied.filters.period)
  return params
}

function queryParams(applied: AppliedFilters, page?: number, pageSize?: number) {
  const params = rangeParams(applied)
  const filters = applied.filters
  params.set('scope', filters.scope)
  params.set('category', filters.category)
  params.set('status', filters.status)
  params.set('sort', filters.sort)
  params.set('order', filters.order)
  for (const [key, value] of [
    ['search', filters.search], ['group', filters.group],
    ['min_checkin_days', filters.minCheckinDays], ['max_requests', filters.maxRequests],
  ]) if (value.trim()) params.set(key, value.trim())
  if (page != null) params.set('page', String(page))
  if (pageSize != null) params.set('page_size', String(pageSize))
  return params
}

function validateCommon(value: CheckinCommon) {
  if (!value || !Number.isFinite(value.as_of) || !Number.isFinite(value.quota_per_unit) || value.quota_per_unit <= 0 ||
    !value.start_date || !value.end_date || !Array.isArray(value.source_status) || !Array.isArray(value.warnings)) {
    throw new ApiError('签到统计返回格式无效，请重新读取。', 503)
  }
  new Intl.DateTimeFormat('zh-CN', { timeZone: value.timezone }).format()
}

function CategoryBadge({ category }: { category: CheckinCategory }) {
  return <Badge variant={category === 'paid' ? 'success' : category === 'unknown' ? 'outline' : 'secondary'}
    className={cn('font-normal', category === 'pure_free' && 'border-orange-200 bg-orange-50 text-orange-700 dark:border-orange-900 dark:bg-orange-950/30 dark:text-orange-300')}>
    {checkinCategoryLabels[category] ?? '待核实'}
  </Badge>
}

export default function CheckinAnalysis({ tokenVersion = 0, quotaPerUnit = 500000, onUnauthorized }: Props) {
  const [draft, setDraft] = useState<Filters>(createFilters)
  const [applied, setApplied] = useState<AppliedFilters>(() => ({ filters: createFilters(), asOf: Math.floor(Date.now() / 1000) }))
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [revision, setRevision] = useState(0)
  const [snapshot, setSnapshot] = useState<QuerySnapshot | null>(null)
  const [formError, setFormError] = useState('')
  const [actionError, setActionError] = useState('')
  const [exporting, setExporting] = useState(false)
  const [selected, setSelected] = useState<CheckinUser | null>(null)
  const [showFilters, setShowFilters] = useState(false)
  const [trendMetric, setTrendMetric] = useState<TrendMetric>('users')
  const serial = useRef(0)
  const exportAbort = useRef<AbortController | null>(null)
  const unauthorized = useRef(onUnauthorized)
  unauthorized.current = onUnauthorized
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const query = queryParams(applied, page, pageSize).toString()
  const rangeQuery = rangeParams(applied).toString()
  const queryKey = JSON.stringify([tokenVersion, query, revision])
  const latestKey = useRef(queryKey)
  latestKey.current = queryKey
  const latestSession = useRef(sessionToken)
  latestSession.current = sessionToken

  useEffect(() => {
    const controller = new AbortController()
    const requestSerial = ++serial.current
    const isCurrent = () => !controller.signal.aborted && serial.current === requestSerial &&
      latestKey.current === queryKey && latestSession.current === sessionToken &&
      sessionStorage.getItem('siteadmin-token') === sessionToken
    if (UI_PREVIEW_ONLY) { setSnapshot(null); return () => controller.abort() }
    setSnapshot(null)
    void Promise.all([
      requestJSON<CheckinSummary>(`/api/checkins/summary?${query}`, {}, controller.signal),
      requestJSON<CheckinUsers>(`/api/checkins/users?${query}`, {}, controller.signal),
    ]).then(([summary, users]) => {
      if (!isCurrent()) return
      validateCommon(summary)
      validateCommon(users)
      if (!summary.summary || !Array.isArray(summary.trend) || !summary.categories || !Array.isArray(users.items) ||
        !Number.isFinite(users.total) || !Number.isFinite(users.total_pages) || !Array.isArray(users.groups) ||
        summary.start_date !== users.start_date || summary.end_date !== users.end_date ||
        summary.as_of !== users.as_of) throw new ApiError('签到汇总与用户列表的统计范围不一致，请重新读取。', 503)
      if (users.total_pages > 0 && page > users.total_pages) { setPage(users.total_pages); return }
      setSnapshot({ key: queryKey, sessionToken, summary, users })
    }).catch(cause => {
      if (!isCurrent()) return
      const expired = cause instanceof ApiError && cause.status === 401
      setSnapshot({
        key: queryKey, sessionToken, unauthorized: expired,
        error: expired ? '管理员令牌已失效，请重新连接。' : cause instanceof Error ? cause.message : '暂时无法读取签到统计。',
      })
      if (expired) unauthorized.current()
    })
    return () => controller.abort()
  }, [queryKey, query, sessionToken, page])

  useEffect(() => {
    exportAbort.current?.abort()
    setExporting(false)
    setSelected(null)
    setActionError('')
  }, [tokenVersion, sessionToken])
  useEffect(() => () => exportAbort.current?.abort(), [])

  const current = snapshot?.key === queryKey && snapshot.sessionToken === sessionToken ? snapshot : null
  const summary = UI_PREVIEW_ONLY ? undefined : current?.summary
  const users = UI_PREVIEW_ONLY ? undefined : current?.users
  const busy = !UI_PREVIEW_ONLY && !current
  const stats = summary?.summary
  const unit = summary?.quota_per_unit ?? quotaPerUnit
  const timezone = summary?.timezone ?? 'Asia/Shanghai'
  const pages = Math.max(1, users?.total_pages ?? 1)
  const groups = [...new Set([...(users?.groups ?? []), ...(draft.group ? [draft.group] : [])])].sort()
  const warnings = [...new Set([...(summary?.warnings ?? []), ...(users?.warnings ?? [])])]
  const chartMetric = {
    users: { label: '签到人数', unit: '人', color: '#3b82f6' },
    records: { label: '签到人次', unit: '次', color: '#8b5cf6' },
    awarded_quota: { label: '奖励额度', unit: 'USD', color: '#f59e0b' },
  }[trendMetric]
  const chartOption = useMemo(() => ({
    animationDuration: 300,
    backgroundColor: 'transparent',
    color: [chartMetric.color],
    tooltip: {
      trigger: 'axis', renderMode: 'richText',
      valueFormatter: (value: number | null) => value == null ? '未知' : trendMetric === 'awarded_quota'
        ? quota(value * unit, unit) : `${count(value)} ${chartMetric.unit}`,
    },
    grid: { top: 20, right: 22, bottom: 25, left: 12, containLabel: true },
    xAxis: {
      type: 'category', boundaryGap: false, data: summary?.trend.map(point => point.date.slice(5)) ?? [],
      axisLine: { lineStyle: { color: '#cbd5e1' } }, axisTick: { show: false },
      axisLabel: { color: '#64748b', fontSize: 11, hideOverlap: true },
    },
    yAxis: {
      type: 'value', minInterval: trendMetric === 'awarded_quota' ? undefined : 1,
      axisLabel: { color: '#64748b', fontSize: 11 },
      splitLine: { lineStyle: { color: '#e2e8f0', type: 'dashed' } },
    },
    series: [{
      name: chartMetric.label, type: 'line', smooth: .2, symbol: 'circle', symbolSize: 5,
      connectNulls: false, lineStyle: { width: 2 }, areaStyle: { opacity: .08 },
      data: summary?.trend.map(point => point[trendMetric] == null ? null
        : trendMetric === 'awarded_quota' ? point.awarded_quota! / unit : point[trendMetric]) ?? [],
    }],
  }), [summary, trendMetric, chartMetric.color, chartMetric.label, chartMetric.unit, unit])

  function applyFilters(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY) return
    if (draft.period === 'custom' && (!/^\d{4}-\d{2}-\d{2}$/.test(draft.startDate) ||
      !/^\d{4}-\d{2}-\d{2}$/.test(draft.endDate) || draft.startDate > draft.endDate)) {
      setFormError('请选择有效的开始日期和结束日期，开始日期不能晚于结束日期。')
      return
    }
    for (const [label, value] of [['最少签到天数', draft.minCheckinDays], ['最多调用次数', draft.maxRequests]]) {
      if (value !== '' && (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)))) {
        setFormError(`${label}需要填写大于或等于 0 的整数。`)
        return
      }
    }
    setFormError('')
    setActionError('')
    setPage(1)
    setApplied({ filters: { ...draft }, asOf: Math.floor(Date.now() / 1000) })
    setRevision(value => value + 1)
  }

  function resetFilters() {
    const filters = createFilters()
    setDraft(filters)
    setApplied({ filters, asOf: Math.floor(Date.now() / 1000) })
    setPage(1)
    setFormError('')
    setActionError('')
    setRevision(value => value + 1)
  }

  const refresh = useCallback(() => {
    setApplied(value => ({ ...value, asOf: Math.floor(Date.now() / 1000) }))
    setRevision(value => value + 1)
    setActionError('')
  }, [])

  function chooseCategory(category: CheckinCategory | 'all') {
    if (UI_PREVIEW_ONLY || busy) return
    setDraft(value => ({ ...value, category }))
    setApplied(value => ({ ...value, filters: { ...value.filters, category } }))
    setPage(1)
    setFormError('')
  }

  async function exportUsers() {
    if (UI_PREVIEW_ONLY || exporting || !users) return
    const controller = new AbortController()
    exportAbort.current?.abort()
    exportAbort.current = controller
    const exportedSession = sessionStorage.getItem('siteadmin-token')
    setExporting(true)
    setActionError('')
    try {
      await downloadCSV(`/api/checkins/export?${queryParams(applied)}`, `签到分析-${users.start_date}-${users.end_date}.csv`, controller.signal)
    } catch (cause) {
      if (controller.signal.aborted || sessionStorage.getItem('siteadmin-token') !== exportedSession) return
      if (cause instanceof ApiError && cause.status === 401) unauthorized.current()
      else setActionError(cause instanceof Error ? cause.message : '导出签到统计失败。')
    } finally {
      if (exportAbort.current === controller) setExporting(false)
    }
  }

  const metricCards = [
    { title: '今日签到用户', value: count(stats?.today_users), detail: '按实例时区自然日', icon: CalendarCheck, color: 'blue' },
    { title: '窗口签到人数', value: count(stats?.users), detail: '窗口内去重用户', icon: Users, color: 'emerald' },
    { title: '窗口签到人次', value: count(stats?.records), detail: '成功签到记录数量', icon: CalendarDays, color: 'purple' },
    { title: '窗口发放额度', value: quota(stats?.awarded_quota, unit), detail: '配额价值，不是现金支出', icon: Gift, color: 'orange' },
    { title: '人均奖励额度', value: quota(stats?.avg_quota_per_user, unit), detail: `次均 ${quota(stats?.avg_quota_per_checkin, unit)}`, icon: Coins, color: 'amber' },
    { title: '有调用的签到用户', value: count(stats?.active_users), detail: `窗口调用占比 ${percent(stats?.active_rate)}`, icon: Activity, color: 'cyan' },
    { title: '已付费签到用户', value: count(stats?.paid_users), detail: `全历史付款判断 · ${percent(stats?.paid_rate)}`, icon: Wallet, color: 'indigo' },
    { title: '纯白嫖用户消耗', value: quota(stats?.pure_free_consumed_quota, unit), detail: `${count(stats?.pure_free_users)} 人 · 免费来源已核实`, icon: TrendingUp, color: 'rose' },
  ]

  return <div className="checkin-analysis space-y-6 animate-in fade-in duration-500">
    <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-start">
      <div><h1 className="text-3xl font-bold">签到分析</h1><p className="mt-1 text-muted-foreground">签到活跃、奖励发放、实际消耗与额度来源核实</p></div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy || !users || exporting} onClick={() => void exportUsers()}>
          {exporting ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Download className="mr-2 h-4 w-4" />}导出 CSV
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy} onClick={refresh}><RefreshCw className={cn('mr-2 h-4 w-4', busy && 'animate-spin')} />刷新</Button>
      </div>
    </div>

    <Card>
      <CardContent className="pt-5">
        <form onSubmit={applyFilters} className="space-y-4">
          <div className="flex flex-wrap items-end gap-3">
            <label className="checkin-field w-36"><span>统计周期</span><Select aria-label="签到统计周期" value={draft.period} onChange={event => setDraft(value => ({ ...value, period: event.target.value as WindowPreset }))}>
              <option value="7">近 7 天</option><option value="30">近 30 天</option><option value="90">近 90 天</option><option value="custom">自定义日期</option>
            </Select></label>
            {draft.period === 'custom' && <>
              <label className="checkin-field"><span>开始日期</span><Input type="date" aria-label="签到开始日期" value={draft.startDate} max={draft.endDate || undefined} onChange={event => setDraft(value => ({ ...value, startDate: event.target.value }))} required /></label>
              <label className="checkin-field"><span>结束日期（含当天）</span><Input type="date" aria-label="签到结束日期" value={draft.endDate} min={draft.startDate || undefined} onChange={event => setDraft(value => ({ ...value, endDate: event.target.value }))} required /></label>
            </>}
            <label className="checkin-field min-w-44"><span>统计范围</span><Select aria-label="签到统计范围" value={draft.scope} onChange={event => setDraft(value => ({ ...value, scope: event.target.value as Filters['scope'] }))}>
              <option value="participants">窗口内签到用户</option><option value="all">全部用户</option>
            </Select></label>
            <Button type="submit" disabled={UI_PREVIEW_ONLY || busy}><Filter className="mr-2 h-4 w-4" />应用范围与筛选</Button>
          </div>
          {formError && <Notice kind="error">{formError}</Notice>}
          <div className="checkin-filter-row">
            <label className="checkin-field checkin-search"><span className="sr-only">搜索用户</span><div className="relative"><Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" /><Input aria-label="签到用户搜索" className="pl-9" value={draft.search} maxLength={128} placeholder="搜索账号 ID、用户名或昵称..." onChange={event => setDraft(value => ({ ...value, search: event.target.value }))} /></div></label>
            <label className="checkin-field"><span className="sr-only">用户分类</span><Select aria-label="签到用户分类" value={draft.category} onChange={event => setDraft(value => ({ ...value, category: event.target.value as Filters['category'] }))}>
              <option value="all">全部分类</option>{categoryKeys.map(category => <option key={category} value={category}>{checkinCategoryLabels[category]}</option>)}
            </Select></label>
            <label className="checkin-field"><span className="sr-only">用户状态</span><Select aria-label="签到用户状态" value={draft.status} onChange={event => setDraft(value => ({ ...value, status: event.target.value as Filters['status'] }))}>
              <option value="all">全部账号状态</option><option value="enabled">启用</option><option value="disabled">已封禁</option>
            </Select></label>
            <Button type="button" variant="outline" onClick={() => setShowFilters(value => !value)} aria-expanded={showFilters}><Filter className="mr-2 h-4 w-4" />更多筛选<ChevronDown className={cn('ml-1 h-3 w-3 transition-transform', showFilters && 'rotate-180')} /></Button>
          </div>
          {showFilters && <div className="checkin-more-filters">
            <label className="checkin-field"><span>用户分组</span><Select aria-label="签到用户分组" value={draft.group} onChange={event => setDraft(value => ({ ...value, group: event.target.value }))}><option value="">全部分组</option>{groups.map(group => <option key={group} value={group}>{group}</option>)}</Select></label>
            <label className="checkin-field"><span>排序依据</span><Select aria-label="签到排行排序依据" value={draft.sort} onChange={event => setDraft(value => ({ ...value, sort: event.target.value as Filters['sort'] }))}><option value="consumed_quota">窗口实际消耗</option><option value="awarded_quota">窗口奖励额度</option><option value="checkin_days">窗口签到天数</option><option value="lifetime_consumed_quota">累计实际消耗</option><option value="created_at">账号注册时间</option></Select></label>
            <label className="checkin-field"><span>排序方向</span><Select aria-label="签到排行排序方向" value={draft.order} onChange={event => setDraft(value => ({ ...value, order: event.target.value as Filters['order'] }))}><option value="desc">从高到低 / 从新到旧</option><option value="asc">从低到高 / 从旧到新</option></Select></label>
            <label className="checkin-field"><span>窗口最少签到天数</span><Input aria-label="最少签到天数" type="number" min={0} step={1} placeholder="不限" value={draft.minCheckinDays} onChange={event => setDraft(value => ({ ...value, minCheckinDays: event.target.value }))} /></label>
            <label className="checkin-field"><span>窗口最多调用次数</span><Input aria-label="最多调用次数" type="number" min={0} step={1} placeholder="不限" value={draft.maxRequests} onChange={event => setDraft(value => ({ ...value, maxRequests: event.target.value }))} /></label>
            <div className="flex items-end"><Button type="button" variant="ghost" onClick={resetFilters} disabled={UI_PREVIEW_ONLY || busy}><X className="mr-2 h-4 w-4" />重置全部筛选</Button></div>
          </div>}
        </form>
        <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
          <p>{summary ? `${summary.start_date} 至 ${summary.end_date}（含当天） · ${summary.timezone}` : UI_PREVIEW_ONLY ? '数据获取已暂停' : '统计日期将在读取后显示'}</p>
          <p>{summary ? `统计于 ${timestamp(summary.as_of, summary.timezone)}` : busy ? '正在读取...' : ''}</p>
        </div>
      </CardContent>
    </Card>

    {current?.error && <Notice kind="error"><div className="flex flex-wrap items-center gap-3"><span>{current.error}</span>{!current.unauthorized && <Button type="button" variant="outline" size="sm" onClick={refresh}>重新读取</Button>}</div></Notice>}
    {actionError && <Notice kind="error">{actionError}</Notice>}
    {warnings.map(warning => <Notice key={warning} kind="warning">{warning}</Notice>)}

    <div className="grid grid-cols-1 gap-4 xs:grid-cols-2 xl:grid-cols-4">
      {metricCards.map(card => <StatCard key={card.title} title={card.title} value={card.value} subValue={card.detail} icon={card.icon} color={card.color} />)}
    </div>

    <section className="space-y-3" aria-label="签到用户分类">
      <div className="flex flex-wrap items-center justify-between gap-2"><h2 className="flex items-center gap-2 text-base font-semibold"><UserCheck className="h-4 w-4 text-primary" />用户分类</h2><Button variant="ghost" size="sm" type="button" disabled={UI_PREVIEW_ONLY || busy || applied.filters.category === 'all'} onClick={() => chooseCategory('all')}>显示全部</Button></div>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">{categoryKeys.map(category => <StatCard key={category} title={checkinCategoryLabels[category]} value={summary ? count(summary.categories[category]) : '—'} icon={category === 'paid' ? Wallet : category === 'unknown' ? Info : Users} color={categoryColors[category]} variant="compact"
        className={cn('checkin-category-card', applied.filters.category === category && 'ring-2 ring-primary ring-offset-2')}
        onClick={UI_PREVIEW_ONLY || busy ? undefined : () => chooseCategory(category)} />)}</div>
      <p className="text-xs leading-relaxed text-muted-foreground">{applied.filters.category === 'all' ? '分类数量沿用当前范围与基础筛选，点击卡片查看相应用户。' : categoryDescriptions[applied.filters.category]} 付费判断基于全历史钱包与订阅付款；来源或覆盖未知时显示待核实，不按未付费处理。</p>
    </section>

    <Card>
      <CardHeader><div className="flex flex-wrap items-center justify-between gap-3"><div><CardTitle className="flex items-center gap-2 text-lg"><TrendingUp className="h-5 w-5 text-primary" />每日签到趋势</CardTitle><CardDescription className="mt-1.5">人数按当天用户去重，人次保留成功签到记录；奖励额度按实例换算。</CardDescription></div>
        <div className="inline-flex rounded-lg border bg-muted/50 p-1" role="group" aria-label="签到趋势指标">{([['users', '人数'], ['records', '人次'], ['awarded_quota', '奖励额度']] as const).map(([value, label]) => <Button type="button" key={value} variant={trendMetric === value ? 'default' : 'ghost'} size="sm" className="h-7 px-3 text-xs" aria-pressed={trendMetric === value} onClick={() => setTrendMetric(value)}>{label}</Button>)}</div>
      </div></CardHeader>
      <CardContent><div className="h-64 min-w-0 sm:h-72">
        {busy ? <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="h-6 w-6 animate-spin text-primary" />正在读取趋势...</div>
          : summary?.trend.length ? <Suspense fallback={<div className="flex h-full items-center justify-center text-muted-foreground"><Loader2 className="h-6 w-6 animate-spin" /></div>}><DashboardChart option={chartOption} /></Suspense>
            : <EmptyState title={UI_PREVIEW_ONLY ? '数据获取已暂停' : current?.error ? '趋势数据暂不可用' : '窗口内暂无签到记录'} />}
      </div></CardContent>
    </Card>

    <Card className="checkin-users-card">
      <CardHeader><div className="flex flex-wrap items-center justify-between gap-3"><div><CardTitle className="flex items-center gap-2 text-lg"><Users className="h-5 w-5 text-primary" />用户签到与消耗排行<Badge variant="secondary" className="font-normal">{users ? `${count(users.total)} 个` : '—'}</Badge></CardTitle><CardDescription className="mt-1.5">点击用户打开当前账号的签到日历、奖励记录与来源核实。</CardDescription></div>{applied.filters.category !== 'all' && <Button type="button" variant="outline" size="sm" onClick={() => chooseCategory('all')} disabled={busy}><X className="mr-1 h-3 w-3" />{checkinCategoryLabels[applied.filters.category]}</Button>}</div></CardHeader>
      <CardContent className="space-y-4">
        <Table className="checkin-users-table" aria-label="签到用户排行" containerClassName="scroll-region scroll-region--lg" containerLabel="签到用户排行列表">
          <TableHeader><TableRow>
            <TableHead>用户</TableHead><TableHead>分类 / 来源</TableHead><TableHead className="text-right">窗口签到</TableHead><TableHead className="text-right">窗口奖励</TableHead><TableHead className="text-right">窗口调用 / 消耗</TableHead><TableHead className="text-right">累计消耗</TableHead><TableHead>全历史付款</TableHead><TableHead>最近记录</TableHead><TableHead className="text-right">操作</TableHead>
          </TableRow></TableHeader>
          <TableBody>{users?.items.length ? users.items.map(user => <TableRow key={user.id}>
            <TableCell><UserIdentityButton userId={user.id} username={user.username} displayName={user.display_name} group={user.group} onClick={() => setSelected(user)} /><Badge variant={user.status === 1 ? 'success' : user.status === 2 ? 'destructive' : 'outline'} className="mt-2 font-normal">{user.status === 1 ? '启用' : user.status === 2 ? '已封禁' : '状态未知'}</Badge></TableCell>
            <TableCell className="max-w-60"><CategoryBadge category={user.category} /><p className="mt-2 max-w-52 text-xs leading-relaxed text-muted-foreground">{user.category_reasons?.[0] || categoryDescriptions[user.category]}</p>{user.review && <span className="mt-2 inline-flex items-center gap-1 text-xs text-muted-foreground"><ShieldCheck className="h-3 w-3" />已保存人工核实</span>}</TableCell>
            <TableCell className="text-right"><div className="font-mono text-sm">{count(user.checkin_days)} 天</div><div className="mt-1 text-xs text-muted-foreground">{count(user.checkin_count)} 人次</div><div className="mt-1 text-xs text-muted-foreground">参与率 {percent(user.participation_rate)}</div></TableCell>
            <TableCell className="text-right font-mono text-xs whitespace-nowrap">{quota(user.awarded_quota, unit)}</TableCell>
            <TableCell className="text-right"><div className="font-mono text-xs whitespace-nowrap">{quota(user.consumed_quota, unit)}</div><div className="mt-1 text-xs text-muted-foreground">{count(user.requests)} 次请求</div></TableCell>
            <TableCell className="text-right"><div className="font-mono text-xs whitespace-nowrap">{quota(user.lifetime_consumed_quota, unit)}</div><div className="mt-1 text-xs text-muted-foreground">{count(user.lifetime_requests)} 次请求</div></TableCell>
            <TableCell className="text-xs"><div className="whitespace-nowrap">钱包 {paymentMoney(user.payment.wallet_money)}</div><div className="mt-1 whitespace-nowrap">订阅 {paymentMoney(user.payment.subscription_money)}</div><div className="mt-1 text-muted-foreground">订单原金额 · 币种未记录</div><div className="mt-1 text-muted-foreground">{user.payment.history_complete ? '付款历史已确认完整' : '付款历史待核实'}</div></TableCell>
            <TableCell className="text-xs text-muted-foreground"><div className="whitespace-nowrap">签到 {user.last_checkin_date || '未观测'}</div><div className="mt-1 whitespace-nowrap">调用 {timestamp(user.last_used_at, timezone)}</div><div className="mt-1">余额 {quota(user.quota, unit)}</div></TableCell>
            <TableCell className="text-right"><Button type="button" variant="ghost" size="sm" className="h-8 px-2" onClick={() => setSelected(user)}><Eye className="mr-1 h-4 w-4" />详情</Button></TableCell>
          </TableRow>) : <TableRow><TableCell colSpan={9} className="h-44">
            {busy ? <div className="flex items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="h-6 w-6 animate-spin text-primary" />正在读取用户统计...</div>
              : <EmptyState title={UI_PREVIEW_ONLY ? '数据获取已暂停' : current?.error ? '用户统计暂不可用' : '暂无符合条件的用户'} action={!UI_PREVIEW_ONLY && !current?.error ? <Button type="button" variant="ghost" size="sm" onClick={resetFilters}>重置筛选</Button> : undefined} />}
          </TableCell></TableRow>}</TableBody>
        </Table>
        <div className="checkin-pagination">
          <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground"><span>共 {users ? count(users.total) : '—'} 个用户</span><div className="flex items-center gap-2"><span>每页</span><div className="w-20"><Select aria-label="签到用户每页数量" value={String(pageSize)} disabled={UI_PREVIEW_ONLY || busy} onChange={event => { setPageSize(Number(event.target.value)); setPage(1) }}><option value="10">10</option><option value="20">20</option><option value="50">50</option><option value="100">100</option></Select></div><span>条</span></div></div>
          <div className="flex items-center gap-3 text-sm text-muted-foreground"><span>第 {users ? page : '—'} / {users ? pages : '—'} 页</span><Button type="button" variant="outline" size="sm" aria-label="签到用户上一页" disabled={!users || busy || page <= 1} onClick={() => setPage(value => value - 1)}><ChevronLeft className="h-4 w-4" /></Button><Button type="button" variant="outline" size="sm" aria-label="签到用户下一页" disabled={!users || busy || page >= pages} onClick={() => setPage(value => value + 1)}><ChevronRight className="h-4 w-4" /></Button></div>
        </div>
        <p className="text-xs leading-relaxed text-muted-foreground">实际消耗来自调用日志；当前余额包含多种来源，不等于可提现资金。奖励与消耗展示 USD 配额面值，付款栏保留订单原金额，原币种未记录，不用于计算实际到账或亏损。没有完整资金归因账本时，不把全部消费归因于签到奖励，也不以未付费分类直接判定风险。</p>
      </CardContent>
    </Card>

    {summary && <Card><CardHeader><CardTitle className="flex items-center gap-2 text-base"><Info className="h-4 w-4 text-primary" />数据来源与口径</CardTitle><CardDescription>签到、调用与付款分别读取，缺失来源显示未知。</CardDescription></CardHeader><CardContent className="space-y-4">
      <div className="grid gap-3 md:grid-cols-2">{summary.source_status.map(source => <div key={source.id} className="flex items-start justify-between gap-3 rounded-lg border bg-muted/20 p-3"><div className="min-w-0"><p className="text-sm font-medium">{sourceNames[source.id] || source.id}</p><p className="mt-1 break-words text-xs leading-relaxed text-muted-foreground">{source.message}</p></div><Badge variant={source.status === 'unavailable' || source.status === 'partial' ? 'warning' : 'outline'} className="shrink-0">{sourceStates[source.status] || source.status}</Badge></div>)}</div>
      <div className="space-y-2 border-t pt-3 text-xs text-muted-foreground"><p>当前用户范围内历史累计</p><div className="flex flex-wrap gap-x-5 gap-y-2"><span>签到用户 {count(stats?.lifetime_users)}</span><span>签到 {count(stats?.lifetime_records)} 人次</span><span>发放 {quota(stats?.lifetime_awarded_quota, unit)}</span></div></div>
      <p className="text-xs leading-relaxed text-muted-foreground">“纯白嫖”仅用于已核实免费来源与完整历史的账号画像。来源核实只保存本站审核信息，不修改 NewAPI 余额、签到或付款记录。</p>
    </CardContent></Card>}

    {!UI_PREVIEW_ONLY && selected && <AdminDialog title={`签到分析 · ${selected.username || `用户 #${selected.id}`}`} onClose={() => setSelected(null)} wide>
      <UserCheckinPanel key={`${selected.id}:${tokenVersion}`} userId={selected.id} username={selected.username}
        tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} rangeQuery={rangeQuery} refreshVersion={revision}
        onReviewSaved={refresh} />
    </AdminDialog>}
  </div>
}
