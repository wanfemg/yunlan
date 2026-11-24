import { useEffect, useRef, useState } from 'react'
import {
  Activity, AlertTriangle, CheckCircle2, ChevronDown, ChevronLeft, ChevronRight, Clock3,
  Copy, Eye, Fingerprint, Globe, Info, KeyRound, MapPin, Network, RefreshCw,
  ShieldBan, ShieldCheck, Timer, XCircle,
} from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { useIPDetails } from './IPDetailsContext'
import { AdminDialog, EmptyState, Notice } from './ModuleUI'
import { StatCard } from './components/StatCard'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { cn } from './lib/utils'
import { UserIdentityButton } from './components/UserIdentityButton'
import { ScrollRegion } from './components/ScrollRegion'
import { useToast } from './components/Toast'
import './risk-ip-detail.css'

type EventSource = 'api_call' | 'login' | 'registration'
type Identity = {
  id: number; username: string; display_name: string; email: string; status: number; role: number
  known: boolean; request_count: number; used_quota: number
}
type IPInfo = {
  ip: string; address_class: string; country_code?: string; country?: string
  region?: string; city?: string; asn?: number; as_org?: string; isp?: string
  network_type: string; proxy: Record<string, string>; data_source: string
  intelligence?: { matches: { kind: string; prefix: string; source: string }[]; available: boolean; version: string }
}
type Exit = { ip: string; events: number; success: number; failure: number; last_seen_at: number; info: IPInfo | null }
type Actor = { user: Identity; ip_count: number; events: number; ips: Exit[]; truncated: boolean; last_seen_at: number }
type SharedIP = { ip: string; events: number; users: Identity[]; user_count: number; token_ids: number[]; token_count: number; info: IPInfo | null; truncated: boolean }
type Token = { token_id: number; users: Identity[]; user_count: number; events: number; ip_count: number; ips: Exit[]; truncated: boolean }
type SharedDevice = {
  browser_id: string; users: Identity[]; user_count: number; events: number
  first_seen_at: number; last_seen_at: number; ips: string[]; ip_count: number; truncated: boolean
}
type MonitorData = {
  source: EventSource; period: string; window_start: number; window_end: number; as_of: number
  timezone: string; quota_per_unit: number; sync_complete: boolean
  summary: {
    events: number; recorded_ip_events: number; ip_coverage: number | null; unique_ips: number
    shared_ips: number; multi_ip_tokens: number; unassociated_events: number; missing_ip_events: number
    invalid_ip_events: number; non_public_ip_events: number
  }
  shared_ips: SharedIP[]; multi_ip_users: Actor[]; multi_ip_tokens: Token[]; shared_devices: SharedDevice[]
  totals: { shared_ips: number; multi_ip_users: number; multi_ip_tokens: number; shared_devices: number }
  truncated: boolean; event_sampled: boolean; limit: number; exit_limit: number; event_limit: number; warnings: string[]
}
type Props = {
  tokenVersion?: number
  onUnauthorized: () => void
  onViewUser: (id: number, username?: string) => void
  onBanUser: (id: number) => void
}
type Detail =
  | { kind: 'user'; actor: Actor }
  | { kind: 'ip'; item: SharedIP }
  | { kind: 'token'; item: Token }
  | { kind: 'device'; item: SharedDevice }
type Snapshot = { key: string; sessionToken: string | null; data?: MonitorData; error?: string; unauthorized?: boolean }

const sources = [
  { id: 'api_call' as const, label: '调用 IP' },
  { id: 'login' as const, label: '登录 IP' },
  { id: 'registration' as const, label: '注册 IP' },
]
const periods = [['1h', '1 小时内'], ['6h', '6 小时内'], ['24h', '24 小时内'], ['3d', '近 3 天'], ['7d', '近 7 天']]
const addressNames: Record<string, string> = {
  public: '公网', private: '内网', loopback: '回环地址', link_local: '链路本地',
  reserved: '保留地址', invalid: '无效地址', missing: '未记录',
}
const networkNames: Record<string, string> = {
  hosting: '托管网络', residential: '住宅网络', mobile: '移动网络', business: '企业网络', corporate: '企业网络', unknown: '未知',
  non_residential: '非住宅出口（机房 / 代理）',
}
const numberFormat = new Intl.NumberFormat('zh-CN')
const PAGE_SIZE = 15

function initialMonitorQuery() {
  const params = new URLSearchParams(window.location.search)
  const source = params.get('source')
  const period = params.get('period')
  return {
    source: source === 'login' || source === 'registration' ? source : 'api_call' as EventSource,
    period: periods.some(([value]) => value === period) ? period! : '24h',
    ip: (params.get('ip') ?? '').trim().slice(0, 255),
  }
}

function count(value?: number | null) {
  return value == null || !Number.isFinite(value) ? '—' : numberFormat.format(value)
}

function timestamp(value: number, timezone: string) {
  return value ? new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).format(value * 1000) : '未记录'
}

function location(info: IPInfo | null) {
  if (!info) return '归属地未知'
  if (info.address_class !== 'public') return addressNames[info.address_class] ?? '地址类型未知'
  return [...new Set([info.country || info.country_code, info.region, info.city].filter(Boolean))].join(' · ') || '归属地未知'
}

function UserIdentity({ user, onViewUser, compact = false }: {
  user: Identity; onViewUser: Props['onViewUser']; compact?: boolean
}) {
  const title = user.display_name || user.username || `用户 #${user.id}`
  return <div className="min-w-0">
    <UserIdentityButton userId={user.id} username={user.username} displayName={user.display_name}
      email={user.email} compact={compact} onClick={() => onViewUser(user.id, user.username || title)} />
    {!user.known && <small className="mt-1 block text-[11px] text-muted-foreground">当前账号资料不可用</small>}
  </div>
}

function ExitAttributes({ info }: { info: IPInfo | null }) {
  return <div className="rid-network-profile">
    <dl className="rid-network-grid">
      <div><dt>地理位置</dt><dd>{location(info)}</dd></div>
      <div><dt>ASN</dt><dd>{info?.asn ? `AS${info.asn}` : '未知'}</dd></div>
      <div><dt>网络组织</dt><dd>{info?.as_org || '未知'}</dd></div>
      <div><dt>运营商</dt><dd>{info?.isp || '未知'}</dd></div>
      <div><dt>网络类型</dt><dd>{networkNames[info?.network_type ?? 'unknown'] || info?.network_type || '未知'}</dd></div>
    </dl>
    <div className="rid-proxy-section"><span className="rid-section-label">代理属性</span><div className="rid-proxy-chips">{[
      ['vpn', 'VPN'], ['public_proxy', '公共代理'], ['residential_proxy', '住宅代理'], ['tor', 'Tor'],
    ].map(([key, label]) => {
      const value = info?.proxy?.[key]
      const state = value === 'yes' ? 'yes' : value === 'no' ? 'no' : 'unknown'
      const listMatch = info?.intelligence?.matches.some(match => match.kind === key)
      return <span className={`rid-proxy-chip rid-proxy-chip--${state}`} key={key}><span>{label}</span><strong>{state === 'yes' ? listMatch ? '名单命中' : '是' : state === 'no' ? '否' : '未知'}</strong></span>
    })}</div></div>
    {info?.data_source === 'demo-fixture' && <Badge variant="outline" className="w-fit text-[10px]">演示画像</Badge>}
  </div>
}

function CopyIPButton({ ip }: { ip: string }) {
  const { showToast } = useToast()
  async function copy() {
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(ip)
      else {
        const input = document.createElement('textarea')
        input.value = ip
        input.style.position = 'fixed'
        input.style.left = '-9999px'
        document.body.appendChild(input)
        input.select()
        let copied = false
        try { copied = document.execCommand('copy') } finally { input.remove() }
        if (!copied) throw new Error('copy failed')
      }
      showToast('success', 'IP 已复制')
    } catch { showToast('error', '复制失败，请手动选择完整 IP 复制。') }
  }
  return <Button type="button" variant="ghost" size="icon" className="rid-copy-button" title="复制完整 IP"
    aria-label={`复制 IP ${ip}`} onClick={() => void copy()}><Copy className="h-3.5 w-3.5" /></Button>
}

function MonitorPager({ page, total, onPage }: { page: number; total: number; onPage: (page: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  return total > PAGE_SIZE ? <div className="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3 text-xs text-muted-foreground">
    <span>已加载 {count(total)} 项</span><div className="flex items-center gap-3">
      <Button type="button" variant="outline" size="sm" className="h-8 w-8 px-0" aria-label="IP 监控上一页" disabled={page <= 1} onClick={() => onPage(page - 1)}><ChevronLeft className="h-4 w-4" /></Button>
      <span>{page} / {pages}</span>
      <Button type="button" variant="outline" size="sm" className="h-8 w-8 px-0" aria-label="IP 监控下一页" disabled={page >= pages} onClick={() => onPage(page + 1)}><ChevronRight className="h-4 w-4" /></Button>
    </div>
  </div> : null
}

export default function RiskIPMonitor({ tokenVersion = 0, onUnauthorized, onViewUser, onBanUser }: Props) {
  const { openIP, openUser, openToken } = useIPDetails()
  const [initial] = useState(initialMonitorQuery)
  const [source, setSource] = useState<EventSource>(initial.source)
  const [period, setPeriod] = useState(initial.period)
  const [revision, setRevision] = useState(0)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [interval, setIntervalSeconds] = useState(0)
  const [countdown, setCountdown] = useState(0)
  const [page, setPage] = useState(1)
  const [tokenPage, setTokenPage] = useState(1)
  const [detailState, setDetail] = useState<Detail | null>(null)
  const detailScope = useRef('')
  const serial = useRef(0)
  const callbacks = useRef({ onUnauthorized, onViewUser, onBanUser })
  callbacks.current = { onUnauthorized, onViewUser, onBanUser }
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const key = JSON.stringify([source, period, revision, tokenVersion, sessionToken])
  const relationScope = JSON.stringify([source, period, revision, tokenVersion, sessionToken])
  const latestKey = useRef(key)
  const initialIPOpened = useRef(false)
  latestKey.current = key

  useEffect(() => {
    const controller = new AbortController()
    const currentSerial = ++serial.current
    const isCurrent = () => !controller.signal.aborted && currentSerial === serial.current &&
      latestKey.current === key && sessionStorage.getItem('siteadmin-token') === sessionToken
    setSnapshot(null)
    if (!UI_PREVIEW_ONLY) {
      void requestJSON<MonitorData>(`/api/risk/ip-monitor?source=${source}&period=${period}`, {}, controller.signal).then(data => {
        if (!isCurrent()) return
        if (data.source !== source || data.period !== period || !data.summary ||
          !Array.isArray(data.shared_ips) || !Array.isArray(data.multi_ip_users) ||
          !Array.isArray(data.multi_ip_tokens) || !Array.isArray(data.shared_devices)) {
          throw new Error('IP 监控返回与当前来源不一致，请刷新。')
        }
        new Intl.DateTimeFormat('zh-CN', { timeZone: data.timezone }).format()
        setSnapshot({ key, sessionToken, data })
      }).catch(cause => {
        if (!isCurrent()) return
        const unauthorized = cause instanceof ApiError && cause.status === 401
        setSnapshot({ key, sessionToken, unauthorized, error: unauthorized ? '管理员会话已失效，请重新连接。' : cause instanceof Error ? cause.message : 'IP 监控暂不可用。' })
        if (unauthorized) callbacks.current.onUnauthorized()
      })
    }
    return () => controller.abort()
  }, [key, sessionToken, source, period])

  useEffect(() => {
    setPage(1)
    setTokenPage(1)
    setDetail(null)
  }, [source, period, tokenVersion, sessionToken])

  const current = snapshot?.key === key && snapshot.sessionToken === sessionToken ? snapshot : null
  const detail = detailScope.current === relationScope ? detailState : null
  const data = !UI_PREVIEW_ONLY ? current?.data : undefined
  const loading = !UI_PREVIEW_ONLY && !current
  const timezone = data?.timezone || 'Asia/Shanghai'
  const sourceLabel = source === 'api_call' ? '调用' : source === 'login' ? '登录' : '注册'
  const rows = data?.multi_ip_users ?? []
  const tokens = data?.multi_ip_tokens ?? []
  const currentPage = Math.min(page, Math.max(1, Math.ceil(rows.length / PAGE_SIZE)))
  const currentTokenPage = Math.min(tokenPage, Math.max(1, Math.ceil(tokens.length / PAGE_SIZE)))

  useEffect(() => {
    if (!data || !initial.ip || initialIPOpened.current || source !== initial.source || period !== initial.period) return
    initialIPOpened.current = true
    openIP({ ip: initial.ip, source, period, start: data.window_start, end: data.window_end })
  }, [data, initial, source, period, openIP])

  useEffect(() => {
    if (!interval || UI_PREVIEW_ONLY || loading) { setCountdown(interval); return }
    const deadline = Date.now() + interval * 1000
    const timer = window.setInterval(() => {
      const remaining = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setCountdown(remaining)
      if (remaining === 0 && document.visibilityState === 'visible') {
        window.clearInterval(timer)
        setRevision(value => value + 1)
      }
    }, 1000)
    return () => window.clearInterval(timer)
  }, [interval, loading, revision, source, period, tokenVersion])

  function viewUser(id: number, username?: string) {
    setDetail(null)
    openUser(id, username)
  }

  function inspectIP(ip: string, userId?: number, tokenId?: number) {
    setDetail(null)
    openIP({ ip, source, period, userId, tokenId, start: data?.window_start, end: data?.window_end })
  }

  function openDetail(next: Detail) {
    detailScope.current = relationScope
    setDetail(next)
  }

  function canBan(user: Identity) {
    return user.known && user.status === 1 && user.role < 10
  }

  function exitsDetail(exits: Exit[], total: number, truncated: boolean, totalEvents: number, userId?: number, tokenId?: number) {
    const listedEvents = exits.reduce((value, exit) => value + exit.events, 0)
    const successes = exits.reduce((value, exit) => value + exit.success, 0)
    const failures = exits.reduce((value, exit) => value + exit.failure, 0)
    const resolved = successes + failures
    const successRate = resolved > 0 ? `${(successes / resolved * 100).toFixed(1)}%` : '—'
    const sortedExits = [...exits].sort((left, right) => right.events - left.events || left.ip.localeCompare(right.ip))
    return <section className="rid-distribution" aria-label={`${sourceLabel} IP 出口分布`}>
      <div className="rid-summary-grid">
        <div className="rid-summary-item"><span className="rid-summary-icon rid-summary-icon--blue"><Network className="h-4 w-4" /></span><div><span>有效 IP 数</span><strong>{count(total)}</strong><small>已列出 {count(exits.length)} 个</small></div></div>
        <div className="rid-summary-item"><span className="rid-summary-icon rid-summary-icon--violet"><Activity className="h-4 w-4" /></span><div><span>已采集窗口事件</span><strong>{count(totalEvents)}</strong><small>已列出 IP 涉及 {count(listedEvents)} 条</small></div></div>
        <div className="rid-summary-item"><span className="rid-summary-icon rid-summary-icon--green"><CheckCircle2 className="h-4 w-4" /></span><div><span>已列出出口成功率</span><strong>{successRate}</strong><small>成功 {count(successes)} / 失败 {count(failures)}</small></div></div>
      </div>
      <div className="rid-distribution-heading"><h3><Globe className="h-4 w-4" />出口分布</h3><span>按事件量排序 · {count(exits.length)} / {count(total)} 个 IP</span></div>
      {truncated && <p className="rid-scope-note">当前仅列出事件量最高的 {count(exits.length)} 个 IP；成功率仅描述这些出口，占比按已采集窗口事件计算。</p>}
      <ScrollRegion label="IP 出口分布卡片，可上下滚动" size="md" fixed className="rid-exit-scroll">
        {sortedExits.length ? <ol className="rid-exit-list">{sortedExits.map((exit, index) => {
          const share = totalEvents > 0 ? Math.max(0, Math.min(100, exit.events / totalEvents * 100)) : null
          const kind = exit.info?.address_class ?? 'unknown'
          const pending = Math.max(0, exit.events - exit.success - exit.failure)
          return <li key={exit.ip}><article className="rid-exit-card">
            <div className="rid-exit-card-heading">
              <span className={cn('rid-exit-rank', index === 0 && 'rid-exit-rank--first')}>{index + 1}</span>
              <div className="rid-exit-identity"><div className="rid-exit-address"><button type="button" className="ip-inline-button" aria-label={`查看 IP ${exit.ip} 的关联分析`} onClick={() => inspectIP(exit.ip, userId, tokenId)}><code>{exit.ip}</code></button><CopyIPButton ip={exit.ip} /><span className={cn('rid-address-tag', kind === 'public' && 'rid-address-tag--public')}>{addressNames[kind] || '类型未知'}</span></div><p><MapPin className="h-3 w-3" />{location(exit.info)}</p></div>
              <div className="rid-exit-events"><strong>{count(exit.events)}</strong><span>{sourceLabel}事件</span></div>
            </div>
            <div className="rid-share"><div className="rid-share-label"><span>占窗口事件</span><strong>{share == null ? '—' : `${share.toFixed(1)}%`}</strong></div><div className="rid-share-track" role="progressbar" aria-label={`${exit.ip} 占窗口事件`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={share ?? undefined} aria-valuetext={share == null ? '没有可计算的窗口事件' : `${share.toFixed(1)}%`}><span style={{ width: `${share ?? 0}%` }} /></div></div>
            <div className="rid-exit-meta"><span className="rid-event-result rid-event-result--success"><CheckCircle2 className="h-3.5 w-3.5" />成功 {count(exit.success)}</span><span className="rid-event-result rid-event-result--failure"><XCircle className="h-3.5 w-3.5" />失败 {count(exit.failure)}</span>{pending > 0 && <span className="rid-event-pending">待确认 {count(pending)}</span>}<span className="rid-exit-last-seen"><Clock3 className="h-3 w-3" />{timestamp(exit.last_seen_at, timezone)}</span></div>
            <details className="rid-network-details" open={index === 0}><summary><span><Network className="h-3.5 w-3.5" />网络与代理详情</span><ChevronDown className="h-3.5 w-3.5" /></summary><ExitAttributes info={exit.info} /></details>
          </article></li>
        })}</ol> : <div className="rid-empty"><Globe className="h-8 w-8" /><p>当前没有可展示的有效 IP 出口。</p></div>}
      </ScrollRegion>
      <p className="rid-scope-note">成功率按已列出出口中已确认成功与失败的事件计算；占比使用已采集窗口的事件总数，未记录或无效 IP 不会补造成出口。{data?.event_sampled && '当前采集达到事件读取上限，以上为已采集样本。'}</p>
    </section>
  }

  return <section className="min-w-0 space-y-6" aria-label="风控 IP 监控">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <div className="w-36"><Select aria-label="IP 监控时间范围" value={period} onChange={event => setPeriod(event.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</Select></div>
        <div className="inline-flex flex-wrap rounded-lg bg-muted p-1" role="group" aria-label="监控来源">
          {sources.map(item => <Button type="button" key={item.id} size="sm" variant={source === item.id ? 'default' : 'ghost'} aria-pressed={source === item.id} className="h-8 px-3" onClick={() => setSource(item.id)}>{item.label}</Button>)}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Timer className="h-4 w-4 text-muted-foreground" />
        <div className="w-36"><Select aria-label="IP 监控自动刷新" value={String(interval)} onChange={event => setIntervalSeconds(Number(event.target.value))}>
          <option value="0">关闭自动刷新</option><option value="30">每 30 秒</option><option value="60">每 60 秒</option><option value="300">每 5 分钟</option>
        </Select></div>
        {interval > 0 && <span className="min-w-8 text-xs tabular-nums text-primary">{countdown}s</span>}
        <Button type="button" variant="outline" size="sm" disabled={loading || UI_PREVIEW_ONLY} onClick={() => setRevision(value => value + 1)}><RefreshCw className={cn('mr-2 h-4 w-4', loading && 'animate-spin')} />全部刷新</Button>
      </div>
    </div>
    {current?.error && <Notice kind="error"><div className="flex flex-wrap items-center gap-3"><span>{current.error}</span>{!current.unauthorized && <Button type="button" size="sm" variant="outline" onClick={() => setRevision(value => value + 1)}>重试</Button>}</div></Notice>}
    {data?.warnings.map(warning => <Notice key={warning} kind="warning">{warning}</Notice>)}
    {UI_PREVIEW_ONLY && <Notice>数据获取已暂停，尚未读取 IP 监控。</Notice>}
    <div className="grid grid-cols-1 gap-4 xs:grid-cols-2 xl:grid-cols-4">
      <StatCard title="IP 记录覆盖率" value={data?.summary.ip_coverage == null ? '—' : `${(data.summary.ip_coverage * 100).toFixed(1)}%`} subValue={data ? `${count(data.summary.recorded_ip_events)} / ${count(data.summary.events)} 条${sourceLabel}事件` : '有效 IP 事件 / 已采集事件'} icon={Globe} color="blue" />
      <StatCard title={`${sourceLabel}唯一 IP`} value={count(data?.summary.unique_ips)} subValue="含公网、内网和保留地址" icon={Network} color="emerald" />
      <StatCard title="共享 IP" value={count(data?.summary.shared_ips)} subValue={source === 'api_call' ? '关联多个账号或多个令牌' : '关联多个已确认账号'} icon={AlertTriangle} color="orange" />
      <StatCard title={source === 'api_call' ? '多 IP 令牌' : '多 IP 账号'} value={count(source === 'api_call' ? data?.summary.multi_ip_tokens : data?.totals.multi_ip_users)} subValue="当前窗口至少 3 个出口" icon={ShieldCheck} color="rose" />
    </div>
    {data && <div className="flex flex-wrap gap-x-5 gap-y-2 text-xs text-muted-foreground">
      <span>{timestamp(data.window_start, timezone)} — {timestamp(data.window_end - 1, timezone)} · {timezone}</span>
      <span>未关联账号 {count(data.summary.unassociated_events)} 条</span>
      <span>IP 缺失 {count(data.summary.missing_ip_events)} 条</span><span>无效 {count(data.summary.invalid_ip_events)} 条</span>
      <span>内网等非公网 {count(data.summary.non_public_ip_events)} 条</span>
      {data.event_sampled && <Badge variant="warning">事件量达到读取上限</Badge>}
    </div>}

    <Card className="min-w-0">
      <CardHeader className="border-b"><CardTitle className="flex items-center gap-2 text-lg"><Fingerprint className="h-5 w-5 text-violet-500" />{source === 'api_call' ? '调用出口关联' : '登录设备与账号关联'}</CardTitle>
        <CardDescription>{source === 'api_call' ? 'API 调用只分析应用出口，不作为登录设备指纹。' : '只包含探针已确认关联的账号，BrowserID 是浏览器标识，不是硬件身份。'}</CardDescription>
      </CardHeader>
      <CardContent className="grid min-w-0 gap-6 p-4 md:grid-cols-2 sm:p-6">
        <div className="min-w-0 space-y-3">
          <h3 className="flex items-center gap-2 text-sm font-semibold"><AlertTriangle className="h-4 w-4 text-orange-500" />同 IP 多账号{source === 'api_call' ? ' / 多令牌' : ''}<Badge variant="secondary">{count(data?.totals.shared_ips)}</Badge></h3>
          <ScrollRegion label="共享 IP 关联" size="md" fixed className="rounded-xl border bg-muted/10 p-2">
            {data?.shared_ips.length ? <div className="space-y-1">{data.shared_ips.map(item => <button type="button" key={item.ip} className="flex w-full min-w-0 items-center justify-between gap-3 rounded-lg border border-transparent px-3 py-3 text-left text-xs transition-colors hover:border-primary/15 hover:bg-primary/5 active:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" onClick={() => inspectIP(item.ip)}>
              <span className="min-w-0 space-y-1"><code className="block break-all text-foreground">{item.ip}</code><span className="block truncate text-muted-foreground">{location(item.info)}</span></span>
              <span className="shrink-0 text-right text-orange-600">{count(item.user_count)} 账号{source === 'api_call' && <span className="block text-muted-foreground">{count(item.token_count)} 令牌</span>}</span>
            </button>)}</div> : <EmptyState title={loading ? '正在读取关联...' : '当前窗口暂无共享 IP 关联'} />}
          </ScrollRegion>
        </div>
        <div className="min-w-0 space-y-3">
          <h3 className="flex items-center gap-2 text-sm font-semibold"><Globe className="h-4 w-4 text-orange-500" />同账号多 IP<Badge variant="secondary">{count(data?.totals.multi_ip_users)}</Badge></h3>
          <ScrollRegion label="同账号多 IP 关联" size="md" fixed className="rounded-xl border bg-muted/10 p-2">
            {rows.length ? <div className="space-y-2">{rows.map(actor => <div key={actor.user.id} className="flex items-center justify-between gap-2">
              <UserIdentity user={actor.user} onViewUser={viewUser} compact /><Button type="button" variant="ghost" size="sm" className="h-8 shrink-0 text-orange-600" onClick={() => openDetail({ kind: 'user', actor })}>{count(actor.ip_count)} IP</Button>
            </div>)}</div> : <EmptyState title={loading ? '正在读取账号...' : '当前窗口暂无多 IP 账号'} />}
          </ScrollRegion>
        </div>
        {source !== 'api_call' && <div className="min-w-0 space-y-3 md:col-span-2">
          <h3 className="flex items-center gap-2 text-sm font-semibold"><Fingerprint className="h-4 w-4 text-violet-500" />同浏览器标识多账号<Badge variant="secondary">{count(data?.totals.shared_devices)}</Badge></h3>
          {data?.shared_devices.length ? <ScrollRegion label="同浏览器标识关联账号" size="md"><div className="grid min-w-0 gap-2 sm:grid-cols-2">{data.shared_devices.map(item => <button key={item.browser_id} type="button" onClick={() => openDetail({ kind: 'device', item })} className="flex min-w-0 items-center justify-between gap-3 rounded-lg border p-3 text-left transition-colors hover:bg-primary/5 active:bg-primary/10"><code className="min-w-0 truncate text-xs">{item.browser_id}</code><Badge variant="outline">{count(item.user_count)} 账号</Badge></button>)}</div></ScrollRegion>
            : <p className="text-xs text-muted-foreground">{loading ? '正在读取浏览器标识...' : '未观测到同一浏览器标识关联多个已确认账号。'}</p>}
        </div>}
      </CardContent>
    </Card>

    <Card className="min-w-0 overflow-hidden">
      <CardHeader className="border-b"><div className="flex flex-wrap items-center justify-between gap-3"><CardTitle className="flex items-center gap-2 text-lg"><Activity className="h-5 w-5 text-primary" />单用户多 IP（≥3）<Badge variant="secondary">{count(data?.totals.multi_ip_users)}</Badge></CardTitle></div></CardHeader>
      <CardContent className="p-0">
        <Table className="min-w-[850px]" containerClassName="scroll-region scroll-region--lg" containerLabel="多 IP 用户列表">
          <TableHeader><TableRow><TableHead className="w-56">用户详情</TableHead><TableHead className="text-right">IP 数量</TableHead><TableHead className="text-right">{sourceLabel}事件总量</TableHead><TableHead>常用 IP 分布</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader>
          <TableBody>{rows.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE).map(actor => <TableRow key={actor.user.id}>
            <TableCell><UserIdentity user={actor.user} onViewUser={viewUser} /></TableCell>
            <TableCell className="text-right"><Badge variant="outline" className="font-mono text-primary">{count(actor.ip_count)}</Badge></TableCell>
            <TableCell className="text-right font-mono text-lg font-semibold text-primary">{count(actor.events)}</TableCell>
            <TableCell><div className="flex flex-wrap gap-2">{actor.ips.slice(0, 2).map(exit => <button key={exit.ip} type="button" className="ip-inline-button text-xs" aria-label={`查看 IP ${exit.ip} 的关联分析`} onClick={() => inspectIP(exit.ip, actor.user.id)}><code>{exit.ip}</code></button>)}<Button type="button" size="sm" variant="outline" className="h-7 px-2 text-xs text-primary" onClick={() => openDetail({ kind: 'user', actor })}>+{Math.max(0, actor.ip_count - 2)}</Button></div></TableCell>
            <TableCell><div className="flex justify-end gap-1"><Button type="button" size="icon" variant="ghost" className="h-8 w-8 text-primary" aria-label={`查看用户 ${actor.user.id} 的详情`} onClick={() => viewUser(actor.user.id, actor.user.username)}><Eye className="h-4 w-4" /></Button><Button type="button" size="icon" variant="ghost" className="h-8 w-8 text-destructive" aria-label={`封禁用户 ${actor.user.id}`} title={canBan(actor.user) ? '人工封禁，需填写原因' : '账号受保护、已封禁或资料不可用'} disabled={UI_PREVIEW_ONLY || !canBan(actor.user)} onClick={() => callbacks.current.onBanUser(actor.user.id)}><ShieldBan className="h-4 w-4" /></Button></div></TableCell>
          </TableRow>)}</TableBody>
        </Table>
        {!rows.length && <EmptyState title={loading ? '正在读取多 IP 账号...' : current?.error ? '数据暂不可用' : '当前窗口暂无至少 3 个出口的账号'} />}
        <MonitorPager page={currentPage} total={rows.length} onPage={setPage} />
      </CardContent>
    </Card>

    {source === 'api_call' && <Card className="min-w-0 overflow-hidden">
      <CardHeader className="border-b"><CardTitle className="flex items-center gap-2 text-lg"><KeyRound className="h-5 w-5 text-primary" />多 IP 令牌（≥3）<Badge variant="secondary">{count(data?.totals.multi_ip_tokens)}</Badge></CardTitle><CardDescription>显示令牌 ID 与已归属账号，不显示令牌密钥。</CardDescription></CardHeader>
      <CardContent className="p-0"><Table className="min-w-[720px]" containerClassName="scroll-region scroll-region--lg" containerLabel="多 IP 令牌列表">
        <TableHeader><TableRow><TableHead>令牌</TableHead><TableHead>所属账号</TableHead><TableHead className="text-right">IP 数量</TableHead><TableHead className="text-right">调用事件</TableHead><TableHead className="text-right">分布</TableHead></TableRow></TableHeader>
        <TableBody>{tokens.slice((currentTokenPage - 1) * PAGE_SIZE, currentTokenPage * PAGE_SIZE).map(item => <TableRow key={item.token_id}>
          <TableCell className="font-mono text-sm"><button type="button" className="ip-inline-button" aria-label={`查看令牌 ${item.token_id} 的分析`} onClick={() => openToken(item.token_id, undefined, period, data ? { start: data.window_start, end: data.window_end } : undefined)}>#{item.token_id}</button></TableCell><TableCell><div className="flex flex-wrap gap-2">{item.users.map(user => <UserIdentity key={user.id} user={user} onViewUser={viewUser} compact />)}</div></TableCell>
          <TableCell className="text-right font-mono text-primary">{count(item.ip_count)}</TableCell><TableCell className="text-right font-mono">{count(item.events)}</TableCell>
          <TableCell className="text-right"><Button type="button" size="sm" variant="ghost" onClick={() => openDetail({ kind: 'token', item })}><Eye className="mr-1 h-4 w-4" />查看出口</Button></TableCell>
        </TableRow>)}</TableBody>
      </Table>{!tokens.length && <EmptyState title={loading ? '正在读取令牌出口...' : '当前窗口暂无至少 3 个出口的令牌'} />}<MonitorPager page={currentTokenPage} total={tokens.length} onPage={setTokenPage} /></CardContent>
    </Card>}

    {data?.truncated && <Notice>关联列表每类最多展示前 {data.limit} 项；卡片总数仍按当前已采集窗口计算。缩小时间范围可查看更完整的关联列表。</Notice>}
    <p className="text-xs leading-relaxed text-muted-foreground">此页显示出口关系与采集覆盖，不直接认定账号共享或令牌泄漏。未认证用户名尝试不归属于被填写的账号；登录与注册浏览器标识也不与 API 调用出口混算。</p>

    {detail && <AdminDialog wide title={detail.kind === 'user' ? `${detail.actor.user.display_name || detail.actor.user.username || `用户 #${detail.actor.user.id}`} · ${sourceLabel} IP 分布`
      : detail.kind === 'ip' ? `共享 IP · ${detail.item.ip}` : detail.kind === 'token' ? `令牌 #${detail.item.token_id} · 调用 IP` : '同浏览器标识关联'} onClose={() => setDetail(null)}>
      <div className="rid-dialog">
        {detail.kind === 'user' && <><div className="rid-context"><span className="rid-context-caption">当前账号 · {sourceLabel}来源</span><UserIdentity user={detail.actor.user} onViewUser={viewUser} compact /></div>{exitsDetail(detail.actor.ips, detail.actor.ip_count, detail.actor.truncated, detail.actor.events, detail.actor.user.id)}</>}
        {detail.kind === 'token' && <><div className="rid-context"><button type="button" className="ip-inline-button rid-context-caption" onClick={() => { setDetail(null); openToken(detail.item.token_id, undefined, period, data ? { start: data.window_start, end: data.window_end } : undefined) }}><KeyRound className="h-3.5 w-3.5" />令牌 #{detail.item.token_id}</button><ScrollRegion label="令牌关联用户" size="sm" className="flex flex-wrap gap-2">{detail.item.users.map(user => <UserIdentity key={user.id} user={user} onViewUser={viewUser} compact />)}</ScrollRegion></div>{exitsDetail(detail.item.ips, detail.item.ip_count, detail.item.truncated, detail.item.events, undefined, detail.item.token_id)}</>}
        {detail.kind === 'ip' && <><div className="rid-single-ip"><span className="rid-single-ip-icon"><Globe className="h-5 w-5" /></span><button type="button" className="ip-inline-button" onClick={() => inspectIP(detail.item.ip)}><code>{detail.item.ip}</code></button><CopyIPButton ip={detail.item.ip} /></div><ExitAttributes info={detail.item.info} /><ScrollRegion label="IP 关联用户" size="sm" className="flex flex-wrap gap-2">{detail.item.users.map(user => <UserIdentity key={user.id} user={user} onViewUser={viewUser} />)}</ScrollRegion>
          {source === 'api_call' && <div className="space-y-2"><p className="text-xs text-muted-foreground">令牌 ID（{count(detail.item.token_count)} 个）</p><div className="flex flex-wrap gap-2">{detail.item.token_ids.map(id => <button type="button" key={id} className="ip-inline-button" onClick={() => { setDetail(null); openToken(id, undefined, period, data ? { start: data.window_start, end: data.window_end } : undefined) }}><code>#{id}</code></button>)}</div></div>}
          {detail.item.truncated && <p className="text-xs text-muted-foreground">每种关联最多列出前 {data?.exit_limit ?? 50} 项。</p>}</>}
        {detail.kind === 'device' && <><div className="rid-device-header"><div className="rid-device-identity"><span className="rid-device-icon"><Fingerprint className="h-5 w-5" /></span><div><span className="rid-context-caption">浏览器标识</span><code className="rid-device-code">{detail.item.browser_id}</code></div></div><Badge variant="secondary">{count(detail.item.user_count)} 个账号</Badge></div>
          <section className="rid-device-section"><h3 className="rid-device-title">关联账号</h3><ScrollRegion label="浏览器标识关联用户" size="sm" className="flex flex-wrap gap-2">{detail.item.users.map(user => <UserIdentity key={user.id} user={user} onViewUser={viewUser} />)}</ScrollRegion></section>
          <dl className="rid-device-time grid gap-3 text-xs sm:grid-cols-2"><div><dt className="text-muted-foreground">首次出现</dt><dd className="mt-1">{timestamp(detail.item.first_seen_at, timezone)}</dd></div><div><dt className="text-muted-foreground">最近出现</dt><dd className="mt-1">{timestamp(detail.item.last_seen_at, timezone)}</dd></div></dl>
          <section className="rid-device-section"><h3 className="rid-device-title">关联 IP</h3><ScrollRegion label="浏览器标识关联 IP" size="sm" className="flex flex-wrap gap-2">{detail.item.ips.map(ip => <button type="button" className="ip-inline-button" key={ip} aria-label={`查看 IP ${ip} 的关联分析`} onClick={() => inspectIP(ip)}><code>{ip}</code></button>)}</ScrollRegion>{detail.item.truncated && <p className="text-xs text-muted-foreground">关联过多，只展示前 {data?.exit_limit ?? 50} 项。</p>}</section></>}
        <p className="rid-dialog-note"><Info className="h-3.5 w-3.5" /><span>关联只描述当前观测，不证明账号本人共享、设备相同或存在恶意。IP 与代理属性不能单独决定封禁。</span></p>
      </div>
    </AdminDialog>}
  </section>
}
