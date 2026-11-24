import { useEffect, useRef, useState } from 'react'
import {
  Activity, ChevronDown, Fingerprint, Globe, Info, Loader2, LogIn, Monitor, RefreshCw, UserPlus, UserX,
} from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { useIPDetails } from './IPDetailsContext'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import { ScrollRegion } from './components/ScrollRegion'
import './user-management.css'

type IPInfo = {
  ip: string
  address_class: string
  country_code?: string
  country?: string
  region?: string
  city?: string
  asn?: number
  as_org?: string
  isp?: string
  network_type?: string
  proxy?: Record<string, string>
  data_source?: string
  intelligence?: { matches: { kind: string; prefix: string; source: string }[]; available: boolean; version: string }
}
type Observation = {
  id: string
  source: 'api_call' | 'login' | 'registration'
  occurred_at: number
  ip: string
  result: string
  method?: string
  browser_id?: string
  user_agent?: string
  token_id?: number
  device: Record<string, string>
  ip_info: IPInfo | null
  identity: 'verified' | 'unverified_account_attempt'
}
type ObservedDevice = {
  browser_id: string
  first_seen_at: number
  last_seen_at: number
  login_count: number
  registration_count: number
  successes: number
  failures: number
  ips: string[]
  device: Record<string, string>
  user_agent?: string
}
type ObservationsData = {
  user_id: number
  as_of: number
  window_start: number
  window_end: number
  timezone: string
  history_complete: boolean
  coverage?: { api_call: boolean; login: boolean; registration: boolean }
  latest_login: Observation | null
  latest_call: Observation | null
  latest_registration: Observation | null
  logins: Observation[]
  calls: Observation[]
  registrations: Observation[]
  unverified_attempts: Observation[]
  devices: ObservedDevice[]
  totals: { login: number; call: number; registration: number; unverified_attempts: number; devices: number }
  limit: number
}
type Snapshot = {
  scope: string
  sessionToken: string | null
  data?: ObservationsData
  error?: string
  unauthorized?: boolean
}
type Props = {
  userId: number
  username: string
  tokenVersion?: number
  onUnauthorized: () => void
}
type ObservationScope = { userId: number; start: number; end: number }

const addressNames: Record<string, string> = {
  public: '公网', private: '内网', loopback: '回环地址', link_local: '链路本地',
  reserved: '保留地址', missing: '未记录 IP', invalid: '无效 IP', unknown: '地址类型未知',
}
const networkNames: Record<string, string> = {
  hosting: '托管网络', residential: '住宅网络', mobile: '移动网络',
  corporate: '企业网络', business: '企业网络', unknown: '网络类型未知',
  non_residential: '非住宅出口（机房 / 代理）',
}
const methodNames: Record<string, string> = {
  password: '密码', github: 'GitHub', wechat: '微信', telegram: 'Telegram',
  discord: 'Discord', oidc: 'OIDC', linux_do: 'LinuxDO',
}
const deviceFields = [
  ['browser', '浏览器'], ['platform', '操作系统'], ['timezone', '时区'], ['language', '语言'],
  ['screen', '屏幕'], ['color_depth', '色深'], ['touch', '触控'],
] as const
const proxyFields = [
  ['vpn', 'VPN'], ['public_proxy', '公共代理'], ['residential_proxy', '住宅代理'], ['tor', 'Tor'],
] as const
const numberFormat = new Intl.NumberFormat('zh-CN')

function count(value: number | undefined) {
  return typeof value === 'number' && Number.isFinite(value) ? numberFormat.format(value) : '—'
}

function time(timestamp: number | undefined, timezone: string) {
  if (!timestamp || !Number.isFinite(timestamp)) return '未观测'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit',
    minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).format(new Date(timestamp * 1000))
}

function location(info: IPInfo | null) {
  if (!info) return '归属地未知'
  const special: Record<string, string> = {
    private: '内网地址，无公网归属地', loopback: '本机回环地址',
    link_local: '链路本地地址', reserved: '保留地址，无公网归属地',
    missing: '未记录 IP，归属地未知', invalid: '地址格式无效，归属地未知',
  }
  if (special[info.address_class]) return special[info.address_class]
  const parts = [info.country || info.country_code, info.region, info.city].filter((value): value is string => Boolean(value))
  return [...new Set(parts)].join(' · ') || '归属地未知'
}

function ObservationResult({ result }: { result: string }) {
  const text = result === 'success' ? '成功' : result === 'failure' ? '失败' : result === 'pending' ? '待确认' : '结果未知'
  return <Badge variant={result === 'success' ? 'success' : result === 'failure' ? 'destructive' : 'outline'} className="text-xs font-normal">{text}</Badge>
}

function ObservationIP({ observation, scope, className }: {
  observation: Observation
  scope: ObservationScope
  className?: string
}) {
  const { openIP } = useIPDetails()
  const valid = Boolean(observation.ip) && observation.ip_info?.address_class !== 'invalid'
  if (!valid) return <code className={className}>{observation.ip || '未记录 IP'}</code>
  return <button type="button" className="ip-inline-button" aria-label={`查看 ${observation.ip} 的${observation.source === 'api_call' ? '调用' : observation.source === 'registration' ? '注册' : '登录'} IP 分析`} onClick={event => {
    event.preventDefault()
    event.stopPropagation()
    openIP({
      ip: observation.ip, source: observation.source, period: '30d',
      start: scope.start, end: scope.end,
      userId: observation.identity === 'verified' ? scope.userId : undefined,
      tokenId: observation.source === 'api_call' ? observation.token_id : undefined,
    })
  }}><code className={className}>{observation.ip}</code></button>
}

function IPIdentity({ observation, scope }: { observation: Observation; scope: ObservationScope }) {
  const info = observation.ip_info
  return <div className="min-w-0 space-y-1.5">
    <ObservationIP observation={observation} scope={scope} className="break-all text-sm font-semibold" />
    <div className="flex flex-wrap gap-1.5">
      <Badge variant="outline" className="text-xs font-normal">{observation.ip ? addressNames[info?.address_class ?? 'unknown'] ?? '地址类型未知' : '未记录 IP'}</Badge>
      {info?.data_source === 'demo-fixture' && <Badge variant="secondary" className="text-xs font-normal">演示画像</Badge>}
    </div>
    <p className="break-words text-xs text-muted-foreground">{location(info)}</p>
  </div>
}

function IPProperties({ info }: { info: IPInfo | null }) {
  return <div className="space-y-2 text-xs">
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5">
      <dt className="text-muted-foreground">ASN</dt>
      <dd className="min-w-0 break-words">{info?.asn ? `AS${info.asn}` : '未知'}{info?.as_org ? ` · ${info.as_org}` : ' · 网络组织未知'}</dd>
      <dt className="text-muted-foreground">运营商</dt><dd className="min-w-0 break-words">{info?.isp || '未知'}</dd>
      <dt className="text-muted-foreground">网络类型</dt><dd className="min-w-0 break-words">{networkNames[info?.network_type ?? 'unknown'] ?? info?.network_type ?? '未知'}</dd>
    </dl>
    <div className="flex flex-wrap gap-x-3 gap-y-1.5 text-muted-foreground" aria-label="代理属性">
      {proxyFields.map(([key, label]) => {
        const value = info?.proxy?.[key]
        const listMatch = info?.intelligence?.matches.some(match => match.kind === key)
        return <span key={key}>{label}：<span className={value === 'yes' ? 'text-foreground' : ''}>{value === 'yes' ? listMatch ? '名单命中' : '是' : value === 'no' ? '否' : '未知'}</span></span>
      })}
    </div>
  </div>
}

function EmptyObservation({ children }: { children: string }) {
  return <div className="rounded-lg border border-dashed bg-muted/20 px-4 py-7 text-center text-sm text-muted-foreground">{children}</div>
}

function LatestObservationCard({ title, description, observation, empty, timezone, Icon, scope }: {
  title: string
  description: string
  observation: Observation | null
  empty: string
  timezone: string
  Icon: typeof LogIn
  scope: ObservationScope
}) {
  return <Card className="um-observation-latest min-w-0 shadow-none">
    <CardHeader className="space-y-2 p-4 pb-3">
      <CardTitle className="flex items-center gap-2 text-sm font-medium"><Icon className="h-4 w-4 text-primary" />{title}</CardTitle>
      <CardDescription className="text-xs leading-relaxed">{description}</CardDescription>
    </CardHeader>
    <CardContent className="space-y-3 p-4 pt-0">
      {observation ? <>
        <IPIdentity observation={observation} scope={scope} />
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><span>{time(observation.occurred_at, timezone)}</span><ObservationResult result={observation.result} /></div>
        <details className="um-network-details"><summary>网络归属与代理属性<ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /></summary><div className="pt-3"><IPProperties info={observation.ip_info} /></div></details>
      </> : <p className="py-3 text-sm leading-relaxed text-muted-foreground">{empty}</p>}
    </CardContent>
  </Card>
}

function AuthenticationTimeline({ observations, timezone, unverified = false, scope }: {
  observations: Observation[]
  timezone: string
  unverified?: boolean
  scope: ObservationScope
}) {
  return <ScrollRegion label={unverified ? '未认证用户名失败尝试，可上下滚动' : '登录与注册轨迹，可上下滚动'} size="md" fixed className="um-records-scroll">
    <ol className="um-auth-list">
      {observations.map(observation => <li key={`${observation.source}:${observation.id}`}>
        <details className="um-auth-record">
          <summary>
            <span className="um-auth-time">{time(observation.occurred_at, timezone)}<small>{unverified ? `${observation.source === 'registration' ? '注册' : '登录'}用户名尝试` : observation.source === 'registration' ? '注册' : methodNames[observation.method ?? ''] || '登录'}</small></span>
            <ObservationResult result={observation.result} />
            <span className="um-auth-ip"><ObservationIP observation={observation} scope={scope} /><small>{location(observation.ip_info)}</small></span>
            <ChevronDown className="um-record-chevron h-4 w-4" aria-hidden="true" />
          </summary>
          <div className="um-auth-expanded">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2"><IPIdentity observation={observation} scope={scope} /><IPProperties info={observation.ip_info} /></div>
            <div className="space-y-2 border-t pt-3 text-xs text-muted-foreground">
              <p>认证方式：{observation.method ? methodNames[observation.method] ?? observation.method : '未记录'}</p>
              <p className="break-all">浏览器标识：{observation.browser_id || '探针未记录'}</p>
              <dl className="um-observation-device-fields">{deviceFields.map(([key, label]) => <div key={key}><dt>{label}</dt><dd>{observation.device?.[key] || '未观测'}</dd></div>)}</dl>
              {observation.user_agent && <details className="um-network-details"><summary>User-Agent<ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /></summary><p className="mt-2 break-all leading-relaxed">{observation.user_agent}</p></details>}
            </div>
          </div>
        </details>
      </li>)}
    </ol>
  </ScrollRegion>
}

function DeviceObservations({ devices, timezone, scope, observations }: {
  devices: ObservedDevice[]
  timezone: string
  scope: ObservationScope
  observations: Observation[]
}) {
  const { openIP } = useIPDetails()
  return <ScrollRegion label="设备指纹与关联 IP，可上下滚动" size="md" fixed className="um-records-scroll">
    <div className="um-device-grid">
      {devices.map(device => <details key={device.browser_id} className="um-device-record">
        <summary><Monitor className="h-4 w-4 shrink-0 text-primary" aria-hidden="true" /><span className="min-w-0 flex-1"><strong>{[device.device?.browser, device.device?.platform].filter(Boolean).join(' · ') || '未观测浏览器与系统'}</strong><code title={device.browser_id}>{device.browser_id}</code><small>最近出现 {time(device.last_seen_at, timezone)} · {count(device.ips?.length)} 个 IP</small></span><ChevronDown className="um-record-chevron h-4 w-4 shrink-0" aria-hidden="true" /></summary>
        <div className="um-device-expanded">
          <p className="break-all text-xs"><span className="text-muted-foreground">浏览器标识：</span><code>{device.browser_id}</code></p>
          <dl className="um-observation-device-fields">
            {deviceFields.map(([key, label]) => <div key={key}><dt>{label}</dt><dd>{device.device?.[key] || '未观测'}</dd></div>)}
            <div><dt>本窗口首次出现</dt><dd>{time(device.first_seen_at, timezone)}</dd></div><div><dt>最近出现</dt><dd>{time(device.last_seen_at, timezone)}</dd></div>
          </dl>
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground"><span>登录 {count(device.login_count)}</span><span>注册 {count(device.registration_count)}</span><span>成功 {count(device.successes)}</span><span>失败 {count(device.failures)}</span></div>
          <div className="border-t pt-3"><p className="mb-2 text-xs text-muted-foreground">关联登录 / 注册 IP</p>
            {device.ips?.length ? <div className="um-device-ips" tabIndex={0} role="region" aria-label="该设备关联登录及注册 IP">{device.ips.map(ip => {
              const event = observations.find(item => item.ip === ip && item.browser_id === device.browser_id && item.identity === 'verified' && item.source !== 'api_call')
              const source = event?.source ?? (device.login_count > 0 && device.registration_count === 0 ? 'login' : device.registration_count > 0 && device.login_count === 0 ? 'registration' : undefined)
              return source ? <button key={ip} type="button" className="ip-inline-button" aria-label={`查看设备关联 IP ${ip}`} onClick={() => openIP({ ip, source, period: '30d', userId: scope.userId, start: scope.start, end: scope.end })}><code>{ip}</code></button> : <code key={ip}>{ip}</code>
            })}</div> : <p className="text-xs text-muted-foreground">未记录 IP</p>}
          </div>
          {device.user_agent && <details className="um-network-details text-xs"><summary>User-Agent<ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /></summary><p className="mt-2 break-all leading-relaxed">{device.user_agent}</p></details>}
        </div>
      </details>)}
    </div>
  </ScrollRegion>
}

function CallObservations({ observations, timezone, scope }: { observations: Observation[]; timezone: string; scope: ObservationScope }) {
  const { openToken } = useIPDetails()
  return <>
    <div className="md:hidden"><ScrollRegion label="最近调用出口，可上下滚动" size="md" fixed className="um-records-scroll">
      <div className="um-mobile-call-list">{observations.map(observation => <details key={observation.id} className="um-mobile-call-record">
        <summary><span className="min-w-0 flex-1"><ObservationIP observation={observation} scope={scope} className="block break-all text-sm font-medium" /><small>{time(observation.occurred_at, timezone)}</small></span><ObservationResult result={observation.result} /><ChevronDown className="um-record-chevron h-4 w-4 shrink-0" aria-hidden="true" /></summary>
        <div className="um-auth-expanded"><IPIdentity observation={observation} scope={scope} /><IPProperties info={observation.ip_info} /><p className="border-t pt-2 text-xs text-muted-foreground">令牌 ID：{observation.token_id ? <button type="button" className="ip-inline-button" onClick={() => openToken(observation.token_id!, undefined, '30d', { start: scope.start, end: scope.end })}>#{observation.token_id}</button> : '未记录'}</p></div>
      </details>)}</div>
    </ScrollRegion></div>
    <div className="hidden min-w-0 md:block">
      <Table className="um-observation-table" containerClassName="scroll-region scroll-region--md scroll-region--fixed um-observation-table-scroll" containerLabel="最近调用轨迹，可上下和左右滚动" aria-label="最近调用出口">
        <TableHeader><TableRow><TableHead className="whitespace-nowrap">调用时间</TableHead><TableHead>结果</TableHead><TableHead>调用出口 IP</TableHead><TableHead>令牌 / 网络详情</TableHead></TableRow></TableHeader>
        <TableBody>{observations.map(observation => <TableRow key={observation.id}>
          <TableCell className="whitespace-nowrap text-xs tabular-nums">{time(observation.occurred_at, timezone)}</TableCell>
          <TableCell><ObservationResult result={observation.result} /></TableCell>
          <TableCell className="min-w-44"><ObservationIP observation={observation} scope={scope} className="break-all text-xs font-medium" /><p className="mt-1 text-xs text-muted-foreground">{location(observation.ip_info)}</p></TableCell>
          <TableCell className="min-w-48"><p className="mb-1.5 text-xs text-muted-foreground">{observation.token_id ? <button type="button" className="ip-inline-button" onClick={() => openToken(observation.token_id!, undefined, '30d', { start: scope.start, end: scope.end })}>令牌 #{observation.token_id}</button> : '令牌未记录'}</p><details className="um-network-details"><summary>网络画像<ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /></summary><div className="space-y-3 pt-3"><IPIdentity observation={observation} scope={scope} /><IPProperties info={observation.ip_info} /></div></details></TableCell>
        </TableRow>)}</TableBody>
      </Table>
    </div>
  </>
}

function validateData(data: ObservationsData, userId: number) {
  if (!data || data.user_id !== userId || !Number.isFinite(data.as_of) ||
    !Number.isFinite(data.window_start) || !Number.isFinite(data.window_end) || data.window_end <= data.window_start ||
    !data.totals || !Number.isInteger(data.limit) || data.limit < 1 ||
    ![data.logins, data.calls, data.registrations, data.unverified_attempts, data.devices].every(Array.isArray)) {
    throw new ApiError('登录观测数据与当前用户不一致，请重新读取。', 503)
  }
  new Intl.DateTimeFormat('zh-CN', { timeZone: data.timezone }).format()
}

export function UserLoginObservations({ userId, username, tokenVersion = 0, onUnauthorized }: Props) {
  const [revision, setRevision] = useState(0)
  const [recordsTab, setRecordsTab] = useState('logins')
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const requestSerial = useRef(0)
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const scope = JSON.stringify([userId, username, tokenVersion, revision])

  useEffect(() => { setRecordsTab('logins') }, [userId, tokenVersion])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) { setSnapshot(null); return }
    if (!Number.isSafeInteger(userId) || userId <= 0) {
      setSnapshot({ scope, sessionToken, error: '用户 ID 无效，无法读取登录观测。' })
      return
    }
    const controller = new AbortController()
    const serial = ++requestSerial.current
    setSnapshot(null)
    const isCurrent = () => !controller.signal.aborted && requestSerial.current === serial &&
      sessionStorage.getItem('siteadmin-token') === sessionToken
    void requestJSON<ObservationsData>(`/api/users/${userId}/observations`, {}, controller.signal).then(data => {
      if (!isCurrent()) return
      validateData(data, userId)
      setSnapshot({ scope, sessionToken, data })
    }).catch(cause => {
      if (!isCurrent()) return
      const unauthorized = cause instanceof ApiError && cause.status === 401
      setSnapshot({
        scope, sessionToken, unauthorized,
        error: unauthorized ? '管理员令牌已失效，请重新验证。'
          : cause instanceof Error ? cause.message : '暂时无法读取登录与调用观测。',
      })
      if (unauthorized) onUnauthorized()
    })
    return () => controller.abort()
  }, [scope, sessionToken, userId, onUnauthorized])

  const current = snapshot?.scope === scope && snapshot.sessionToken === sessionToken ? snapshot : null
  const loading = !UI_PREVIEW_ONLY && !current
  const data = !UI_PREVIEW_ONLY ? current?.data : undefined
  const timezone = data?.timezone || 'Asia/Shanghai'
  const limit = Math.min(data?.limit ?? 20, 20)
  const logins = data?.logins.filter(item => item.identity === 'verified' && item.source === 'login').slice(0, limit) ?? []
  const calls = data?.calls.filter(item => item.identity === 'verified' && item.source === 'api_call').slice(0, limit) ?? []
  const registrations = data?.registrations.filter(item => item.identity === 'verified' && item.source === 'registration').slice(0, limit) ?? []
  const attempts = data?.unverified_attempts.filter(item => item.identity === 'unverified_account_attempt' && item.result === 'failure' && item.source !== 'api_call').slice(0, limit) ?? []
  const devices = data?.devices.slice(0, limit) ?? []
  const latestLogin = data?.latest_login?.identity === 'verified' && data.latest_login.source === 'login' && data.latest_login.result === 'success' ? data.latest_login : null
  const latestCall = data?.latest_call?.identity === 'verified' && data.latest_call.source === 'api_call' ? data.latest_call : null
  const latestRegistration = data?.latest_registration?.identity === 'verified' && data.latest_registration.source === 'registration' && data.latest_registration.result === 'success' ? data.latest_registration : null
  const observationScope = { userId, start: data?.window_start ?? 0, end: data?.window_end ?? 0 }

  return <section className="um-observations um-detail um-detail--login min-w-0 space-y-4" aria-label={`${username || `用户 #${userId}`} 的登录与调用观测`}>
    <div className="um-detail-section um-detail-section--login">
      <header>
        <h4>登录与设备观测</h4>
        <p>登录、注册、调用出口和设备指纹分别记录</p>
      </header>
      <div className="um-detail-section-toolbar">
        <span className="um-detail-section-user">{username || `用户 #${userId}`}</span>
        <Button size="sm" variant="outline" type="button" disabled={UI_PREVIEW_ONLY || loading} onClick={() => setRevision(value => value + 1)}>
          <RefreshCw className={`mr-2 h-4 w-4${loading ? ' animate-spin' : ''}`} />刷新观测
        </Button>
      </div>
    </div>
    {UI_PREVIEW_ONLY ? <EmptyObservation>数据获取已暂停，尚未读取登录与调用观测。</EmptyObservation>
      : loading ? <div className="flex min-h-36 items-center justify-center gap-2 rounded-lg border text-sm text-muted-foreground" role="status"><Loader2 className="h-5 w-5 animate-spin" />正在读取当前用户的观测记录…</div>
        : current?.error ? <div className="space-y-3 rounded-lg border border-destructive/30 p-4" role="alert"><p className="break-words text-sm text-destructive">{current.error}</p>
          {!current.unauthorized && <Button type="button" variant="outline" size="sm" onClick={() => setRevision(value => value + 1)}><RefreshCw className="mr-2 h-4 w-4" />重新读取</Button>}
        </div>
          : data && <>
            <div className="rounded-lg border bg-muted/20 px-3 py-2.5 text-xs leading-relaxed text-muted-foreground">
              <p>观测范围：{time(data.window_start, timezone)} — {time(data.window_end, timezone)} · {timezone}</p>
              <p className="mt-1">更新于 {time(data.as_of, timezone)}；各类列表展示最近 {limit} 条，数量为整个观测窗口的累计。</p>
            </div>
            <div className="grid min-w-0 gap-3 xl:grid-cols-3">
              <LatestObservationCard title="最近成功登录 IP" description="近 30 天内探针观测到的最近一次成功登录" observation={latestLogin} empty="探针在当前窗口尚未观测到成功登录。" timezone={timezone} Icon={LogIn} scope={observationScope} />
              <LatestObservationCard title="最近调用 IP" description="近 30 天内最近一次调用，包含成功与失败；表示应用出口" observation={latestCall} empty="当前窗口尚无已导入的调用记录。" timezone={timezone} Icon={Globe} scope={observationScope} />
              <LatestObservationCard title="注册 IP" description="近 30 天内探针观测到的最近一次成功注册" observation={latestRegistration} empty="探针在当前窗口尚未观测到成功注册，历史注册 IP 可能未采集。" timezone={timezone} Icon={UserPlus} scope={observationScope} />
            </div>
            <details className="um-observation-scope rounded-lg border bg-muted/20 text-xs leading-relaxed text-muted-foreground">
              <summary><Info className="h-4 w-4 shrink-0" /><span>来源与统计口径</span><ChevronDown className="um-record-chevron ml-auto h-4 w-4" aria-hidden="true" /></summary><div className="space-y-1.5 px-3 pb-3">
                <p>调用 IP 可能属于服务器、应用或代理出口，不能据此推断用户登录位置。内网、未知归属地及代理属性单独显示，均不直接代表恶意。</p>
                <p>调用历史：{(data.coverage?.api_call ?? data.history_complete) ? '已声明导入完整' : '覆盖完整性未确认'}。登录与注册仅代表探针已观测范围；无记录不代表从未发生。</p>
              </div>
            </details>
            <Card className="um-observation-records min-w-0 shadow-none">
              <CardHeader className="space-y-1.5 p-4 pb-3">
                <CardTitle className="flex items-center gap-2 text-sm font-semibold"><Activity className="h-4 w-4 text-primary" />最近轨迹与设备</CardTitle>
                <CardDescription className="text-xs leading-relaxed">切换记录类型查看最近 {limit} 条，展开一行查看详细画像；列表可在固定窗口内滚动。</CardDescription>
              </CardHeader>
              <CardContent className="min-w-0 px-4 pb-4 pt-0">
                <Tabs value={recordsTab} onValueChange={setRecordsTab} className="min-w-0">
                  <div className="um-observation-tab-strip"><TabsList className="w-max gap-1" aria-label="登录与调用观测记录类型">
                    <TabsTrigger value="logins" className="gap-1.5"><LogIn className="h-3.5 w-3.5" />登录记录<span className="um-record-total">{count(data.totals.login)}</span></TabsTrigger>
                    <TabsTrigger value="calls" className="gap-1.5"><Activity className="h-3.5 w-3.5" />调用轨迹<span className="um-record-total">{count(data.totals.call)}</span></TabsTrigger>
                    <TabsTrigger value="devices" className="gap-1.5"><Fingerprint className="h-3.5 w-3.5" />设备指纹<span className="um-record-total">{count(data.totals.devices)}</span></TabsTrigger>
                    <TabsTrigger value="registrations" className="gap-1.5"><UserPlus className="h-3.5 w-3.5" />注册观测<span className="um-record-total">{count(data.totals.registration)}</span></TabsTrigger>
                    <TabsTrigger value="attempts" className="gap-1.5"><UserX className="h-3.5 w-3.5" />未认证尝试<span className="um-record-total">{count(data.totals.unverified_attempts)}</span></TabsTrigger>
                  </TabsList></div>
                  <TabsContent value="logins" className="mt-3 space-y-3"><p className="text-xs leading-relaxed text-muted-foreground">已验证关联当前账号的登录记录。成功、失败和待确认分别展示。</p>{logins.length ? <AuthenticationTimeline observations={logins} timezone={timezone} scope={observationScope} /> : <EmptyObservation>探针尚未观测到已验证关联该账号的登录记录。</EmptyObservation>}</TabsContent>
                  <TabsContent value="calls" className="mt-3 space-y-3"><p className="text-xs leading-relaxed text-muted-foreground">调用出口来自使用日志，独立于登录设备，不并入浏览器指纹。</p>{calls.length ? <CallObservations observations={calls} timezone={timezone} scope={observationScope} /> : <EmptyObservation>当前窗口尚无该账号已导入的调用日志。</EmptyObservation>}</TabsContent>
                  <TabsContent value="devices" className="mt-3 space-y-3"><p className="text-xs leading-relaxed text-muted-foreground">BrowserID 是浏览器标识，并非硬件身份；同一设备可能产生多个标识，多个使用者也可能共享浏览器。仅展示探针实际记录的字段。</p>{devices.length ? <DeviceObservations devices={devices} timezone={timezone} scope={observationScope} observations={[...logins, ...registrations]} /> : <EmptyObservation>探针尚未观测到该账号的浏览器标识或设备字段。</EmptyObservation>}</TabsContent>
                  <TabsContent value="registrations" className="mt-3 space-y-3"><p className="text-xs leading-relaxed text-muted-foreground">已关联当前账号的注册事件，未采集的历史注册 IP 不会补造。</p>{registrations.length ? <AuthenticationTimeline observations={registrations} timezone={timezone} scope={observationScope} /> : <EmptyObservation>探针尚未观测到该账号的注册事件。</EmptyObservation>}</TabsContent>
                  <TabsContent value="attempts" className="mt-3 space-y-3"><p className="text-xs leading-relaxed text-muted-foreground">输入了该用户名但未完成身份认证的失败尝试，不代表账号本人发起，不计入已验证登录、设备指纹或调用活跃。</p>{attempts.length ? <AuthenticationTimeline observations={attempts} timezone={timezone} scope={observationScope} unverified /> : <EmptyObservation>当前窗口未观测到匹配该用户名的未认证失败尝试。</EmptyObservation>}</TabsContent>
                </Tabs>
              </CardContent>
            </Card>
          </>}
  </section>
}

export default UserLoginObservations
