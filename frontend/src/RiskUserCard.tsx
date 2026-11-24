import { useEffect, useRef, useState } from 'react'
import { Activity, CalendarCheck, Fingerprint, Loader2, RefreshCw, ShieldCheck, UserRound } from 'lucide-react'
import { ApiError, get, requestJSON, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import UserLoginObservations from './UserLoginObservations'
import UserRiskAnalysisPanel from './UserRiskAnalysisPanel'
import UserCheckinPanel from './UserCheckinPanel'
import type { SystemMeta } from './types'
import './user-management.css'

type AccountActivity = {
  activity: string
  intensity_7d: string
  requests_24h: number
  requests_7d: number
  requests_30d: number
  active_days_7d: number
  active_days_30d?: number
  successes_30d: number
  failures_30d: number
  last_attempt_at?: number
  last_success_at?: number
}
type Account = {
  id: number
  username: string
  display_name: string
  email: string
  role: number
  status: number
  group: string
  quota: number
  used_quota: number
  upstream_request_count: number
  created_at?: number
  upstream_last_login_at?: number
  remark?: string
  remark_supported?: boolean
  call_activity: AccountActivity
  login_activity: {
    last_success_at?: number
    successes_30d: number
    failures_30d: number
    attempted_account_failures: number
  }
}
type AccountDetail = {
  user: Account
  as_of: number
  history_complete: boolean
  active_days_timezone?: string
}
type Snapshot = {
  scope: string
  data?: AccountDetail
  meta?: SystemMeta
  error?: string
  unauthorized?: boolean
}
export type RiskUserCardProps = {
  userId: number
  username?: string
  tokenVersion?: number
  initialTab?: 'overview' | 'usage' | 'login' | 'risk' | 'checkins'
  onUnauthorized: () => void
}
const activityLabels: Record<string, string> = {
  daily: '日活跃', weekly: '周内调用', monthly: '月内调用', silent: '沉默',
  dormant: '长期未调用', never: '从未调用', unknown: '数据不足',
}
const intensityLabels: Record<string, string> = {
  none: '无调用', low: '低频', regular: '常规', high: '高频', dense: '密集',
}

export default function RiskUserCard({ userId, username, tokenVersion = 0, initialTab = 'overview', onUnauthorized }: RiskUserCardProps) {
  const [tab, setTab] = useState<string>(initialTab)
  const [revision, setRevision] = useState(0)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const sequence = useRef(0)
  const callback = useRef(onUnauthorized)
  callback.current = onUnauthorized
  const session = sessionStorage.getItem('siteadmin-token')
  const scope = JSON.stringify([userId, tokenVersion, session, revision])
  const currentScope = useRef(scope)
  currentScope.current = scope

  useEffect(() => { setTab(initialTab) }, [userId, tokenVersion, initialTab])
  useEffect(() => {
    const controller = new AbortController()
    const serial = ++sequence.current
    const isCurrent = () => !controller.signal.aborted && sequence.current === serial &&
      currentScope.current === scope && sessionStorage.getItem('siteadmin-token') === session
    setSnapshot(null)
    if (UI_PREVIEW_ONLY) return () => controller.abort()
    if (!Number.isSafeInteger(userId) || userId <= 0) {
      setSnapshot({ scope, error: '用户 ID 无效。' })
      return () => controller.abort()
    }
    void Promise.all([
      requestJSON<AccountDetail>(`/api/users/${userId}`, {}, controller.signal),
      get<SystemMeta>('/api/meta', controller.signal),
    ]).then(([data, meta]) => {
      if (!isCurrent()) return
      if (data.user?.id !== userId || !data.user.call_activity || !data.user.login_activity ||
        !(meta.quota_per_unit && meta.quota_per_unit > 0)) {
        throw new Error('当前账号详情返回不完整，请重新读取。')
      }
      setSnapshot({ scope, data, meta })
    }).catch(error => {
      if (!isCurrent()) return
      const unauthorized = error instanceof ApiError && error.status === 401
      setSnapshot({ scope, unauthorized, error: unauthorized ? '管理员会话已失效，请重新连接。'
        : error instanceof Error ? error.message : '读取用户详情失败。' })
      if (unauthorized) callback.current()
    })
    return () => controller.abort()
  }, [scope, session, userId])

  const current = snapshot?.scope === scope ? snapshot : null
  const user = UI_PREVIEW_ONLY ? undefined : current?.data?.user
  const detail = current?.data
  const timezone = detail?.active_days_timezone ?? current?.meta?.timezone ?? 'Asia/Shanghai'
  const unit = current?.meta?.quota_per_unit ?? 500000
  const loading = !UI_PREVIEW_ONLY && !current
  const count = (value?: number) => value == null || !Number.isFinite(value) ? '—' : value.toLocaleString('zh-CN')
  const money = (value: number) => new Intl.NumberFormat('zh-CN', {
    style: 'currency', currency: 'USD', maximumFractionDigits: Math.abs(value / unit) < 1 ? 6 : 2,
  }).format(value / unit)
  const time = (value?: number) => !value || !Number.isFinite(value) ? '未观测' : new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(value * 1000)

  return <div className="um-detail min-w-0 space-y-5">
    <div className="um-profile-card flex flex-wrap items-start justify-between gap-3">
      <div className="flex min-w-0 items-center gap-3">
        <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-full border border-primary/15 bg-primary/10 text-lg font-semibold text-primary" aria-hidden="true">
          {(user?.display_name || user?.username || username || '#').slice(0, 1).toUpperCase()}
        </span>
        <div className="min-w-0"><p className="break-words font-semibold">{user?.display_name || user?.username || username || `用户 #${userId}`}</p>
          <p className="mt-1 break-all text-xs text-muted-foreground">{user?.username || username || '账号'} · ID: {userId}</p>
          {user && <Badge variant="outline" className="mt-2 bg-background/50 font-normal">{user.group || 'default'}</Badge>}</div>
        {user && <Badge variant={user.status === 2 ? 'destructive' : user.status === 1 ? 'success' : 'outline'}>
          {user.status === 2 ? '已封禁' : user.status === 1 ? '启用' : '状态未知'}
        </Badge>}
      </div>
      <Button type="button" size="sm" variant="outline" disabled={loading || UI_PREVIEW_ONLY} onClick={() => setRevision(value => value + 1)}>
        <RefreshCw className={`mr-2 h-4 w-4${loading ? ' animate-spin' : ''}`} />刷新详情
      </Button>
    </div>
    {UI_PREVIEW_ONLY ? <p className="py-8 text-center text-sm text-muted-foreground">数据获取已暂停</p>
      : loading ? <div className="flex min-h-44 items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="h-5 w-5 animate-spin text-primary" />正在读取用户详情…</div>
        : current?.error ? <Notice kind="error">{current.error}</Notice> : null}
    {user && <Tabs value={tab} onValueChange={setTab} className="min-w-0">
      <TabsList className="um-detail-tab-list">
        <TabsTrigger value="overview" className="flex-1 gap-1.5"><UserRound className="h-3.5 w-3.5" />账号概览</TabsTrigger>
        <TabsTrigger value="usage" className="flex-1 gap-1.5"><Activity className="h-3.5 w-3.5" />调用活跃</TabsTrigger>
        <TabsTrigger value="login" className="flex-1 gap-1.5"><Fingerprint className="h-3.5 w-3.5" />登录与设备</TabsTrigger>
        <TabsTrigger value="risk" className="flex-1 gap-1.5"><ShieldCheck className="h-3.5 w-3.5" />风控分析</TabsTrigger>
        <TabsTrigger value="checkins" className="flex-1 gap-1.5"><CalendarCheck className="h-3.5 w-3.5" />签到分析</TabsTrigger>
      </TabsList>
      <TabsContent value="overview" className="mt-5 space-y-4">
        <dl className="um-detail-grid text-sm">
          {[
            ['邮箱', user.email || '未填写'], ['用户角色', user.role >= 100 ? '超级管理员' : user.role >= 10 ? '管理员' : '普通用户'],
            ['分组', user.group || 'default'], ['当前余额', money(user.quota)], ['累计已用额度', money(user.used_quota)],
            ['上游累计请求', count(user.upstream_request_count)], ['注册时间', time(user.created_at)], ['上游最近登录', time(user.upstream_last_login_at)],
          ].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
        </dl>
        <section className="rounded-lg border bg-muted/20 p-3"><h4 className="text-xs text-muted-foreground">NewAPI 用户备注 · 仅管理员可见</h4><p className="mt-2 whitespace-pre-wrap break-words text-sm">{user.remark_supported === false ? '当前 NewAPI 不支持用户备注' : user.remark || '暂无备注'}</p></section>
      </TabsContent>
      <TabsContent value="usage" className="mt-5 space-y-4">
        <div className="flex flex-wrap gap-2"><Badge variant="secondary">{activityLabels[user.call_activity.activity] ?? user.call_activity.activity}</Badge><Badge variant="outline">7 日强度：{intensityLabels[user.call_activity.intensity_7d] ?? '未知'}</Badge></div>
        <dl className="um-profile-stat-grid">
          {[
            ['24 小时请求', count(user.call_activity.requests_24h)], ['7 日请求', count(user.call_activity.requests_7d)],
            ['30 日请求', count(user.call_activity.requests_30d)], ['30 日成功 / 失败', `${count(user.call_activity.successes_30d)} / ${count(user.call_activity.failures_30d)}`],
            ['7 日活跃天数', count(user.call_activity.active_days_7d)], ['30 日活跃天数', count(user.call_activity.active_days_30d)],
            ['最近调用', time(user.call_activity.last_attempt_at)], ['最近成功调用', time(user.call_activity.last_success_at)],
          ].map(([label, value]) => <div key={label}><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1.5 break-words text-sm font-semibold tabular-nums">{value}</dd></div>)}
        </dl>
      </TabsContent>
      <TabsContent value="login" className="mt-5">
        <UserLoginObservations key={user.id} userId={user.id} username={user.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
      </TabsContent>
      <TabsContent value="risk" className="mt-5">
        <UserRiskAnalysisPanel key={user.id} userId={user.id} username={user.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
      </TabsContent>
      <TabsContent value="checkins" className="mt-5">
        <UserCheckinPanel key={user.id} userId={user.id} username={user.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
      </TabsContent>
    </Tabs>}
    {user && !detail?.history_complete && <Notice kind="warning">调用历史覆盖尚未确认完整，未观测记录不等于账号从未使用。</Notice>}
    {detail && <p className="border-t pt-3 text-xs text-muted-foreground">资料统计于 {time(detail.as_of)} · {timezone}</p>}
  </div>
}
