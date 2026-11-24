import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  ArrowLeft, ArrowUpRight, CheckCircle2, ChevronLeft, ChevronRight, Clock3,
  Copy, Fingerprint, Globe, KeyRound, Loader2, LogIn, Network, RefreshCw,
  ShieldCheck, UserPlus, Users, X,
} from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, EmptyState, Notice } from './ModuleUI'
import { IPDetailsContext, useIPDetails } from './IPDetailsContext'
import type { IPDetailsActions, IPDetailsTarget, IPEventSource, UserDetailTab } from './IPDetailsContext'
import type { TokenWindow } from './TokenManagement'
import { UserIdentityButton } from './components/UserIdentityButton'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { Tabs, TabsList, TabsTrigger } from './components/ui/tabs'
import { useToast } from './components/Toast'

const UserCard = lazy(() => import('./RiskUserCard'))
const LinkedTokenDialog = lazy(() => import('./TokenManagement').then(module => ({ default: module.TokenAnalysisDialog })))

type Identity = {
  id: number; username: string; display_name: string; email?: string
  status: number; role: number; known: boolean
}
type SourceSummary = {
  events: number; success: number; failure: number; pending: number
  users: number; tokens: number; browsers: number; unassociated_events: number
  first_seen_at: number; last_seen_at: number
}
type IntelligenceMatch = { kind: 'vpn' | 'non_residential' | 'tor'; prefix: string; source: string }
type IPInfo = {
  ip: string; address_class: string; country_code?: string; country?: string; region?: string; city?: string
  asn?: number; as_org?: string; isp?: string; network_type: string; proxy: Record<string, string>
  data_source: string; updated_at: number; field_sources: Record<string, string>
  geo_version?: string; asn_version?: string; isp_version?: string; proxy_version?: string
  intelligence?: { matches: IntelligenceMatch[]; available: boolean; version: string }
}
type AccountActivity = {
  user: Identity; events: number; success: number; failure: number; first_seen_at: number
  last_seen_at: number; token_count: number; browser_count: number
}
type TokenActivity = {
  id: number; name: string; user_id: number; status: number; known: boolean
  events: number; success: number; failure: number; first_seen_at: number; last_seen_at: number
}
type ObservedEvent = {
  id: string; source: IPEventSource; occurred_at: number; result: string; user_id: number
  token_id?: number; method?: string; browser_id?: string; model?: string; device?: Record<string, string>
}
type BrowserActivity = {
  browser_id: string; user_ids: number[]; users: Identity[]; events: number; first_seen_at: number; last_seen_at: number
}
type DetailView = 'users' | 'tokens' | 'events' | 'devices' | 'profile'
type IPDetailsData = {
  info: IPInfo; source: IPEventSource; window_start: number; window_end: number; as_of: number
  timezone: string; quota_per_unit: number; summary: SourceSummary
  sources: { source: IPEventSource; applicable: boolean; summary: SourceSummary | null }[]
  users: AccountActivity[]; tokens: TokenActivity[]; events: ObservedEvent[]; devices: BrowserActivity[]
  user_labels?: Record<string, Identity>
  totals: { users: number; tokens: number; events: number; devices: number }
  view: Exclude<DetailView, 'profile'>; page: number; page_size: number; warnings: string[]; sync_complete: boolean
  metadata?: {
    history_complete?: boolean
    event_sources?: Partial<Record<IPEventSource, { status: string }>>
    intelligence?: {
      available: boolean; loaded_at: number; version: string
      files: { name: string; kind: string; status: 'loaded' | 'missing' | 'invalid'; prefix_count: number }[]
      geo?: { anonymous_available: boolean }
    }
  }
}
type DetailTarget =
  | { kind: 'ip'; value: IPDetailsTarget }
  | { kind: 'user'; id: number; username?: string; tab?: UserDetailTab }
  | { kind: 'token'; id: number; name?: string; period?: string; range?: { start: number; end: number } }

const sources = [
  { id: 'api_call' as const, label: '调用 IP', icon: Globe },
  { id: 'login' as const, label: '登录 IP', icon: LogIn },
  { id: 'registration' as const, label: '注册 IP', icon: UserPlus },
]
const windows = [
  ['1h', '近 1 小时'], ['3h', '近 3 小时'], ['6h', '近 6 小时'], ['12h', '近 12 小时'],
  ['24h', '近 24 小时'], ['3d', '近 3 天'], ['7d', '近 7 天'], ['30d', '近 30 天'], ['90d', '近 90 天'],
]
const addressNames: Record<string, string> = {
  public: '公网', private: '内网', loopback: '回环', link_local: '链路本地', reserved: '保留地址',
  missing: '未记录', invalid: '无效地址',
}
const networkNames: Record<string, string> = {
  unknown: '未知', residential: '住宅网络', mobile: '移动网络', hosting: '托管网络', corporate: '企业网络', business: '企业网络',
  non_residential: '非住宅出口（机房 / 代理）',
}
const proxyNames = [['vpn', 'VPN'], ['public_proxy', '公共代理'], ['residential_proxy', '住宅代理'], ['tor', 'Tor']] as const
const fieldNames: Record<string, string> = {
  geography: '地域', asn: 'ASN', isp: '运营商', network_type: '网络类型',
  proxy_vpn: 'VPN', proxy_public_proxy: '公共代理', proxy_residential_proxy: '住宅代理', proxy_tor: 'Tor',
}
const dataSourceNames: Record<string, string> = {
  unavailable: '未配置可用数据源', 'demo-fixture': '演示画像',
  offline_intelligence: '本地 IP 识别名单', mixed_offline: '离线数据库与识别名单',
  offline_maxmind: 'MaxMind 离线数据库', offline_maxmind_city: 'MaxMind 地域库',
  offline_maxmind_asn: 'MaxMind ASN 库', offline_maxmind_isp: 'MaxMind ISP 库',
  offline_maxmind_anonymous: 'MaxMind 匿名 IP 库', x4bnet_vpn: 'X4BNet VPN 名单',
  x4bnet_non_residential: 'X4BNet 非住宅出口名单', tor_project_exit_list: 'Tor Project 官方出口名单',
}
const intelligenceKinds: Record<string, string> = {
  vpn: 'VPN 名单命中', non_residential: '非住宅出口名单命中', tor: 'Tor 出口名单命中',
}
const numbers = new Intl.NumberFormat('zh-CN')
const count = (value?: number | null) => value == null || !Number.isFinite(value) ? '—' : numbers.format(value)
const PAGE_SIZE = 15

function sourceLabel(source: IPEventSource) { return sources.find(item => item.id === source)!.label }
function location(info: IPInfo) {
  if (info.address_class !== 'public') return addressNames[info.address_class] ?? '地址类型未知'
  return [...new Set([info.country || info.country_code, info.region, info.city].filter(Boolean))].join(' · ') || '归属地未知'
}
function timestamp(value: number | undefined, timezone: string) {
  if (!value) return '未观测'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).format(value * 1000)
}

export function IPDetailsProvider({ children, tokenVersion, quotaPerUnit, onUnauthorized }: {
  children: ReactNode; tokenVersion: number; quotaPerUnit: number; onUnauthorized: () => void
}) {
  const session = sessionStorage.getItem('siteadmin-token')
  const scope = JSON.stringify([tokenVersion, session])
  const scopeRef = useRef(scope)
  scopeRef.current = scope
  const [navigation, setNavigation] = useState<{ scope: string; stack: DetailTarget[] }>({ scope, stack: [] })
  const stack = navigation.scope === scope ? navigation.stack : []
  const active = stack[stack.length - 1]
  const push = useCallback((target: DetailTarget) => {
    setNavigation(previous => {
      const items = previous.scope === scopeRef.current ? previous.stack : []
      if (JSON.stringify(items[items.length - 1]) === JSON.stringify(target)) return previous
      return { scope: scopeRef.current, stack: [...items.slice(-9), target] }
    })
  }, [])
  const close = useCallback(() => setNavigation(previous => ({
    scope: scopeRef.current, stack: previous.scope === scopeRef.current ? previous.stack.slice(0, -1) : [],
  })), [])
  const updateIPTarget = useCallback((value: IPDetailsTarget) => setNavigation(previous => {
    if (previous.scope !== scopeRef.current || previous.stack[previous.stack.length - 1]?.kind !== 'ip') return previous
    return { ...previous, stack: [...previous.stack.slice(0, -1), { kind: 'ip', value }] }
  }), [])
  const actions = useMemo<IPDetailsActions>(() => ({
    openIP: target => { if (target.ip.trim()) push({ kind: 'ip', value: { ...target, ip: target.ip.trim() } }) },
    openUser: (id, username, tab) => { if (Number.isSafeInteger(id) && id > 0) push({ kind: 'user', id, username, tab }) },
    openToken: (id, name, period, range) => { if (Number.isSafeInteger(id) && id > 0) push({ kind: 'token', id, name, period, range }) },
  }), [push])
  const handleTokenError = useCallback((error: unknown) => {
    if (error && typeof error === 'object' && 'status' in error && error.status === 401) onUnauthorized()
    return error instanceof Error ? error.message : '令牌分析暂不可用。'
  }, [onUnauthorized])
  const entityChanged = useCallback(() => window.dispatchEvent(new Event('siteadmin:entities-changed')), [])
  const loading = <div className="ip-detail-loading" role="status"><Loader2 className="h-5 w-5 animate-spin" />正在读取详情…</div>

  return <IPDetailsContext.Provider value={actions}>
    {children}
    {active?.kind === 'ip' && <IPDetailsDialog key={stack.length} target={active.value} tokenVersion={tokenVersion}
      onUnauthorized={onUnauthorized} onClose={close} canGoBack={stack.length > 1} onRemember={updateIPTarget} />}
    {active?.kind === 'user' && <AdminDialog title={`用户详情 · ${active.username || `#${active.id}`}`} wide onClose={close}>
      {stack.length > 1 && <Button type="button" variant="ghost" size="sm" onClick={close} className="mb-3 gap-2"><ArrowLeft className="h-4 w-4" />返回上一层</Button>}
      <Suspense fallback={loading}><UserCard userId={active.id} username={active.username} initialTab={active.tab}
        tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} /></Suspense>
    </AdminDialog>}
    {active?.kind === 'token' && <Suspense fallback={<AdminDialog title="令牌分析" onClose={close}>{loading}</AdminDialog>}>
      <LinkedTokenDialog target={{ id: active.id, name: active.name || '' }} tokenVersion={tokenVersion}
        initialWindow={(windows.some(([value]) => value === active.period) ? active.period : '7d') as TokenWindow} initialRange={active.range}
        handleError={handleTokenError} quotaPerUnit={quotaPerUnit} onClose={close}
        onChanged={entityChanged} onViewUser={id => actions.openUser(id)} />
    </Suspense>}
  </IPDetailsContext.Provider>
}

function IPDetailsDialog({ target, tokenVersion, onUnauthorized, onClose, canGoBack, onRemember }: {
  target: IPDetailsTarget; tokenVersion: number; onUnauthorized: () => void; onClose: () => void; canGoBack: boolean
  onRemember: (target: IPDetailsTarget) => void
}) {
  const { openUser, openToken } = useIPDetails()
  const { showToast } = useToast()
  const [source, setSource] = useState(target.source)
  const [period, setPeriod] = useState(windows.some(([value]) => value === target.period) ? target.period! : '7d')
  const [fixedWindow, setFixedWindow] = useState(target.start != null && target.end != null ? { start: target.start, end: target.end } : null)
  const [userId, setUserId] = useState(target.userId)
  const [tokenId, setTokenId] = useState(target.source === 'api_call' ? target.tokenId : undefined)
  const [view, setView] = useState<DetailView>(target.view ?? 'users')
  const [page, setPage] = useState(target.page ?? 1)
  const [revision, setRevision] = useState(0)
  const [snapshot, setSnapshot] = useState<{ key: string; scope: string; data?: IPDetailsData; error?: string } | null>(null)
  const callback = useRef(onUnauthorized)
  callback.current = onUnauthorized
  const session = sessionStorage.getItem('siteadmin-token')
  const params = new URLSearchParams({ source, period, view: view === 'profile' ? 'users' : view, page: String(page), page_size: String(PAGE_SIZE) })
  if (userId) params.set('user_id', String(userId))
  if (source === 'api_call' && tokenId) params.set('token_id', String(tokenId))
  if (fixedWindow) { params.set('start', String(fixedWindow.start)); params.set('end', String(fixedWindow.end)) }
  const path = `/api/ip/details/${encodeURIComponent(target.ip)}?${params}`
  const key = JSON.stringify([path, tokenVersion, session, revision])
  const latest = useRef(key)
  latest.current = key
  const contextScope = JSON.stringify([target.ip, source, period, fixedWindow, userId, tokenId, tokenVersion, session])
  const syncScope = `${contextScope}:${revision}`
  const lastSyncScope = useRef('')
  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    const current = () => !controller.signal.aborted && latest.current === key && sessionStorage.getItem('siteadmin-token') === session
    const requestPath = lastSyncScope.current === syncScope ? `${path}&sync=false` : path
    void requestJSON<IPDetailsData>(requestPath, {}, controller.signal).then(data => {
      if (!current()) return
      if (data.source !== source || !data.info || !data.summary || !data.totals || !Array.isArray(data.sources) ||
        !Array.isArray(data.users) || !Array.isArray(data.tokens) || !Array.isArray(data.events) || !Array.isArray(data.devices)) {
        throw new Error('IP 详情返回不完整，请刷新。')
      }
      new Intl.DateTimeFormat('zh-CN', { timeZone: data.timezone }).format()
      if (data.sync_complete) lastSyncScope.current = syncScope
      setSnapshot({ key, scope: contextScope, data })
      const total = data.totals[view === 'profile' ? 'users' : view]
      const last = Math.max(1, Math.ceil(total / data.page_size))
      if (page > last) setPage(last)
    }).catch(cause => {
      if (!current()) return
      setSnapshot({ key, scope: contextScope, error: cause instanceof Error ? cause.message : 'IP 详情暂不可用。' })
      if (cause instanceof ApiError && cause.status === 401) callback.current()
    })
    return () => controller.abort()
  }, [key, path, session, source, page, view, syncScope, contextScope])
  const current = snapshot?.key === key ? snapshot : null
  const data = current?.data ?? (snapshot?.scope === contextScope ? snapshot.data : undefined)
  const loading = !UI_PREVIEW_ONLY && !current
  const info = data?.info
  const zone = data?.timezone ?? 'Asia/Shanghai'
  const total = data?.totals[view === 'profile' ? 'users' : view] ?? 0
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  function changeSource(next: IPEventSource) {
    if (next !== 'api_call' && tokenId) { setTokenId(undefined); showToast('info', '已移除仅适用于调用事件的令牌筛选。') }
    setSource(next); setPage(1)
    if (next !== 'api_call' && view === 'tokens') setView('users')
    if (next === 'api_call' && view === 'devices') setView('users')
  }
  function fullPageURL() {
    const query = new URLSearchParams(params)
    query.set('ip', info?.ip || target.ip)
    query.set('view', 'addresses')
    query.delete('page'); query.delete('page_size')
    if (data) { query.set('start', String(data.window_start)); query.set('end', String(data.window_end)) }
    return `/ip-analysis?${query}`
  }
  async function copy() {
    try { await navigator.clipboard.writeText(info?.ip || target.ip); showToast('success', 'IP 已复制') }
    catch { showToast('error', '复制失败，请手动复制 IP。') }
  }
  function remember() {
    onRemember({ ...target, source, period, userId, tokenId,
      start: data?.window_start ?? fixedWindow?.start, end: data?.window_end ?? fixedWindow?.end, view, page })
  }
  function viewUser(id: number, username?: string, tab?: UserDetailTab) { remember(); openUser(id, username, tab) }
  function viewToken(id: number, name?: string) {
    remember()
    openToken(id, name, period, data ? { start: data.window_start, end: data.window_end } : fixedWindow ?? undefined)
  }
  const userButton = (id: number, supplied?: Identity) => {
    const user = supplied ?? data?.user_labels?.[String(id)]
    return id > 0
    ? <UserIdentityButton userId={id} username={user?.username} displayName={user?.display_name} compact
      onClick={() => viewUser(id, user?.username)} />
    : <span className="text-xs text-muted-foreground">未确认账号归属</span>
  }

  return <AdminDialog title="IP 分析详情" wide onClose={onClose}>
    <div className="ip-detail-workspace">
      <header className="ip-detail-identity">
        <div className="ip-detail-address">
          {canGoBack && <Button type="button" size="icon" variant="ghost" className="h-8 w-8 shrink-0" title="返回上一层" aria-label="返回上一层" onClick={onClose}><ArrowLeft className="h-4 w-4" /></Button>}
          <Globe className="h-5 w-5 shrink-0 text-primary" />
          <div className="min-w-0"><code>{info?.ip || target.ip}</code><p>{info ? location(info) : sourceLabel(source)}</p></div>
          <Button type="button" variant="ghost" size="icon" className="h-8 w-8 shrink-0" title="复制 IP" aria-label="复制 IP" onClick={() => void copy()}><Copy className="h-4 w-4" /></Button>
        </div>
        <div className="ip-detail-tools">
          <Select aria-label="IP 详情时间范围" value={period} onChange={event => { setPeriod(event.target.value); setFixedWindow(null); setPage(1) }}>
            {windows.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </Select>
          <Button type="button" variant="outline" size="icon" className="h-9 w-9" title="刷新 IP 详情" aria-label="刷新 IP 详情" disabled={loading || UI_PREVIEW_ONLY} onClick={() => setRevision(value => value + 1)}><RefreshCw className={`h-4 w-4${loading ? ' animate-spin' : ''}`} /></Button>
          <Button type="button" variant="outline" size="sm" asChild><a href={fullPageURL()}><ArrowUpRight className="mr-1.5 h-4 w-4" />展开分析</a></Button>
        </div>
      </header>
      {(userId || tokenId || fixedWindow) && <div className="ip-detail-scope">
        {userId && <button type="button" onClick={() => { setUserId(undefined); setPage(1) }}>用户 #{userId}<X className="h-3 w-3" /><span className="sr-only">移除用户条件</span></button>}
        {tokenId && <button type="button" onClick={() => { setTokenId(undefined); setPage(1) }}>令牌 #{tokenId}<X className="h-3 w-3" /><span className="sr-only">移除令牌条件</span></button>}
        {fixedWindow && <span><Clock3 className="h-3 w-3" />{timestamp(fixedWindow.start, zone)} 至 {timestamp(fixedWindow.end, zone)}</span>}
      </div>}
      <div className="ip-detail-source-grid" aria-label="同一 IP 的分源记录">
        {sources.map(item => {
          const row = data?.sources.find(value => value.source === item.id)
          const summary = row?.summary
          const notObserved = row?.applicable !== false && item.id !== 'api_call' && data?.metadata?.event_sources?.[item.id]?.status === 'not_observed'
          return <button key={item.id} type="button" className="ip-detail-source" aria-pressed={source === item.id}
            onClick={() => changeSource(item.id)}>
            <span><item.icon className="h-4 w-4" />{item.label}{source === item.id && <CheckCircle2 className="ml-auto h-3.5 w-3.5" />}</span>
            <strong>{row?.applicable === false ? '不适用' : notObserved ? '未观测' : count(summary?.events)}<small>{row?.applicable !== false && !notObserved && '条事件'}</small></strong>
            <small>{row?.applicable === false ? '令牌条件仅适用于调用' : notObserved ? '认证历史覆盖未知' : `${count(summary?.users)} 个账号 · ${count(summary?.failure)} 次失败`}</small>
          </button>
        })}
      </div>
      {UI_PREVIEW_ONLY && <EmptyState title="数据获取已暂停" />}
      {loading && !data && <div className="ip-detail-loading" role="status"><Loader2 className="h-5 w-5 animate-spin" />正在读取 IP 行为与关联记录…</div>}
      {current?.error && <Notice kind="error"><div className="flex flex-wrap items-center gap-2">{current.error}<Button type="button" size="sm" variant="outline" onClick={() => setRevision(value => value + 1)}>重试</Button></div></Notice>}
      {data && <>
        {!!data.warnings?.length && <details className="ip-detail-notes"><summary>数据提示 · {data.warnings.length} 项</summary><ul>{data.warnings.map(note => <li key={note}>{note}</li>)}</ul></details>}
        {source === 'api_call' && data.metadata?.history_complete === false && <p className="ip-detail-caution">仅覆盖已采集的调用历史，当前记录不能还原源日志删除前的行为。</p>}
        <dl className="ip-detail-metrics">
          <div><dt>当前来源账号</dt><dd>{count(data.summary.users)}</dd></div>
          <div><dt>{source === 'api_call' ? '关联令牌' : '成功认证浏览器'}</dt><dd>{count(source === 'api_call' ? data.summary.tokens : data.summary.browsers)}</dd></div>
          <div><dt>成功 / 失败 / 待确认</dt><dd><span className="text-emerald-600">{count(data.summary.success)}</span> / <span className="text-red-600">{count(data.summary.failure)}</span> / {count(data.summary.pending)}</dd></div>
          <div><dt>最近出现</dt><dd className="ip-detail-time">{timestamp(data.summary.last_seen_at, zone)}</dd></div>
        </dl>
        {data.summary.unassociated_events > 0 && <p className="ip-detail-caution">当前来源有 {count(data.summary.unassociated_events)} 条事件尚未确认账号归属，未将填写的账号名称当作已认证身份。</p>}
        <Tabs value={view} onValueChange={value => { setView(value as DetailView); setPage(1) }}>
          <div className="ip-detail-tabs-scroll"><TabsList aria-label="IP 详情内容">
            <TabsTrigger value="users" className="gap-1.5"><Users className="h-3.5 w-3.5" />关联账号 {count(data.totals.users)}</TabsTrigger>
            {source === 'api_call' && <TabsTrigger value="tokens" className="gap-1.5"><KeyRound className="h-3.5 w-3.5" />关联令牌 {count(data.totals.tokens)}</TabsTrigger>}
            <TabsTrigger value="events" className="gap-1.5"><Clock3 className="h-3.5 w-3.5" />事件记录 {count(data.totals.events)}</TabsTrigger>
            {source !== 'api_call' && <TabsTrigger value="devices" className="gap-1.5"><Fingerprint className="h-3.5 w-3.5" />认证浏览器 {count(data.totals.devices)}</TabsTrigger>}
            <TabsTrigger value="profile" className="gap-1.5"><Network className="h-3.5 w-3.5" />网络画像</TabsTrigger>
          </TabsList></div>
        </Tabs>
        {loading && <div className="ip-detail-loading" role="status"><Loader2 className="h-5 w-5 animate-spin" />正在读取记录…</div>}
        {!loading && view === 'users' && <Table containerClassName="scroll-region ip-detail-table" containerLabel="当前 IP 关联账号" className="min-w-[660px]">
          <TableHeader><TableRow><TableHead>账号</TableHead><TableHead>状态</TableHead><TableHead className="text-right">事件 / 失败</TableHead><TableHead className="text-right">{source === 'api_call' ? '令牌数' : '认证浏览器'}</TableHead><TableHead>最近出现</TableHead><TableHead className="text-right">分析</TableHead></TableRow></TableHeader>
          <TableBody>{data.users.map(row => <TableRow key={row.user.id}>
            <TableCell>{userButton(row.user.id, row.user)}{!row.user.known && <small className="text-xs text-muted-foreground">账号资料暂不可用</small>}</TableCell>
            <TableCell><Badge variant={row.user.status === 1 ? 'success' : row.user.status === 2 ? 'destructive' : 'secondary'}>{row.user.status === 1 ? '启用' : row.user.status === 2 ? '已封禁' : '未知'}</Badge></TableCell>
            <TableCell className="text-right tabular-nums">{count(row.events)} / <span className="text-red-600">{count(row.failure)}</span></TableCell>
            <TableCell className="text-right tabular-nums">{count(source === 'api_call' ? row.token_count : row.browser_count)}</TableCell>
            <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{timestamp(row.last_seen_at, zone)}</TableCell>
            <TableCell className="text-right"><Button type="button" size="icon" variant="ghost" className="h-8 w-8" title="查看该账号风控分析" aria-label={`查看用户 ${row.user.id} 的风控分析`} onClick={() => viewUser(row.user.id, row.user.username, 'risk')}><ShieldCheck className="h-4 w-4" /></Button></TableCell>
          </TableRow>)}{!data.users.length && <TableRow><TableCell colSpan={6}><EmptyState title="当前范围未观测到已确认归属的账号" /></TableCell></TableRow>}</TableBody>
        </Table>}
        {!loading && view === 'tokens' && <Table containerClassName="scroll-region ip-detail-table" containerLabel="当前 IP 关联令牌" className="min-w-[600px]">
          <TableHeader><TableRow><TableHead>令牌</TableHead><TableHead>所属用户</TableHead><TableHead>状态</TableHead><TableHead className="text-right">事件 / 失败</TableHead><TableHead>最近出现</TableHead></TableRow></TableHeader>
          <TableBody>{data.tokens.map(row => <TableRow key={row.id}>
            <TableCell><button type="button" className="ip-entity-button" onClick={() => viewToken(row.id, row.name)}><KeyRound className="h-4 w-4" /><span>{row.name || `令牌 #${row.id}`}<small>#{row.id}{!row.known && ' · 元信息未知'}</small></span></button></TableCell>
            <TableCell>{row.user_id ? userButton(row.user_id) : <span className="text-xs text-muted-foreground">未知</span>}</TableCell>
            <TableCell><Badge variant={row.status === 1 ? 'success' : row.status === 2 ? 'destructive' : 'secondary'}>{row.status === 1 ? '启用' : row.status === 2 ? '禁用' : row.known ? '已失效' : '未知'}</Badge></TableCell>
            <TableCell className="text-right tabular-nums">{count(row.events)} / <span className="text-red-600">{count(row.failure)}</span></TableCell>
            <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{timestamp(row.last_seen_at, zone)}</TableCell>
          </TableRow>)}{!data.tokens.length && <TableRow><TableCell colSpan={5}><EmptyState title="当前范围未观测到令牌记录" /></TableCell></TableRow>}</TableBody>
        </Table>}
        {!loading && view === 'events' && <Table containerClassName="scroll-region ip-detail-table" containerLabel="当前 IP 事件记录" className="min-w-[650px]">
          <TableHeader><TableRow><TableHead>时间</TableHead><TableHead>结果</TableHead><TableHead>账号</TableHead><TableHead>{source === 'api_call' ? '令牌 / 模型' : '认证 / 浏览器'}</TableHead></TableRow></TableHeader>
          <TableBody>{data.events.map(event => <TableRow key={`${event.source}:${event.id}`}>
            <TableCell className="whitespace-nowrap text-xs">{timestamp(event.occurred_at, zone)}</TableCell>
            <TableCell><Badge variant={event.result === 'success' ? 'success' : event.result === 'failure' ? 'destructive' : 'outline'}>{event.result === 'success' ? '成功' : event.result === 'failure' ? '失败' : '待确认'}</Badge></TableCell>
            <TableCell>{userButton(event.user_id)}</TableCell>
            <TableCell>{source === 'api_call' ? <div className="space-y-1">{event.token_id ? <button type="button" className="ip-inline-button" onClick={() => viewToken(event.token_id!)}><KeyRound className="h-3 w-3" />令牌 #{event.token_id}</button> : <span className="text-xs text-muted-foreground">未记录令牌</span>}<p className="text-xs text-muted-foreground">{event.model || '模型未知'}</p></div>
              : <div className="max-w-64 space-y-1"><span className="text-xs">{event.method || '方式未知'}</span><p className="break-all font-mono text-xs text-muted-foreground">{event.browser_id || '未记录浏览器'}</p>{event.device?.browser && <p className="text-xs text-muted-foreground">{event.device.browser} · {event.device.platform}</p>}</div>}</TableCell>
          </TableRow>)}{!data.events.length && <TableRow><TableCell colSpan={4}><EmptyState title="当前范围没有事件记录" /></TableCell></TableRow>}</TableBody>
        </Table>}
        {!loading && view === 'devices' && <div className="scroll-region ip-detail-devices" role="region" tabIndex={0} aria-label="已认证浏览器">
          {data.devices.map(device => <article key={device.browser_id} className="ip-detail-device">
            <div className="ip-detail-device-heading"><Fingerprint className="h-4 w-4 text-primary" /><code>{device.browser_id}</code><Badge variant={device.user_ids.length > 1 ? 'warning' : 'secondary'}>{count(device.user_ids.length)} 个账号</Badge></div>
            <div className="flex flex-wrap gap-2">{device.users.map(user => <span key={user.id}>{userButton(user.id, user)}</span>)}</div>
            <p>{count(device.events)} 条成功认证记录 · {timestamp(device.first_seen_at, zone)} 至 {timestamp(device.last_seen_at, zone)}</p>
          </article>)}
          {!data.devices.length && <EmptyState title="当前范围未观测到成功认证的浏览器标识" />}
        </div>}
        {!loading && view === 'profile' && <section className="ip-detail-network" aria-label="IP 网络画像">
          <dl className="ip-detail-profile">
            {[['地址类型', addressNames[data.info.address_class] ?? data.info.address_class], ['归属地', location(data.info)],
              ['ASN', data.info.asn ? `AS${data.info.asn}` : '未知'], ['网络组织', data.info.as_org || '未知'],
              ['运营商 ISP', data.info.isp || '未知'], ['网络类型', networkNames[data.info.network_type] ?? data.info.network_type],
              ['数据来源', dataSourceNames[data.info.data_source] ?? data.info.data_source],
              ['更新时间', timestamp(data.info.updated_at, zone)]].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
          </dl>
          <div className="ip-detail-proxies">{proxyNames.map(([key, label]) => {
            const value = data.info.proxy[key] ?? 'unknown'
            const listMatch = data.info.intelligence?.matches.some(match => match.kind === key)
            return <div key={key}><span>{label}</span><Badge variant={value === 'yes' ? 'warning' : value === 'no' ? 'success' : 'secondary'}>{value === 'yes' ? listMatch ? '名单命中' : '命中' : value === 'no' ? '未命中' : '未知'}</Badge></div>
          })}</div>
          {data.info.intelligence && <section className="ip-detail-intelligence" aria-labelledby="ip-detail-intelligence-title">
            <div className="ip-detail-intelligence-heading"><h4 id="ip-detail-intelligence-title"><ShieldCheck className="h-4 w-4" />本地名单检测依据</h4>
              <Badge variant={data.info.intelligence.available ? 'success' : 'outline'}>{data.info.intelligence.available ? '名单可用' : '名单不可用'}</Badge></div>
            {data.info.intelligence.matches.length > 0 ? <div className="ip-detail-match-list">{data.info.intelligence.matches.map(match => <article key={`${match.kind}:${match.prefix}`}>
              <span>{intelligenceKinds[match.kind] ?? match.kind}</span><code>{match.prefix}</code>
              <small>{dataSourceNames[match.source] ?? match.source}</small>
            </article>)}</div> : <p>{data.info.intelligence.available ? '当前 IP 未命中已加载名单，不能由此排除 VPN、代理或其他匿名出口。' : '尚无可用名单匹配结果，代理属性保持未知。'}</p>}
            <p>非住宅名单包含机房或代理网段，不能单独判断为 VPN。{!data.metadata?.intelligence?.geo?.anonymous_available && '公共代理、住宅代理尚无独立识别数据源。'}</p>
            <details className="ip-detail-notes"><summary>名单版本</summary><p>{data.info.intelligence.version || '暂无版本信息'}</p></details>
          </section>}
          <details className="ip-detail-notes"><summary>字段来源与数据库版本</summary><dl className="ip-detail-profile">
            {Object.entries(data.info.field_sources ?? {}).map(([field, origin]) => <div key={field}><dt>{fieldNames[field] || field}</dt><dd>{dataSourceNames[origin] ?? origin}</dd></div>)}
            {[['地域库', data.info.geo_version], ['ASN 库', data.info.asn_version], ['ISP 库', data.info.isp_version], ['代理库', data.info.proxy_version]].filter(([, value]) => value).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
          </dl></details>
        </section>}
        {!loading && view !== 'profile' && <nav className="ip-detail-pagination" aria-label="IP 详情分页">
          <span>共 {count(total)} 项 · 每页 {PAGE_SIZE} 项</span><div>
            <Button type="button" variant="outline" size="icon" className="h-8 w-8" aria-label="IP 详情上一页" disabled={loading || page <= 1} onClick={() => setPage(value => value - 1)}><ChevronLeft className="h-4 w-4" /></Button>
            <span>{page} / {pages}</span>
            <Button type="button" variant="outline" size="icon" className="h-8 w-8" aria-label="IP 详情下一页" disabled={loading || page >= pages} onClick={() => setPage(value => value + 1)}><ChevronRight className="h-4 w-4" /></Button>
          </div>
        </nav>}
        <footer className="ip-detail-footer"><ShieldCheck className="h-4 w-4 shrink-0" /><p>共享出口和代理属性仅作为关联线索；账号风险须结合认证浏览器、账号行为和调用异常复核。</p><span>{timestamp(data.window_start, zone)} 至 {timestamp(data.window_end, zone)}</span></footer>
      </>}
    </div>
  </AdminDialog>
}
