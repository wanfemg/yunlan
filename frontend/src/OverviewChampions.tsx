import { useEffect, useRef, useState } from 'react'
import { Crown, Loader2, RefreshCw, Trophy } from 'lucide-react'
import { ApiError, get, UI_PREVIEW_ONLY } from './api'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { cn } from './lib/utils'

type UserRank = { user_id: number; username: string; count: number; quota: number }
type ChampionData = {
  period: '24h'
  timezone: string
  generated_at: string
  range: { start: string; end: string }
  quota_per_unit: number
  rankings: { requests: UserRank[]; quota: UserRank[] }
}
type Snapshot = {
  key: string
  sessionToken: string | null
  data?: ChampionData
  error?: string
  unauthorized?: boolean
}
type Props = {
  tokenVersion?: number
  refreshVersion?: number | string
  onViewUser: (user: { id: number; username?: string }) => void
  onUnauthorized: () => void
}

const counts = new Intl.NumberFormat('zh-CN')
const dollars = new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 4 })

function validateChampions(data: ChampionData) {
  if (!data || data.period !== '24h' || !Number.isFinite(data.quota_per_unit) || data.quota_per_unit <= 0 ||
    !data.range || !Number.isFinite(Date.parse(data.range.start)) || !Number.isFinite(Date.parse(data.range.end)) ||
    Date.parse(data.range.end) <= Date.parse(data.range.start) || !Number.isFinite(Date.parse(data.generated_at)) ||
    !Array.isArray(data.rankings?.requests) || !Array.isArray(data.rankings?.quota)) {
    throw new ApiError('冠军统计数据格式无效，请重试。', 503)
  }
  new Intl.DateTimeFormat('zh-CN', { timeZone: data.timezone }).format()
  for (const winner of [data.rankings.requests[0], data.rankings.quota[0]]) {
    if (winner && (!Number.isSafeInteger(winner.user_id) || winner.user_id <= 0 ||
      typeof winner.username !== 'string' || !Number.isFinite(winner.count) || winner.count < 0 ||
      !Number.isFinite(winner.quota) || winner.quota < 0)) {
      throw new ApiError('冠军用户数据格式无效，请重试。', 503)
    }
  }
}

export function OverviewChampions({ tokenVersion = 0, refreshVersion = 0, onViewUser, onUnauthorized }: Props) {
  const [retry, setRetry] = useState(0)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const requestVersion = useRef(0)
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const key = JSON.stringify([tokenVersion, refreshVersion, retry])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) { setSnapshot(null); return }
    const controller = new AbortController()
    const serial = ++requestVersion.current
    setSnapshot(null)
    const isCurrent = () => !controller.signal.aborted && serial === requestVersion.current &&
      sessionStorage.getItem('siteadmin-token') === sessionToken
    void get<ChampionData>('/api/log-analysis?period=24h&page_size=1', controller.signal).then(data => {
      if (!isCurrent()) return
      validateChampions(data)
      setSnapshot({ key, sessionToken, data })
    }).catch(cause => {
      if (!isCurrent()) return
      const unauthorized = cause instanceof ApiError && cause.status === 401
      setSnapshot({
        key, sessionToken, unauthorized,
        error: unauthorized ? '管理员令牌已失效，请重新验证。'
          : cause instanceof Error ? cause.message : '暂时无法读取冠军统计。',
      })
      if (unauthorized) onUnauthorized()
    })
    return () => controller.abort()
  }, [key, sessionToken, onUnauthorized])

  const current = snapshot?.key === key && snapshot.sessionToken === sessionToken ? snapshot : null
  const data = !UI_PREVIEW_ONLY ? current?.data : undefined
  const loading = !UI_PREVIEW_ONLY && !current
  const clock = data ? new Intl.DateTimeFormat('zh-CN', {
    timeZone: data.timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }) : null
  const updatedClock = data ? new Intl.DateTimeFormat('zh-CN', {
    timeZone: data.timezone, hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }) : null

  return <section className="grid min-w-0 gap-4 md:grid-cols-2" aria-label="近24小时用户冠军">
    {([
      { title: '调用王', kind: 'requests', Icon: Crown, color: 'text-amber-600 dark:text-amber-400', iconBackground: 'bg-amber-50 dark:bg-amber-950/40' },
      { title: '额度消耗王', kind: 'quota', Icon: Trophy, color: 'text-violet-600 dark:text-violet-400', iconBackground: 'bg-violet-50 dark:bg-violet-950/40' },
    ] as const).map(({ title, kind, Icon, color, iconBackground }) => {
      const candidate = data?.rankings[kind][0]
      const winner = candidate && (kind === 'requests' ? candidate.count > 0 : candidate.quota > 0) ? candidate : null
      return <Card key={kind} className="group min-w-0 overflow-hidden transition-all duration-200 hover:-translate-y-0.5 hover:border-primary/25 hover:shadow-lg">
        <CardHeader className="flex flex-row items-center justify-between gap-3 space-y-0 pb-4">
          <CardTitle className="flex min-w-0 items-center gap-2 text-lg">
            <span className={cn('rounded-lg p-2', color, iconBackground)}><Icon className="h-5 w-5" aria-hidden="true" /></span>
            {title}
          </CardTitle>
          <Badge variant="outline">近 24 小时</Badge>
        </CardHeader>
        <CardContent className="space-y-4">
          {UI_PREVIEW_ONLY ? <div className="flex min-h-28 items-center justify-center text-sm text-muted-foreground">数据获取已暂停</div>
            : loading ? <div className="flex min-h-28 items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="h-4 w-4 animate-spin" />正在读取冠军统计...</div>
              : current?.error ? <div className="flex min-h-28 flex-col items-start justify-center gap-3" role="alert">
                <p className="break-words text-sm text-destructive">{current.error}</p>
                {!current.unauthorized && <Button type="button" variant="outline" size="sm" onClick={() => setRetry(value => value + 1)}><RefreshCw className="mr-2 h-4 w-4" />重试</Button>}
              </div>
                : winner ? <button type="button" className="flex min-h-28 w-full flex-wrap items-end justify-between gap-4 rounded-xl border bg-background/60 p-3 text-left transition-colors hover:border-primary/25 hover:bg-primary/5 active:bg-primary/10" onClick={() => onViewUser({ id: winner.user_id, username: winner.username })}>
                  <div className="min-w-0 space-y-2">
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-primary/10 text-sm font-semibold text-primary">{(winner.username.trim()[0] || '#').toLocaleUpperCase()}</span>
                      <span className="min-w-0">
                        <span className="block max-w-40 truncate text-sm font-semibold">{winner.username.trim() || `用户 #${winner.user_id}`}</span>
                        <span className="block text-xs text-muted-foreground">ID: #{winner.user_id}</span>
                      </span>
                    </span>
                    <p className={cn('break-words text-3xl font-bold tabular-nums tracking-tight', color)}>
                      {kind === 'requests' ? counts.format(winner.count) : `$${dollars.format(winner.quota / data!.quota_per_unit)}`}
                      <span className="ml-2 text-sm font-normal text-muted-foreground">{kind === 'requests' ? '次调用' : 'USD'}</span>
                    </p>
                  </div>
                </button>
                  : <div className="flex min-h-28 items-center justify-center text-sm text-muted-foreground">{kind === 'requests' ? '近24小时暂无调用记录' : '近24小时暂无额度消耗'}</div>}
          {data && clock && updatedClock && <div className="space-y-1 border-t pt-3 text-xs text-muted-foreground">
            <p className="break-words">{clock.format(new Date(data.range.start))} — {clock.format(new Date(data.range.end))}</p>
            <p>更新于 {updatedClock.format(new Date(data.generated_at))} · {data.timezone}</p>
          </div>}
        </CardContent>
      </Card>
    })}
  </section>
}

export default OverviewChampions
