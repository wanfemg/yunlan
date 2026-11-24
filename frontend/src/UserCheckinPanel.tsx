import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { CalendarCheck, ChevronLeft, ChevronRight, Coins, Download, Loader2, RefreshCw } from 'lucide-react'
import { ApiError, downloadCSV, requestJSON, UI_PREVIEW_ONLY } from './api'
import { checkinCategoryLabels } from './checkin-types'
import type { CheckinCategory, CheckinReview, CheckinUserDetail } from './checkin-types'
import CheckinReviewForm from './CheckinReviewForm'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { cn } from './lib/utils'

export type UserCheckinPanelProps = {
  userId: number
  username: string
  tokenVersion?: number
  initialDays?: number
  rangeQuery?: string
  refreshVersion?: number | string
  onUnauthorized: () => void
  onReviewSaved?: () => void
}

type ViewState = { userId: number; month: string; date: string; page: number }
type DetailSnapshot = { scope: string; sessionToken: string | null; data?: CheckinUserDetail; error?: string; unauthorized?: boolean }
const countFormat = new Intl.NumberFormat('zh-CN')
const monthPattern = /^\d{4}-(0[1-9]|1[0-2])$/
const weekdayLabels = ['一', '二', '三', '四', '五', '六', '日']
const windowOptions = [7, 30, 90]
const redemptionStatusLabels: Record<string, string> = {
  unavailable: '兑换码记录暂不可用，来源待核实',
  none_observed: '未观测到兑换码使用',
  unverified: '使用过兑换码，付费或福利来源待核实',
  confirmed_free_by_review: '已人工核实为免费福利来源',
}

function count(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : countFormat.format(value)
}

function quota(value: number | null | undefined, unit: number, compact = false) {
  if (value == null || !Number.isFinite(value) || !Number.isFinite(unit) || unit <= 0) return '—'
  const amount = value / unit
  return `${compact ? '' : '$'}${new Intl.NumberFormat('en-US', {
    minimumFractionDigits: compact ? 0 : 2,
    maximumFractionDigits: amount !== 0 && Math.abs(amount) < 0.01 ? 6 : 4,
  }).format(amount)}`
}

function paymentAmount(value: string | null | undefined) {
  if (value == null || value === '') return '未知'
  const amount = Number(value)
  return Number.isFinite(amount) ? new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 6 }).format(amount) : value
}

function timestamp(value: number | null | undefined, timezone: string) {
  if (!value || !Number.isFinite(value)) return '未记录'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    second: '2-digit', hourCycle: 'h23',
  }).format(value * 1000)
}

function dayAt(timestampSeconds: number, timezone: string) {
  const parts = new Intl.DateTimeFormat('en-CA', { timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit' })
    .formatToParts(timestampSeconds * 1000)
  return `${parts.find(part => part.type === 'year')?.value}-${parts.find(part => part.type === 'month')?.value}-${parts.find(part => part.type === 'day')?.value}`
}

function shiftedMonth(month: string, offset: number) {
  const [year, value] = month.split('-').map(Number)
  const date = new Date(Date.UTC(year, value - 1 + offset, 1))
  return `${date.getUTCFullYear()}-${String(date.getUTCMonth() + 1).padStart(2, '0')}`
}

function monthDays(month: string) {
  if (!monthPattern.test(month)) return { offset: 0, dates: [] as string[] }
  const [year, value] = month.split('-').map(Number)
  const first = new Date(Date.UTC(year, value - 1, 1))
  const days = new Date(Date.UTC(year, value, 0)).getUTCDate()
  return {
    offset: (first.getUTCDay() + 6) % 7,
    dates: Array.from({ length: days }, (_, index) => `${month}-${String(index + 1).padStart(2, '0')}`),
  }
}

function categoryVariant(category: CheckinCategory) {
  return category === 'paid' ? 'success' as const : category === 'pure_free' ? 'warning' as const
    : category === 'unknown' ? 'outline' as const : 'secondary' as const
}

function Metric({ label, value, description }: { label: string; value: ReactNode; description?: string }) {
  return <div className="min-w-0 rounded-lg bg-muted/30 p-3 sm:p-4">
    <p className="text-xs text-muted-foreground">{label}</p>
    <p className="mt-1 break-words text-lg font-semibold tabular-nums">{value}</p>
    {description && <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{description}</p>}
  </div>
}

export default function UserCheckinPanel({
  userId, username, tokenVersion = 0, initialDays = 30, rangeQuery,
  refreshVersion = 0, onUnauthorized, onReviewSaved,
}: UserCheckinPanelProps) {
  const [revision, setRevision] = useState(0)
  const [rangeState, setRangeState] = useState(() => ({
    userId, days: windowOptions.includes(initialDays) ? initialDays : 30, asOf: Math.floor(Date.now() / 1000),
  }))
  const [viewState, setViewState] = useState<ViewState>({ userId, month: '', date: '', page: 1 })
  const [snapshot, setSnapshot] = useState<DetailSnapshot | null>(null)
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState('')
  const serial = useRef(0)
  const exportingRef = useRef(false)
  const exportAbort = useRef<AbortController | null>(null)
  const callbacks = useRef({ onUnauthorized, onReviewSaved })
  callbacks.current = { onUnauthorized, onReviewSaved }
  const clock = useRef({ userId, asOf: Math.floor(Date.now() / 1000) })
  if (clock.current.userId !== userId) clock.current = { userId, asOf: Math.floor(Date.now() / 1000) }
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const view = viewState.userId === userId ? viewState : { userId, month: '', date: '', page: 1 }
  const range = rangeState.userId === userId ? rangeState : {
    userId, days: windowOptions.includes(initialDays) ? initialDays : 30, asOf: clock.current.asOf,
  }
  const rangeParams = useMemo(() => {
    const params = new URLSearchParams()
    const supplied = new URLSearchParams(rangeQuery?.replace(/^\?/, '') ?? '')
    for (const field of ['days', 'start_date', 'end_date', 'as_of']) {
      const value = supplied.get(field)
      if (value) params.set(field, value)
    }
    if (!params.has('days') && !params.has('start_date')) params.set('days', String(range.days))
    if (!params.has('as_of')) params.set('as_of', String(range.asOf))
    return params.toString()
  }, [rangeQuery, range.days, range.asOf])
  const query = new URLSearchParams(rangeParams)
  if (view.month) query.set('month', view.month)
  if (view.date) query.set('date', view.date)
  query.set('page', String(view.page))
  query.set('page_size', '10')
  const queryString = query.toString()
  const scope = JSON.stringify([userId, username, tokenVersion, sessionToken, queryString, refreshVersion, revision])
  const currentScope = useRef(scope)
  currentScope.current = scope

  useEffect(() => {
    exportAbort.current?.abort()
    exportingRef.current = false
    setExporting(false)
    setExportError('')
  }, [scope])

  useEffect(() => {
    const controller = new AbortController()
    const requestSerial = ++serial.current
    const requestScope = scope
    const isCurrent = () => !controller.signal.aborted && serial.current === requestSerial &&
      currentScope.current === requestScope && sessionStorage.getItem('siteadmin-token') === sessionToken
    setSnapshot(null)
    if (!UI_PREVIEW_ONLY) {
      void requestJSON<CheckinUserDetail>(`/api/checkins/users/${userId}?${queryString}`, {}, controller.signal).then(data => {
        if (!isCurrent()) return
        if (data.user?.id !== userId || !Array.isArray(data.records) || !Array.isArray(data.calendar) ||
          !monthPattern.test(data.month) || !Number.isFinite(data.as_of) || !(data.quota_per_unit > 0)) {
          throw new Error('返回的签到详情与当前账号不匹配，请重新读取。')
        }
        new Intl.DateTimeFormat('zh-CN', { timeZone: data.timezone }).format()
        setSnapshot({ scope: requestScope, sessionToken, data })
      }).catch(cause => {
        if (!isCurrent()) return
        const unauthorized = cause instanceof ApiError && cause.status === 401
        setSnapshot({
          scope: requestScope, sessionToken, unauthorized,
          error: unauthorized ? '管理员会话已失效，请重新连接。'
            : cause instanceof Error ? cause.message : '无法读取当前账号的签到详情。',
        })
        if (unauthorized) callbacks.current.onUnauthorized()
      })
    }
    return () => controller.abort()
  }, [scope, queryString, userId, sessionToken])

  useEffect(() => () => exportAbort.current?.abort(), [])
  const current = snapshot?.scope === scope && snapshot.sessionToken === sessionToken ? snapshot : null
  const data = !UI_PREVIEW_ONLY ? current?.data : undefined
  const loading = !UI_PREVIEW_ONLY && !current
  const user = data?.user
  const timezone = data?.timezone ?? 'Asia/Shanghai'
  const selectedMonth = data?.month ?? (view.month || dayAt(range.asOf, timezone).slice(0, 7))
  const today = dayAt(data?.as_of ?? range.asOf, timezone)
  const calendar = monthDays(selectedMonth)
  const byDate = new Map(data?.calendar.map(day => [day.date, day]) ?? [])
  const selectedDate = view.date
  const payment = user?.payment
  const paidObserved = Boolean(payment && (payment.paid_count != null
    ? payment.paid_count > 0
    : (payment.wallet_money != null && Number(payment.wallet_money) > 0) ||
      (payment.subscription_money != null && Number(payment.subscription_money) > 0)))
  const review = data?.review ?? user?.review ?? null
  const unit = data?.quota_per_unit ?? 500000

  function refresh() {
    if (!rangeQuery) setRangeState({ userId, days: range.days, asOf: Math.floor(Date.now() / 1000) })
    setRevision(value => value + 1)
  }

  function selectMonth(month: string) {
    setViewState({ userId, month, date: '', page: 1 })
  }

  function reviewSaved(_review: CheckinReview | null) {
    refresh()
    callbacks.current.onReviewSaved?.()
  }

  async function exportUser() {
    if (UI_PREVIEW_ONLY || exportingRef.current || !data) return
    exportingRef.current = true
    const controller = new AbortController()
    exportAbort.current?.abort()
    exportAbort.current = controller
    const requestScope = scope
    setExporting(true)
    setExportError('')
    try {
      const params = new URLSearchParams(rangeParams)
      params.set('scope', 'all')
      params.set('user_id', String(userId))
      await downloadCSV(`/api/checkins/export?${params}`, `签到分析-用户${userId}-${data.start_date}-${data.end_date}.csv`, controller.signal)
    } catch (cause) {
      if (controller.signal.aborted || currentScope.current !== requestScope) return
      if (cause instanceof ApiError && cause.status === 401) callbacks.current.onUnauthorized()
      else setExportError(cause instanceof Error ? cause.message : '导出失败，请重试。')
    } finally {
      if (!controller.signal.aborted && currentScope.current === requestScope) {
        exportingRef.current = false
        setExporting(false)
      }
    }
  }

  return <section className="um-detail um-detail--checkin min-w-0 space-y-5" aria-label={`${username || `用户 #${userId}`}的签到分析`} aria-busy={loading}>
    <div className="um-detail-section um-detail-section--checkin">
      <header>
        <h4>签到分析</h4>
        <p>签到奖励、实际调用与付费来源核实</p>
      </header>
      <div className="um-detail-section-toolbar">
        <span className="um-detail-section-user">{username || `用户 #${userId}`}</span>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" type="button" disabled={UI_PREVIEW_ONLY || loading || exporting || !data} onClick={() => void exportUser()}>
            {exporting ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Download className="mr-2 h-4 w-4" />}导出 CSV
          </Button>
          <Button size="sm" variant="outline" type="button" disabled={UI_PREVIEW_ONLY || loading} onClick={refresh}>
            <RefreshCw className={cn('mr-2 h-4 w-4', loading && 'animate-spin')} />刷新
          </Button>
        </div>
      </div>
    </div>
    {!rangeQuery && <div className="flex flex-wrap gap-2" role="group" aria-label="签到统计范围">
      {windowOptions.map(days => <Button key={days} type="button" size="sm" variant={range.days === days ? 'default' : 'outline'}
        disabled={UI_PREVIEW_ONLY} aria-pressed={range.days === days} onClick={() => {
          setRangeState({ userId, days, asOf: Math.floor(Date.now() / 1000) })
          setViewState({ userId, month: view.month, date: '', page: 1 })
        }}>近 {days} 天</Button>)}
    </div>}
    {UI_PREVIEW_ONLY ? <p className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">数据获取已暂停</p>
      : loading ? <div className="flex min-h-32 items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="h-5 w-5 animate-spin text-primary" />正在读取签到详情…</div>
        : current?.error ? <div className="space-y-3 rounded-lg border border-destructive/30 p-4" role="alert">
          <p className="break-words text-sm text-destructive">{current.error}</p>
          {!current.unauthorized && <Button type="button" size="sm" variant="outline" onClick={refresh}>重新读取</Button>}
        </div> : null}
    {exportError && <p className="text-sm text-destructive" role="alert">{exportError}</p>}
    {data && user && <>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={categoryVariant(user.category)}>{checkinCategoryLabels[user.category] ?? user.category}</Badge>
        <Badge variant="outline">{data.start_date} — {data.end_date}</Badge>
        <span className="text-xs text-muted-foreground">{timezone} · 更新于 {timestamp(data.as_of, timezone)}</span>
      </div>
      <div className="grid min-w-0 grid-cols-2 gap-3 lg:grid-cols-4">
        <Metric label="窗口签到天数 / 人次" value={`${count(user.checkin_days)} / ${count(user.checkin_count)}`} />
        <Metric label="窗口奖励面值" value={quota(user.awarded_quota, unit)} />
        <Metric label="历史签到天数 / 人次" value={`${count(user.lifetime_checkin_days)} / ${count(user.lifetime_checkin_count)}`} />
        <Metric label="历史奖励面值" value={quota(user.lifetime_awarded_quota, unit)} />
        <Metric label="同期调用 / 消耗" value={count(user.requests)} description={`成功消费额度 ${quota(user.consumed_quota, unit)}`} />
        <Metric label="历史调用 / 消耗" value={count(user.lifetime_requests)} description={`成功消费额度 ${quota(user.lifetime_consumed_quota, unit)}`} />
        <Metric label={data.streak_complete ? '当前 / 历史最长连续' : '当前 / 保留记录内最长'} value={`${count(data.current_streak)} / ${count(data.longest_streak)} 天`}
          description={data.streak_complete ? undefined : '首次签到以来的历史完整性未确认，仅按现有保留记录计算。'} />
        <Metric label="窗口参与率" value={user.participation_rate == null ? '—' : `${(user.participation_rate * 100).toFixed(1)}%`}
          description={user.participation_basis || '以已观测窗口计算'} />
      </div>

      <Card className="min-w-0 shadow-sm">
        <CardHeader className="space-y-3 p-4 pb-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <CardTitle className="text-sm">签到日历</CardTitle>
            <div className="flex items-center gap-2">
              <Button size="icon" variant="outline" type="button" className="h-8 w-8" disabled={loading} aria-label="上个月" onClick={() => selectMonth(shiftedMonth(selectedMonth, -1))}><ChevronLeft className="h-4 w-4" /></Button>
              <span className="min-w-20 text-center text-sm font-medium tabular-nums">{selectedMonth}</span>
              <Button size="icon" variant="outline" type="button" className="h-8 w-8" disabled={loading || selectedMonth >= today.slice(0, 7)} aria-label="下个月" onClick={() => selectMonth(shiftedMonth(selectedMonth, 1))}><ChevronRight className="h-4 w-4" /></Button>
              <Button size="sm" variant="ghost" type="button" disabled={loading} onClick={() => selectMonth(today.slice(0, 7))}>本月</Button>
            </div>
          </div>
          <CardDescription className="text-xs">选择日期查看实际签到记录；日历不提供补签或发放额度操作。</CardDescription>
        </CardHeader>
        <CardContent className="min-w-0 p-3 pt-0 sm:p-4 sm:pt-0">
          <div className="mb-2 grid grid-cols-7 gap-1 text-center text-xs text-muted-foreground">{weekdayLabels.map(day => <div className="py-2" key={day}>周{day}</div>)}</div>
          <div className="grid grid-cols-7 gap-1 sm:gap-2">
            {Array.from({ length: calendar.offset }, (_, index) => <div key={`blank-${index}`} aria-hidden="true" />)}
            {calendar.dates.map(date => {
              const entry = byDate.get(date)
              const future = date > today
              const checked = Boolean(entry?.count)
              return <button key={date} type="button" disabled={future || loading}
                aria-pressed={selectedDate === date} aria-label={`${date}${future ? '，未来日期' : checked ? `，签到 ${entry!.count} 条，奖励面值 ${quota(entry!.awarded_quota, unit)}` : '，无已观测签到记录'}`}
                onClick={() => setViewState({ userId, month: selectedMonth, date: selectedDate === date ? '' : date, page: 1 })}
                className={cn('flex min-h-16 min-w-0 flex-col items-center justify-center gap-1 rounded-lg border p-1 text-center outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring sm:min-h-20',
                  checked ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300' : 'border-border/60 bg-muted/10 text-muted-foreground',
                  selectedDate === date && 'ring-2 ring-primary ring-offset-1',
                  date === today && 'font-bold',
                  future && 'border-transparent bg-transparent opacity-35',
                )}>
                <span className="text-sm tabular-nums">{Number(date.slice(-2))}{date === today && <span className="ml-0.5 text-[9px]">今</span>}</span>
                <span className="max-w-full truncate text-[10px] tabular-nums">{future ? '—' : checked ? quota(entry!.awarded_quota, unit) : '—'}</span>
                {entry && entry.count > 1 && <span className="text-[9px]">{entry.count} 条记录</span>}
              </button>
            })}
          </div>
          <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <span>绿色为已观测签到，空白不代表从未签到；今天未结束时不视为断签。</span>
            {selectedDate && <Button size="sm" type="button" variant="ghost" className="h-7" onClick={() => setViewState({ userId, month: selectedMonth, date: '', page: 1 })}>显示整月记录</Button>}
          </div>
        </CardContent>
      </Card>

      <Card className="min-w-0 shadow-sm">
        <CardHeader className="space-y-2 p-4 pb-3">
          <CardTitle className="flex flex-wrap items-center gap-2 text-sm"><CalendarCheck className="h-4 w-4 text-primary" />
            {selectedDate ? `${selectedDate} 签到明细` : `${selectedMonth} 签到明细`}
            <Badge variant="secondary" className="font-normal">共 {count(data.total)} 条</Badge>
          </CardTitle>
          <CardDescription className="text-xs">奖励以签到记录的实际额度为准，不按当前奖励配置反推。</CardDescription>
        </CardHeader>
        <CardContent className="min-w-0 space-y-3 p-4 pt-0">
          <Table className="min-w-[460px]">
            <TableHeader><TableRow><TableHead>业务日期</TableHead><TableHead>记录时间</TableHead><TableHead className="text-right">奖励面值</TableHead></TableRow></TableHeader>
            <TableBody>{data.records.length ? data.records.map(record => <TableRow key={record.id}>
              <TableCell className="text-xs tabular-nums">{record.checkin_date}</TableCell>
              <TableCell className="text-xs tabular-nums text-muted-foreground">{timestamp(record.created_at, timezone)}</TableCell>
              <TableCell className="text-right font-mono text-xs">{quota(record.quota_awarded, unit)}</TableCell>
            </TableRow>) : <TableRow><TableCell colSpan={3} className="h-20 text-center text-sm text-muted-foreground">该日期范围暂无已观测签到记录。</TableCell></TableRow>}</TableBody>
          </Table>
          <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground">
            <span>第 {count(data.page)} / {count(Math.max(1, data.total_pages))} 页</span>
            <div className="flex gap-2">
              <Button type="button" size="sm" variant="outline" disabled={loading || data.page <= 1} onClick={() => setViewState({ ...view, userId, page: Math.max(1, data.page - 1) })}><ChevronLeft className="mr-1 h-4 w-4" />上一页</Button>
              <Button type="button" size="sm" variant="outline" disabled={loading || data.page >= data.total_pages} onClick={() => setViewState({ ...view, userId, page: data.page + 1 })}>下一页<ChevronRight className="ml-1 h-4 w-4" /></Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card className="min-w-0 shadow-sm">
        <CardHeader className="space-y-2 p-4 pb-3">
          <CardTitle className="flex items-center gap-2 text-sm"><Coins className="h-4 w-4 text-primary" />付费事实与额度来源</CardTitle>
          <CardDescription className="text-xs">订单金额（原币种未记录）。成功外部付款与余额购买订阅分开，金额保留订单原值，不与额度面值合并。</CardDescription>
        </CardHeader>
        <CardContent className="min-w-0 space-y-4 p-4 pt-0">
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <Metric label="历史外部钱包付款" value={paymentAmount(payment?.wallet_money)} />
            <Metric label="历史外部订阅付款" value={paymentAmount(payment?.subscription_money)} />
            <Metric label="窗口外部钱包付款" value={paymentAmount(payment?.window_wallet_money)} />
            <Metric label="窗口外部订阅付款" value={paymentAmount(payment?.window_subscription_money)} />
          </div>
          <dl className="grid min-w-0 gap-3 text-xs sm:grid-cols-2">
            <div><dt className="text-muted-foreground">成功付费订单</dt><dd className="mt-1">{count(payment?.paid_count)}</dd></div>
            <div><dt className="text-muted-foreground">付费历史覆盖</dt><dd className="mt-1">{payment?.history_complete ? '已确认完整' : '尚未确认完整'}</dd></div>
            <div><dt className="text-muted-foreground">兑换码使用</dt><dd className="mt-1">{count(payment?.redemption_count)} 次 · 额度面值 {quota(payment?.redemption_quota, unit)}</dd></div>
            <div><dt className="text-muted-foreground">兑换码来源</dt><dd className="mt-1 break-words">{payment?.redemption_status ? redemptionStatusLabels[payment.redemption_status] ?? '来源状态尚未确认' : '尚未确认，不能据此判断为免费'}</dd></div>
          </dl>
          <div className="rounded-lg bg-muted/30 p-3 text-xs leading-relaxed text-muted-foreground">
            <p className="font-medium text-foreground">分类依据</p>
            {user.category_reasons.length ? <ul className="mt-2 list-disc space-y-1 pl-4">{user.category_reasons.map((reason, index) => <li key={`${index}-${reason}`}>{reason}</li>)}</ul>
              : <p className="mt-1">现有资料不足以给出进一步来源结论。</p>}
            <p className="mt-2">当前余额 {quota(user.quota, unit)} 不能视为签到余额；总消费不能归因到某笔签到奖励。</p>
          </div>
          <CheckinReviewForm userId={userId} username={username} review={review} tokenVersion={tokenVersion}
            observedPaid={paidObserved} onUnauthorized={onUnauthorized} onSaved={reviewSaved} />
        </CardContent>
      </Card>
      {(data.warnings.length > 0 || data.source_status.some(source => source.status !== 'ok' && source.status !== 'demo')) &&
        <div className="space-y-2 rounded-lg border border-amber-200 bg-amber-50/50 p-3 text-xs leading-relaxed text-muted-foreground dark:border-amber-900 dark:bg-amber-950/20">
          {data.source_status.filter(source => source.status !== 'ok' && source.status !== 'demo').map(source => <p key={source.id}>{source.message}</p>)}
          {data.warnings.map((warning, index) => <p key={`${index}-${warning}`}>{warning}</p>)}
        </div>}
    </>}
  </section>
}
