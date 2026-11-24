// Model statistics follow new_api_tools' Analytics.tsx conventions.
// MIT License; see tools-reference-LICENSE.txt.
import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity, CheckCircle2, ChevronLeft, ChevronRight, Info,
  Loader2, MessageSquareWarning, Percent,
  RefreshCw, RotateCcw, Search, Timer, XCircle,
} from 'lucide-react'
import { ApiError, get, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, Notice } from './ModuleUI'
import RiskUserCard from './RiskUserCard'
import ModelStatusTrend from './ModelStatusTrend'
import { UserIdentityButton } from './components/UserIdentityButton'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import type { DashboardPeriod } from './types'
import './log-analysis.css'

type ModelHealth = 'healthy' | 'warning' | 'critical' | 'unknown'
type ModelSort = 'request_count' | 'success_rate' | 'empty_rate' | 'failure_count'
type Filters = {
  period: DashboardPeriod
  model: string
  channel_id: string
  health: ModelHealth | 'all'
  sort: ModelSort
  order: 'desc' | 'asc'
}
type ModelCounts = {
  total_requests: number
  success_count: number
  failure_count: number
  empty_count: number | null
  known_empty_count: number
  empty_unknown_count: number
  success_rate: number | null
  empty_rate: number | null
}
type ModelStatistics = ModelCounts & { model_name: string; health: ModelHealth }
type ModelSummary = ModelCounts & {
  model_count: number
  healthy_models: number
  warning_models: number
  critical_models: number
  unknown_models: number
}
type UserRank = { user_id: number; username: string; count: number; quota: number }
type Analysis = {
  mode: 'demo' | 'live'
  period: DashboardPeriod
  timezone: string
  generated_at: string
  range: { start: string; end: string }
  quota_per_unit: number
  summary: ModelSummary
  models: ModelStatistics[]
  total_models: number
  page: number
  page_size: number
  trend: (ModelCounts & { at: string })[]
  rankings: { requests: UserRank[]; quota: UserRank[] }
  empty_detection_available: boolean
  data_notes: string[]
}
type Result = { key: string; scope: string; data?: Analysis; error?: string; unauthorized?: boolean }
type UserTarget = { id: number; username?: string }
const defaultFilters: Filters = {
  period: '30d', model: '', channel_id: '', health: 'all', sort: 'request_count', order: 'desc',
}
const counts = new Intl.NumberFormat('zh-CN')
const percentages = new Intl.NumberFormat('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 })
const money = new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 })
const intervals = [
  { seconds: 0, label: '关闭自动刷新' }, { seconds: 30, label: '每 30 秒' },
  { seconds: 60, label: '每 60 秒' }, { seconds: 120, label: '每 2 分钟' },
  { seconds: 300, label: '每 5 分钟' },
]
const healthLabels: Record<ModelHealth, string> = {
  healthy: '正常', warning: '需关注', critical: '异常', unknown: '数据不足',
}
const periods: Record<DashboardPeriod, string> = { '24h': '最近 24 小时', '7d': '近 7 天', '30d': '近 30 天' }
const PAGE_SIZE = 20

function number(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : counts.format(value)
}
function rate(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : `${percentages.format(value)}%`
}
function rateClass(value: number | null, empty = false) {
  if (value == null) return 'text-muted-foreground'
  if (empty) return value <= 5 ? 'text-green-600' : value <= 15 ? 'text-yellow-600' : 'text-red-600'
  return value >= 95 ? 'text-green-600' : value >= 80 ? 'text-yellow-600' : 'text-red-600'
}
function formatQuota(value: number, quotaPerUnit: number) {
  return quotaPerUnit > 0 ? money.format(value / quotaPerUnit) : number(value)
}
function RankBadge({ rank }: { rank: number }) {
  const colors: Record<number, string> = {
    1: 'bg-yellow-400 text-yellow-900', 2: 'bg-gray-300 text-gray-700', 3: 'bg-orange-300 text-orange-800',
  }
  return <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full text-xs ${colors[rank] ? `font-bold ${colors[rank]}` : 'text-muted-foreground'}`}>{rank}</span>
}
function UserRankTable({ id, title, metric, entries, quotaPerUnit, emptyMessage, onViewUser }: {
  id: string; title: string; metric: 'count' | 'quota'; entries: UserRank[]
  quotaPerUnit: number; emptyMessage: string; onViewUser: (user: UserTarget) => void
}) {
  return <Card className="min-w-0" aria-labelledby={id}>
    <CardHeader><CardTitle id={id} className="text-lg">{title} <span className="text-sm font-normal text-muted-foreground">Top 10</span></CardTitle>
      <p className="text-xs text-muted-foreground">与当前模型、渠道和时间范围一致，点击账号查看详情。</p></CardHeader>
    <CardContent className="analytics-ranking-content">
      <Table aria-labelledby={id} className="analytics-ranking-table" containerClassName="scroll-region scroll-region--lg" containerLabel={title}>
        <TableHeader><TableRow><TableHead className="w-16">排名</TableHead><TableHead>用户</TableHead><TableHead className="text-right">{metric === 'quota' ? '消耗额度' : '请求数'}</TableHead></TableRow></TableHeader>
        <TableBody>{entries.map((entry, index) => <TableRow key={entry.user_id}>
          <TableCell><RankBadge rank={index + 1} /></TableCell>
          <TableCell><UserIdentityButton userId={entry.user_id} username={entry.username} compact onClick={() => onViewUser({ id: entry.user_id, username: entry.username })} /></TableCell>
          <TableCell className={`whitespace-nowrap text-right font-semibold ${metric === 'quota' ? 'text-green-600' : ''}`}>{metric === 'quota' ? formatQuota(entry.quota, quotaPerUnit) : number(entry.count)}</TableCell>
        </TableRow>)}</TableBody>
      </Table>
      {!entries.length && <div className="flex min-h-36 items-center justify-center text-center text-sm text-muted-foreground" role="status">{emptyMessage}</div>}
    </CardContent>
  </Card>
}
function validateAnalysis(value: Analysis) {
  if (!value || !value.summary || !Array.isArray(value.models) || !Array.isArray(value.trend) ||
    !value.rankings || !Array.isArray(value.rankings.requests) || !Array.isArray(value.rankings.quota) ||
    !Number.isSafeInteger(value.total_models) || value.total_models < 0 ||
    !Number.isInteger(value.page) || value.page < 1 || !Number.isInteger(value.page_size) || value.page_size < 1 ||
    !Number.isFinite(Date.parse(value.generated_at))) throw new Error('模型状态统计返回不完整，请重新读取。')
  const validCount = (count: number) => Number.isSafeInteger(count) && count >= 0
  const validCounts = (entry: ModelCounts) => validCount(entry.total_requests) &&
    validCount(entry.success_count) && validCount(entry.failure_count) &&
    entry.total_requests === entry.success_count + entry.failure_count &&
    validCount(entry.known_empty_count) && validCount(entry.empty_unknown_count) &&
    entry.known_empty_count + entry.empty_unknown_count <= entry.success_count &&
    (entry.empty_count === null || validCount(entry.empty_count) && entry.empty_count <= entry.success_count) &&
    [entry.success_rate, entry.empty_rate].every(rate => rate === null || Number.isFinite(rate) && rate >= 0 && rate <= 100)
  if (!validCounts(value.summary) || value.models.some(model => typeof model.model_name !== 'string' ||
    !validCounts(model) || !Object.hasOwn(healthLabels, model.health)) ||
    value.trend.some(point => !Number.isFinite(Date.parse(point.at)) || !validCounts(point))) {
    throw new Error('模型统计指标返回异常，请重新读取。')
  }
  new Intl.DateTimeFormat('zh-CN', { timeZone: value.timezone }).format()
}

export default function LogAnalysis({ tokenVersion, onUnauthorized }: { tokenVersion: number; onUnauthorized: () => void }) {
  const [draft, setDraft] = useState<Filters>(defaultFilters)
  const [query, setQuery] = useState({ filters: defaultFilters, page: 1, revision: 0 })
  const [result, setResult] = useState<Result | null>(null)
  const [interval, setIntervalSeconds] = useState(60)
  const [countdown, setCountdown] = useState(60)
  const [selectedUser, setSelectedUser] = useState<UserTarget | null>(null)
  const requestSerial = useRef(0)
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const params = new URLSearchParams({
    period: query.filters.period, health: query.filters.health, sort: query.filters.sort,
    order: query.filters.order, page: String(query.page), page_size: String(PAGE_SIZE),
  })
  for (const key of ['model', 'channel_id'] as const) {
    const value = query.filters[key].trim()
    if (value) params.set(key, value)
  }
  const path = `/api/log-analysis/models?${params}`
  const scope = JSON.stringify([tokenVersion, sessionToken, path])
  const requestKey = `${scope}:${query.revision}`
  const latestKey = useRef(requestKey)
  latestKey.current = requestKey

  useEffect(() => { setSelectedUser(null) }, [tokenVersion, sessionToken])
  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    const serial = ++requestSerial.current
    const live = () => !controller.signal.aborted && serial === requestSerial.current &&
      latestKey.current === requestKey && sessionStorage.getItem('siteadmin-token') === sessionToken
    void get<Analysis>(path, controller.signal).then(data => {
      if (!live()) return
      validateAnalysis(data)
      const maxPage = Math.max(1, Math.ceil(data.total_models / data.page_size))
      if (query.page > maxPage) {
        setQuery(previous => ({ ...previous, page: maxPage, revision: previous.revision + 1 }))
        return
      }
      setResult({ key: requestKey, scope, data })
    }).catch(cause => {
      if (!live()) return
      const unauthorized = cause instanceof ApiError && cause.status === 401
      setResult({ key: requestKey, scope, unauthorized, error: unauthorized ? '管理员令牌已失效，请重新验证。'
        : cause instanceof Error ? cause.message : '暂时无法读取模型状态统计。' })
      if (unauthorized) onUnauthorized()
    })
    return () => controller.abort()
  }, [path, requestKey, scope, sessionToken, query.page, onUnauthorized])

  const current = result?.key === requestKey ? result : null
  const data = current?.data ?? null
  const error = current?.error
  const busy = !UI_PREVIEW_ONLY && !current
  const pages = Math.max(1, data ? Math.ceil(data.total_models / data.page_size) : 1)
  const page = data?.page ?? query.page
  const summary = data?.summary
  const dateFormat = useMemo(() => new Intl.DateTimeFormat('zh-CN', {
    timeZone: data?.timezone ?? 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }), [data?.timezone])
  const emptyMessage = UI_PREVIEW_ONLY ? '数据获取已暂停' : busy ? '正在统计模型状态…' : error ? '模型统计暂不可用' : '当前范围没有符合条件的模型记录'

  useEffect(() => {
    if (UI_PREVIEW_ONLY || !interval || !current || current.unauthorized) return
    const deadline = Date.now() + interval * 1000
    const tick = () => {
      const seconds = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setCountdown(seconds)
      if (!seconds && document.visibilityState === 'visible') {
        window.clearInterval(timer)
        setQuery(previous => ({ ...previous, revision: previous.revision + 1 }))
      }
    }
    const timer = window.setInterval(tick, 250)
    tick()
    return () => window.clearInterval(timer)
  }, [interval, current])

  function applyFilters(filters: Filters) {
    setQuery(previous => ({ filters: { ...filters }, page: 1, revision: previous.revision + 1 }))
  }
  function refresh() { setQuery(previous => ({ ...previous, revision: previous.revision + 1 })) }
  function updatePage(next: number) { setQuery(previous => ({ ...previous, page: next, revision: previous.revision + 1 })) }
  function reset() { setDraft(defaultFilters); applyFilters(defaultFilters) }
  const statistics = [
    {
      title: '总请求', value: number(summary?.total_requests), caption: summary ? `${number(summary.model_count)} 个模型` : '当前分析范围',
      icon: Activity, color: 'blue', progress: 100, progressLabel: '统计样本',
    },
    {
      title: '成功数', value: number(summary?.success_count), caption: '消费记录，包含疑似空回复',
      icon: CheckCircle2, color: 'green', progress: summary?.success_rate ?? null, progressLabel: '占比',
    },
    {
      title: '失败数', value: number(summary?.failure_count), caption: 'NewAPI 错误记录',
      icon: XCircle, color: 'red', progress: summary && summary.total_requests > 0 ? (summary.failure_count / summary.total_requests) * 100 : null, progressLabel: '占比',
    },
    {
      title: '空回复数', value: number(summary?.empty_count), caption: summary?.empty_count == null && (summary?.empty_unknown_count ?? 0) > 0
        ? `已知零输出 ${number(summary?.known_empty_count)} · 未记录 ${number(summary?.empty_unknown_count)}` : '成功记录中输出 Tokens 为 0',
      icon: MessageSquareWarning, color: 'orange', progress: summary && summary.success_count > 0 && summary.empty_count != null ? (summary.empty_count / summary.success_count) * 100 : null, progressLabel: '占成功数',
    },
    {
      title: '成功率', value: rate(summary?.success_rate), caption: '成功数 ÷ 总请求',
      icon: Percent, color: 'purple', progress: summary?.success_rate ?? null, progressLabel: '健康度',
    },
    {
      title: '空回复率', value: rate(summary?.empty_rate), caption: '空回复数 ÷ 成功数',
      icon: Percent, color: 'amber', progress: summary?.empty_rate ?? null, progressLabel: '异常度',
    },
  ]

  return <div className="analytics-page space-y-5">
    <section className="analytics-hero">
      <div className="analytics-hero__main">
        <div className="analytics-hero__eyebrow">
          <span className="analytics-hero__icon"><Activity aria-hidden="true" /></span>
          <div>
            <h1>日志分析</h1>
            <p>模型状态 · 成功率 · 失败与疑似空回复</p>
          </div>
        </div>
        <div className="analytics-hero__meta">
          <span>{periods[query.filters.period]}</span>
          <span>{data ? `${number(data.summary.total_requests)} 条请求` : '等待统计'}</span>
          <span>{data ? dateFormat.format(new Date(data.generated_at)) : '当前范围调用记录'}</span>
        </div>
      </div>
      <div className="analytics-hero__status">
        <Badge variant={UI_PREVIEW_ONLY || busy || error ? 'secondary' : 'success'}>{UI_PREVIEW_ONLY ? '数据已暂停' : busy ? '统计中' : error ? '读取失败' : '已更新'}</Badge>
        <div className="analytics-refresh-interval"><Timer className="pointer-events-none absolute left-3 top-2.5 z-10 h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <Select aria-label="自动刷新间隔" value={String(interval)} onChange={event => { const seconds = Number(event.target.value); setIntervalSeconds(seconds); setCountdown(seconds) }}>
            {intervals.map(item => <option key={item.seconds} value={String(item.seconds)}>{item.label}</option>)}
          </Select></div>
        {!UI_PREVIEW_ONLY && interval > 0 && !current?.unauthorized && <span className="analytics-countdown text-xs font-medium text-primary">{busy ? interval : countdown}s</span>}
        <Button type="button" size="sm" onClick={refresh} disabled={busy || UI_PREVIEW_ONLY}>{busy ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <RefreshCw className="mr-2 h-4 w-4" />}刷新统计</Button>
      </div>
    </section>

    <section className="analytics-filter-panel" aria-labelledby="analytics-filter-title">
      <form onSubmit={event => { event.preventDefault(); applyFilters(draft) }}>
        <div className="analytics-filter-head">
          <div><h2 id="analytics-filter-title">统计范围</h2><p>汇总与排行覆盖当前范围，模型状态仅影响模型表。</p></div>
          <div className="analytics-filter-actions">
            <Button type="button" variant="ghost" size="sm" onClick={reset}><RotateCcw className="mr-2 h-4 w-4" />重置</Button>
            <Button type="submit" size="sm" disabled={UI_PREVIEW_ONLY}><Search className="mr-2 h-4 w-4" />查询</Button>
          </div>
        </div>
        <div className="analytics-filter-grid">
          <div className="analytics-filter-field"><label htmlFor="analytics-period">时间范围</label><Select id="analytics-period" value={draft.period} onChange={event => setDraft({ ...draft, period: event.target.value as DashboardPeriod })}>
            <option value="24h">最近 24 小时</option><option value="7d">近 7 天</option><option value="30d">近 30 天</option>
          </Select></div>
          <div className="analytics-filter-field analytics-filter-field--wide"><label htmlFor="analytics-model">模型搜索</label><Input id="analytics-model" value={draft.model} maxLength={255} placeholder="搜索模型名称" onChange={event => setDraft({ ...draft, model: event.target.value })} /></div>
          <div className="analytics-filter-field"><label htmlFor="analytics-channel">渠道 ID</label><Input id="analytics-channel" value={draft.channel_id} maxLength={19} inputMode="numeric" pattern="[0-9]*" placeholder="全部渠道" onChange={event => setDraft({ ...draft, channel_id: event.target.value })} /></div>
          <div className="analytics-filter-field"><label htmlFor="analytics-health">模型状态</label><Select id="analytics-health" value={draft.health} onChange={event => setDraft({ ...draft, health: event.target.value as Filters['health'] })}>
            <option value="all">全部状态</option><option value="healthy">正常</option><option value="warning">需关注</option><option value="critical">异常</option><option value="unknown">数据不足</option>
          </Select></div>
          <div className="analytics-filter-field"><label htmlFor="analytics-sort">模型排序</label><Select id="analytics-sort" value={draft.sort} onChange={event => setDraft({ ...draft, sort: event.target.value as ModelSort })}>
            <option value="request_count">总请求</option><option value="failure_count">失败数</option><option value="success_rate">成功率</option><option value="empty_rate">空回复率</option>
          </Select></div>
          <div className="analytics-filter-field"><label htmlFor="analytics-order">排序方向</label><Select id="analytics-order" value={draft.order} onChange={event => setDraft({ ...draft, order: event.target.value as Filters['order'] })}>
            <option value="desc">从高到低</option><option value="asc">从低到高</option>
          </Select></div>
        </div>
      </form>
    </section>

    {error && <Notice kind="error"><div className="flex flex-wrap items-center gap-3"><span>{error}</span><Button type="button" size="sm" variant="outline" onClick={refresh}>重新读取</Button></div></Notice>}
    {UI_PREVIEW_ONLY && <Notice>数据获取已暂停，尚未读取模型状态统计。</Notice>}

    <div className="analytics-metrics" aria-label="模型状态汇总">
      {statistics.map(item => {
        const Icon = item.icon
        const progress = item.progress == null || !Number.isFinite(item.progress) ? null : Math.max(0, Math.min(100, item.progress))
        return <article key={item.title} className={`analytics-metric analytics-metric--${item.color}`}>
          <div className="analytics-metric__top">
            <span className="analytics-metric__icon"><Icon aria-hidden="true" /></span>
            <div>
              <h3>{item.title}</h3>
              <strong>{item.value}</strong>
            </div>
          </div>
          <p>{item.caption}</p>
          <div className="analytics-metric__progress" aria-hidden="true">
            <i style={progress == null ? undefined : { width: `${progress}%` }} />
          </div>
          {progress != null && <span className="analytics-metric__progress-label">{item.progressLabel} {percentages.format(progress)}%</span>}
        </article>
      })}
    </div>

    {data && (!data.empty_detection_available || data.summary.empty_unknown_count > 0) && <Notice kind="warning">
      {!data.empty_detection_available ? '当前日志未提供输出 Tokens 字段，空回复数与空回复率暂不计算。' : `有 ${number(data.summary.empty_unknown_count)} 条成功记录未记录输出 Tokens；受影响模型的空回复数与空回复率显示“—”，已知零输出记录仍单独标明。`}
    </Notice>}
    {!!data?.data_notes?.length && <div className="analytics-data-notes">{[...new Set(data.data_notes)].map(note => <p key={note}><Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />{note}</p>)}</div>}

    <Card className="min-w-0" aria-labelledby="analytics-models-title" aria-busy={busy}>
      <CardHeader><div className="flex flex-wrap items-center justify-between gap-3">
        <CardTitle id="analytics-models-title" className="text-lg">模型统计 <span className="text-sm font-normal text-muted-foreground">成功率 &amp; 空回复率</span></CardTitle>
        <span className="text-xs text-muted-foreground">{data ? `${number(data.total_models)} 个符合条件的模型` : '按模型聚合统计'}</span>
      </div>
        {summary && <div className="flex flex-wrap gap-2 pt-1 text-xs">
          <Badge variant="success">正常 {number(summary.healthy_models)}</Badge><Badge variant="warning">需关注 {number(summary.warning_models)}</Badge>
          <Badge variant="destructive">异常 {number(summary.critical_models)}</Badge><Badge variant="secondary">数据不足 {number(summary.unknown_models)}</Badge>
          <span className="self-center text-muted-foreground">以上为整个统计范围内的模型数</span>
        </div>}
      </CardHeader>
      <CardContent className="space-y-4">
        <Table aria-labelledby="analytics-models-title" className="analytics-model-table" containerClassName="scroll-region scroll-region--lg analytics-model-table-scroll" containerLabel="模型状态统计，可上下和左右滚动">
          <TableHeader><TableRow>{['模型', '总请求', '成功数', '失败数', '空回复数', '成功率', '空回复率'].map((label, index) => <TableHead key={label} className={index ? 'text-right' : ''}>{label}</TableHead>)}</TableRow></TableHeader>
          <TableBody>{data?.models.map(model => <TableRow key={model.model_name}>
            <TableCell className="analytics-model-name font-medium" title={model.model_name || '未记录模型'}><span>{model.model_name || '未记录模型'}</span><small className={`analytics-model-health analytics-model-health--${model.health}`}>{healthLabels[model.health] || '数据不足'}</small></TableCell>
            <TableCell className="text-right tabular-nums">{number(model.total_requests)}</TableCell>
            <TableCell className="text-right tabular-nums text-green-600">{number(model.success_count)}</TableCell>
            <TableCell className="text-right tabular-nums text-red-600">{number(model.failure_count)}</TableCell>
            <TableCell className="text-right tabular-nums">{number(model.empty_count)}{model.empty_unknown_count > 0 && <small className="analytics-empty-unknown">已知 {number(model.known_empty_count)} · 未记录 {number(model.empty_unknown_count)}</small>}</TableCell>
            <TableCell className={`text-right tabular-nums ${rateClass(model.success_rate)}`}>{rate(model.success_rate)}</TableCell>
            <TableCell className={`text-right tabular-nums ${rateClass(model.empty_rate, true)}`}>{rate(model.empty_rate)}</TableCell>
          </TableRow>)}
            {!data?.models.length && <TableRow><TableCell colSpan={7} className="h-40 text-center text-sm text-muted-foreground" role="status">{emptyMessage}</TableCell></TableRow>}
          </TableBody>
        </Table>
        <div className="analytics-pagination" role="navigation" aria-label="模型统计分页">
          <span className="text-sm text-muted-foreground">{data ? `显示 ${number((page - 1) * data.page_size + (data.models.length ? 1 : 0))}–${number((page - 1) * data.page_size + data.models.length)}，共 ${number(data.total_models)} 个模型` : `每页显示 ${PAGE_SIZE} 个模型`}</span>
          <div className="flex items-center gap-2"><Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy || page <= 1} onClick={() => updatePage(page - 1)}><ChevronLeft className="mr-1 h-4 w-4" />上一页</Button>
            <span className="px-2 text-sm font-medium">{page} / {pages}</span>
            <Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy || !data || page >= pages} onClick={() => updatePage(page + 1)}>下一页<ChevronRight className="ml-1 h-4 w-4" /></Button></div>
        </div>
      </CardContent>
    </Card>

    <section className="analytics-insight">
      <div className="analytics-health-legend">
        <span><i />成功率 ≥ 95% / 空回复率 ≤ 5%</span>
        <span><i />成功率 80–95% / 空回复率 5–15%</span>
        <span><i />成功率 &lt; 80% / 空回复率 &gt; 15%</span>
      </div>
      <p>成功率 = 成功数 ÷ 总请求；空回复率 = 空回复数 ÷ 成功数。成功数采用 NewAPI 消费记录（type=2，包含空回复），失败数采用错误记录（type=5）。</p>
      <details className="analytics-definition"><summary>空回复与模型状态的统计说明</summary>
        <div><p>“空回复”按成功记录中输出 Tokens 为 0 识别，为疑似空回复线索；没有读取回复正文，图像、音频等非文本输出也可能没有输出 Tokens。</p>
          <p>缺少输出 Tokens 字段或值时保持未知，不按 0 计数；没有成功请求时，空回复率显示“—”。空回复是成功记录的子集，不另加到总请求。</p>
          <p>这里按调用日志记录计数，包含渠道重试记录；成功率描述日志状态，不等于去重后的最终请求成功率。</p>
          <p>绿色需成功率与空回复率同时达到正常区间；任一指标到达红色区间即标为异常，其余可判断情况为需关注。无法充分判断时标为数据不足。</p></div>
      </details>
    </section>

    <ModelStatusTrend data={data} loading={busy} emptyMessage={emptyMessage} />

    <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
      <UserRankTable id="analytics-request-ranks" title="用户请求数排行" metric="count" entries={data?.rankings.requests ?? []} quotaPerUnit={data?.quota_per_unit ?? 0} emptyMessage={emptyMessage} onViewUser={setSelectedUser} />
      <UserRankTable id="analytics-quota-ranks" title="用户额度消耗排行" metric="quota" entries={data?.rankings.quota ?? []} quotaPerUnit={data?.quota_per_unit ?? 0} emptyMessage={emptyMessage} onViewUser={setSelectedUser} />
    </div>
    {selectedUser && <AdminDialog wide title={`用户详情 · ${selectedUser.username || `#${selectedUser.id}`}`} onClose={() => setSelectedUser(null)}>
      <RiskUserCard userId={selectedUser.id} username={selectedUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
    </AdminDialog>}
  </div>
}
