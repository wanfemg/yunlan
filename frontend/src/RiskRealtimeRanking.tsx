// Adapted from new_api_tools (RealtimeRanking.tsx), MIT License.
// See tools-reference-LICENSE.txt. Account actions use the shared risk workflow.
import { useEffect, useRef, useState } from 'react'
import { Activity, Loader2, RefreshCw, ShieldBan, ShieldCheck, Timer } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Select } from './components/ui/select'
import { cn } from './lib/utils'
import './risk-realtime-ranking.css'

type WindowKey = '1h' | '3h' | '6h' | '12h' | '24h' | '3d' | '7d'
type LongWindow = '24h' | '3d' | '7d'
type SortKey = 'requests' | 'quota' | 'failure_rate'
type RankingItem = {
  user_id: number
  username: string
  display_name: string
  user_status: number
  role: number
  group?: string
  identity_known: boolean
  request_count: number
  failure_requests: number
  failure_rate: number | null
  quota_used: number
  prompt_tokens: number | null
  completion_tokens: number | null
  unique_ips: number | null
  ip_count_complete: boolean
}
type RankingWindow = {
  window: WindowKey
  window_start: number
  window_end: number
  items: RankingItem[]
}
type RankingResponse = {
  as_of: number
  timezone: string
  quota_per_unit: number
  sort: SortKey
  windows: RankingWindow[]
  warnings: string[]
  data_notes?: string[]
}
type Snapshot = {
  scope: string
  windows: Partial<Record<WindowKey, RankingWindow>>
  errors: Partial<Record<WindowKey, string>>
  warning: string[]
  notes?: string[]
  error?: string
  unauthorized?: boolean
  asOf?: number
  timezone?: string
  quotaPerUnit?: number
}
export type RiskRealtimeRankingProps = {
  tokenVersion?: number
  refreshRevision?: number
  actionsDisabled?: boolean
  onUnauthorized: () => void
  onViewUser: (id: number, username?: string) => void
  onBanUser: (id: number) => void
}

const shortWindows = ['1h', '3h', '6h', '12h'] as const
const longWindows = ['24h', '3d', '7d'] as const
const windowLabels: Record<WindowKey, string> = {
  '1h': '1小时内', '3h': '3小时内', '6h': '6小时内', '12h': '12小时内',
  '24h': '24小时内', '3d': '3天内', '7d': '7天内',
}
const sortLabels: Record<SortKey, string> = { requests: '请求次数', quota: '额度消耗', failure_rate: '失败率' }
const refreshIntervals = [0, 10, 30, 60, 120, 300, 600]
const numberFormat = new Intl.NumberFormat('zh-CN')
const percentFormat = new Intl.NumberFormat('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 })
const currencyFormat = new Intl.NumberFormat('zh-CN', {
  style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4,
})

function count(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : numberFormat.format(value)
}
function percent(value: number | null) {
  return value == null ? '—' : `${percentFormat.format(value)}%`
}
function countdownLabel(value: number) {
  const minutes = Math.floor(value / 60)
  return minutes ? `${minutes}:${String(value % 60).padStart(2, '0')}` : `${value}s`
}
function intervalLabel(value: number) {
  return value === 0 ? '关闭自动刷新' : value < 60 ? `每 ${value} 秒` : `每 ${value / 60} 分钟`
}
function formatTime(value?: number, timezone = 'Asia/Shanghai') {
  if (!value) return '尚未读取'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    second: '2-digit', hourCycle: 'h23',
  }).format(value * 1000)
}
function validateResponse(value: RankingResponse, sort: SortKey, expected: readonly WindowKey[]) {
  const validCount = (count: number) => Number.isSafeInteger(count) && count >= 0
  if (!value || value.sort !== sort || !Number.isFinite(value.as_of) || value.as_of <= 0 ||
    !Number.isFinite(value.quota_per_unit) || value.quota_per_unit < 0 ||
    !Array.isArray(value.windows) || value.windows.length !== expected.length ||
    !Array.isArray(value.warnings) || value.warnings.some(warning => typeof warning !== 'string') ||
    (value.data_notes !== undefined && (!Array.isArray(value.data_notes) || value.data_notes.some(note => typeof note !== 'string')))) {
    throw new Error('排行返回不完整，请重新读取。')
  }
  const found = new Set<WindowKey>()
  for (const window of value.windows) {
    if (!expected.includes(window.window) || found.has(window.window) || !Number.isFinite(window.window_start) ||
      !Number.isFinite(window.window_end) || window.window_end <= window.window_start ||
      window.window_end > value.as_of || !Array.isArray(window.items) || window.items.length > 10) {
      throw new Error('排行时间窗口不一致，请重新读取。')
    }
    found.add(window.window)
    const users = new Set<number>()
    for (const user of window.items) {
      if (!Number.isSafeInteger(user.user_id) || user.user_id < 1 || users.has(user.user_id) ||
        typeof user.username !== 'string' || typeof user.display_name !== 'string' ||
        typeof user.identity_known !== 'boolean' || typeof user.ip_count_complete !== 'boolean' ||
        !validCount(user.request_count) || !validCount(user.failure_requests) ||
        user.failure_requests > user.request_count || !Number.isFinite(user.quota_used) ||
        !Number.isFinite(user.role) || !Number.isFinite(user.user_status) ||
        (user.unique_ips !== null && !validCount(user.unique_ips)) ||
        [user.prompt_tokens, user.completion_tokens].some(tokens => tokens !== null && !validCount(tokens)) ||
        (user.failure_rate !== null && (!Number.isFinite(user.failure_rate) || user.failure_rate < 0 || user.failure_rate > 100))) {
        throw new Error('账号排行数据异常，请重新读取。')
      }
      users.add(user.user_id)
    }
  }
  new Intl.DateTimeFormat('zh-CN', { timeZone: value.timezone }).format()
}
function emptySnapshot(scope: string): Snapshot {
  return { scope, windows: {}, errors: {}, warning: [] }
}

export default function RiskRealtimeRanking({
  tokenVersion = 0, refreshRevision = 0, actionsDisabled = false, onUnauthorized, onViewUser, onBanUser,
}: RiskRealtimeRankingProps) {
  const [sort, setSort] = useState<SortKey>('requests')
  const [longWindow, setLongWindow] = useState<LongWindow>('24h')
  const [revision, setRevision] = useState(0)
  const [interval, setIntervalSeconds] = useState(60)
  const [countdown, setCountdown] = useState(60)
  const [completed, setCompleted] = useState(0)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [allPending, setAllPending] = useState<string | null>(null)
  const [windowPending, setWindowPending] = useState<{ scope: string; window: WindowKey } | null>(null)
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const scope = JSON.stringify([tokenVersion, sessionToken, sort, longWindow, refreshRevision])
  const activeScope = useRef(scope)
  activeScope.current = scope
  const callbacks = useRef({ onUnauthorized, onViewUser, onBanUser })
  callbacks.current = { onUnauthorized, onViewUser, onBanUser }
  const allController = useRef<AbortController | null>(null)
  const cardController = useRef<AbortController | null>(null)
  const unauthorizedScope = useRef('')

  function report(cause: unknown, requestScope: string) {
    if (cause instanceof ApiError && cause.status === 401) {
      if (unauthorizedScope.current !== requestScope) {
        unauthorizedScope.current = requestScope
        callbacks.current.onUnauthorized()
      }
      return '管理员会话已失效，请重新连接。'
    }
    return cause instanceof Error ? cause.message : '排行暂不可用，请重新读取。'
  }

  useEffect(() => {
    cardController.current?.abort()
    cardController.current = null
    setWindowPending(null)
    if (UI_PREVIEW_ONLY) {
      setSnapshot(null)
      setAllPending(null)
      return
    }
    const controller = new AbortController()
    allController.current = controller
    setAllPending(scope)
    setSnapshot(previous => {
      const next = previous?.scope === scope ? previous : emptySnapshot(scope)
      return { ...next, error: undefined, unauthorized: undefined, errors: {} }
    })
    const live = () => !controller.signal.aborted && allController.current === controller &&
      activeScope.current === scope && sessionStorage.getItem('siteadmin-token') === sessionToken
    const params = new URLSearchParams({ sort, long_window: longWindow })
    void requestJSON<RankingResponse>(`/api/risk/leaderboards?${params}`, {}, controller.signal).then(value => {
      if (!live()) return
      validateResponse(value, sort, [...shortWindows, longWindow])
      const windows = Object.fromEntries(value.windows.map(window => [window.window, window])) as Snapshot['windows']
      setSnapshot({
        scope, windows, errors: {}, warning: value.warnings, notes: value.data_notes,
        asOf: value.as_of, timezone: value.timezone, quotaPerUnit: value.quota_per_unit,
      })
    }).catch(cause => {
      if (!live()) return
      const error = report(cause, scope)
      setSnapshot(previous => ({
        ...(previous?.scope === scope ? previous : emptySnapshot(scope)), error,
        unauthorized: cause instanceof ApiError && cause.status === 401,
      }))
    }).finally(() => {
      if (live()) {
        allController.current = null
        setAllPending(null)
        setCompleted(value => value + 1)
      }
    })
    return () => {
      controller.abort()
      if (allController.current === controller) allController.current = null
      cardController.current?.abort()
      cardController.current = null
    }
  }, [scope, sort, longWindow, revision, sessionToken])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || interval === 0 || allPending === scope || snapshot?.scope === scope && snapshot.unauthorized) {
      setCountdown(interval)
      return
    }
    const deadline = Date.now() + interval * 1000
    const tick = () => {
      const remaining = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setCountdown(remaining)
      if (remaining === 0 && document.visibilityState === 'visible' &&
        !allController.current && !cardController.current) {
        window.clearInterval(timer)
        setRevision(value => value + 1)
      }
    }
    const timer = window.setInterval(tick, 1000)
    tick()
    return () => window.clearInterval(timer)
  }, [interval, scope, completed, allPending, snapshot?.unauthorized])

  useEffect(() => () => {
    allController.current?.abort()
    cardController.current?.abort()
  }, [])

  async function refreshCard(window: WindowKey) {
    if (UI_PREVIEW_ONLY || allController.current || cardController.current) return
    const controller = new AbortController()
    cardController.current = controller
    setWindowPending({ scope, window })
    setSnapshot(previous => {
      const next = previous?.scope === scope ? previous : emptySnapshot(scope)
      const errors = { ...next.errors }
      delete errors[window]
      return { ...next, errors }
    })
    const live = () => !controller.signal.aborted && cardController.current === controller &&
      activeScope.current === scope && sessionStorage.getItem('siteadmin-token') === sessionToken
    try {
      const params = new URLSearchParams({ sort, window })
      const value = await requestJSON<RankingResponse>(`/api/risk/leaderboards?${params}`, {}, controller.signal)
      if (!live()) return
      validateResponse(value, sort, [window])
      setSnapshot(previous => {
        const next = previous?.scope === scope ? previous : emptySnapshot(scope)
        const errors = { ...next.errors }
        delete errors[window]
        return {
          ...next, windows: { ...next.windows, [window]: value.windows[0] }, errors,
          warning: [...new Set([...next.warning, ...value.warnings])], notes: value.data_notes ?? next.notes, asOf: value.as_of,
          timezone: value.timezone, quotaPerUnit: value.quota_per_unit,
        }
      })
    } catch (cause) {
      if (!live()) return
      const error = report(cause, scope)
      setSnapshot(previous => {
        const next = previous?.scope === scope ? previous : emptySnapshot(scope)
        return {
          ...next, errors: { ...next.errors, [window]: error },
          unauthorized: cause instanceof ApiError && cause.status === 401,
        }
      })
    } finally {
      if (cardController.current === controller) {
        cardController.current = null
        if (activeScope.current === scope) setWindowPending(null)
      }
    }
  }

  const current = snapshot?.scope === scope ? snapshot : null
  const allBusy = !UI_PREVIEW_ONLY && allPending === scope
  const singleBusy = windowPending?.scope === scope ? windowPending.window : null
  const refreshing = allBusy || singleBusy !== null
  const unit = current?.quotaPerUnit ?? 0
  const metric = (item: RankingItem) => sort === 'quota'
    ? unit > 0 ? currencyFormat.format(item.quota_used / unit) : `${count(item.quota_used)} 额度`
    : sort === 'failure_rate' ? percent(item.failure_rate) : count(item.request_count)

  function renderWindow(window: WindowKey, extended = false) {
    const data = current?.windows[window]
    const cardError = current?.errors[window]
    const loading = allBusy || singleBusy === window
    return <Card className={cn('rr-card', extended && 'rr-card--extended')} key={window} aria-busy={loading}>
      <CardHeader className="rr-card-header">
        <div className="rr-card-heading">
          <CardTitle className="rr-card-title"><Activity className="h-4 w-4 text-primary" />{windowLabels[window]}</CardTitle>
          <div className="rr-card-actions">
            {extended && <Select aria-label="长期排行时间窗口" value={longWindow} onChange={event => setLongWindow(event.target.value as LongWindow)} className="rr-long-window">
              {longWindows.map(value => <option key={value} value={value}>{windowLabels[value]}</option>)}
            </Select>}
            <Button type="button" variant="ghost" size="icon" className="h-8 w-8" disabled={refreshing || UI_PREVIEW_ONLY}
              onClick={() => void refreshCard(window)} aria-label={`刷新${windowLabels[window]}排行`} title={data ? `统计于 ${formatTime(data.window_end, current?.timezone)}` : '刷新此窗口'}>
              <RefreshCw className={cn('h-4 w-4', loading && 'animate-spin')} />
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="rr-card-content">
        {cardError && <div className="rr-card-error" role="alert"><span>{cardError}</span><Button type="button" size="sm" variant="outline" disabled={refreshing} onClick={() => void refreshCard(window)}>重试</Button></div>}
        {loading && !data ? <div className="rr-empty" role="status"><Loader2 className="h-5 w-5 animate-spin" />正在读取排行…</div>
          : data?.items.length ? <ol className="rr-list" aria-label={`${windowLabels[window]} ${sortLabels[sort]} Top 10`}>
            {data.items.map((item, index) => {
              const name = item.display_name || item.username || `用户 #${item.user_id}`
              const banned = item.user_status === 2
              const canBan = item.identity_known && item.role < 10 && item.user_status === 1
              return <li key={item.user_id} className={cn('rr-row', banned && 'rr-row--banned')}>
                <span className={cn('rr-rank', index < 3 && `rr-rank--${index + 1}`)}>{index + 1}</span>
                <button type="button" className="rr-user" onClick={() => callbacks.current.onViewUser(item.user_id, item.username || undefined)}
                  aria-label={`查看 ${name} 的用户详情`} aria-haspopup="dialog" title={item.username && item.display_name ? `${item.display_name} · ${item.username}` : name}>
                  <span className="rr-user-heading"><span className="rr-user-pill"><span className="rr-avatar" aria-hidden="true">{Array.from(name)[0]?.toUpperCase() || '#'}</span><strong>{name}</strong></span>
                    {banned && <Badge variant="destructive" className="rr-state-badge">已封禁</Badge>}
                  </span>
                  <span className="rr-user-meta"><span>ID: {item.user_id}</span><i aria-hidden="true" /><span title={item.ip_count_complete ? '当前窗口内的有效调用出口 IP 数' : '调用出口 IP 未记录或统计未完成'}>IP: {count(item.unique_ips)}</span>
                    {extended && <><i aria-hidden="true" /><span>失败: {percent(item.failure_rate)}</span></>}
                    {!item.identity_known && <span className="rr-unavailable">资料不可用</span>}
                  </span>
                </button>
                <div className="rr-metric"><strong className={sort === 'quota' ? 'text-primary' : ''}>{metric(item)}</strong><small>{sortLabels[sort]}</small></div>
                <Button type="button" variant={banned ? 'secondary' : 'ghost'} size="icon" className={cn('rr-quick-action h-8 w-8', banned && 'rr-quick-action--visible')}
                  disabled={UI_PREVIEW_ONLY || !banned && (!canBan || actionsDisabled) || !item.identity_known}
                  onClick={() => banned ? callbacks.current.onViewUser(item.user_id, item.username || undefined) : callbacks.current.onBanUser(item.user_id)}
                  aria-label={banned ? `查看已封禁用户 ${name}` : `封禁用户 ${name}`}
                  title={banned ? '查看已封禁账号详情' : canBan ? '人工封禁，填写原因与用户备注' : item.role >= 10 ? '管理员账号受保护' : '账号资料或状态不可用'}>
                  {banned ? <ShieldCheck className="h-4 w-4" /> : <ShieldBan className="h-4 w-4" />}
                </Button>
              </li>
            })}
          </ol> : <div className="rr-empty" role="status"><ShieldCheck className="h-8 w-8 opacity-20" />{UI_PREVIEW_ONLY ? '数据获取已暂停' : cardError || current?.error ? '排行数据暂不可用' : data ? '当前窗口暂无调用记录' : '尚未读取排行'}</div>}
      </CardContent>
    </Card>
  }

  return <section className="rr-page" aria-label="实时排行">
    <div className="rr-toolbar">
      <div className="rr-toolbar-summary"><p>各时间窗口 Top 10 · 点击账号查看详情</p></div>
      <div className="rr-toolbar-controls">
        <div className="rr-refresh-select"><Timer className="rr-refresh-icon h-4 w-4" aria-hidden="true" /><Select aria-label="实时排行自动刷新间隔" value={String(interval)} onChange={event => { const seconds = Number(event.target.value); setIntervalSeconds(seconds); setCountdown(seconds) }}>
          {refreshIntervals.map(seconds => <option key={seconds} value={seconds}>{intervalLabel(seconds)}</option>)}
        </Select>{interval > 0 && !UI_PREVIEW_ONLY && !current?.unauthorized && <span className="rr-countdown" aria-label={`${countdown} 秒后自动刷新`}>{countdownLabel(allBusy ? interval : countdown)}</span>}</div>
        <div className="rr-sort-select"><Select aria-label="实时排行排序" value={sort} onChange={event => setSort(event.target.value as SortKey)}>
          <option value="requests">按请求次数</option><option value="quota">按额度消耗</option><option value="failure_rate">按失败率</option>
        </Select></div>
        <Button type="button" variant="outline" size="sm" className="rr-refresh-all" disabled={refreshing || UI_PREVIEW_ONLY} onClick={() => setRevision(value => value + 1)}>
          <RefreshCw className={cn('mr-2 h-4 w-4', allBusy && 'animate-spin')} />刷新全部
        </Button>
      </div>
    </div>
    {current?.error && <Notice kind="error">{current.error}{Object.keys(current.windows).length > 0 && ' 当前保留上次读取的排行。'}</Notice>}
    {current?.warning.map(warning => <Notice key={warning} kind="warning">{warning}</Notice>)}
    {UI_PREVIEW_ONLY && <Notice>数据获取已暂停，尚未读取实时排行。</Notice>}
    <div className="rr-windows-grid">{shortWindows.map(window => renderWindow(window))}</div>
    {renderWindow(longWindow, true)}
    <div className="rr-footnote"><p>最新刷新 {formatTime(current?.asOf, current?.timezone)} · IP 为对应窗口内的调用出口，与登录 IP 分开统计。排行按已保存的调用日志计数，包含渠道重试；排名不直接认定风险。</p>
      {!!current?.notes?.length && <details className="rr-data-notes"><summary>统计口径</summary><div>{current.notes.map(note => <p key={note}>{note}</p>)}</div></details>}
    </div>
  </section>
}
