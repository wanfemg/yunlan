import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import ReactECharts from 'echarts-for-react/lib/core'
import * as echarts from 'echarts/core'
import { LineChart, MapChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent, VisualMapComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import {
  Activity, AlertTriangle, Check, CheckCircle2, ChevronDown, ChevronLeft,
  ChevronRight, Database, Download, Filter, Globe, KeyRound, Loader2, LogIn, Map as MapIcon,
  MapPin, Network, RefreshCw, Search, ShieldCheck, Timer, TrendingUp, UserPlus, Users, X,
} from 'lucide-react'
import { ApiError, downloadCSV, requestJSON, UI_PREVIEW_ONLY } from './api'
import IPAttributeEditor from './IPAttributeEditor'
import IPMaintenancePanel from './IPMaintenancePanel'
import { Notice } from './ModuleUI'
import { useIPDetails } from './IPDetailsContext'
import { UserIdentityButton } from './components/UserIdentityButton'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Button } from './components/ui/button'
import { Input } from './components/ui/input'
import { Badge } from './components/ui/badge'
import { Select } from './components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { cn } from './lib/utils'
import './ip-analysis.css'

echarts.use([
  LineChart, MapChart, PieChart, GridComponent, LegendComponent,
  TooltipComponent, VisualMapComponent, CanvasRenderer,
])

type EventSource = 'api_call' | 'login' | 'registration'
type RelationView = 'addresses' | 'shared' | 'users' | 'tokens' | 'user_ips'
type GeoLevel = 'country' | 'region' | 'city'
type MapView = 'world' | 'china'
type Bucket = {
  key: string; label: string; events: number; success: number; failure: number
  users: number; tokens: number; ips: number; user_ids?: number[]; token_ids?: number[]
  first_seen_at?: number; last_seen_at?: number
}
type IntelligenceMatch = { kind: 'vpn' | 'non_residential' | 'tor'; prefix: string; source: string }
type IntelligenceStatus = {
  configured: boolean; available: boolean; loaded_at: number; version: string
  files: {
    name: string; kind: string; source: string; status: 'loaded' | 'missing' | 'invalid'
    prefix_count: number; last_modified: number; version: string; error?: string
  }[]
  warnings: string[]
  geo?: { city_available: boolean; asn_available: boolean; isp_available: boolean; anonymous_available: boolean }
}
type IPInfo = {
  ip: string; address_class: string; country_code?: string; country?: string
  region?: string; city?: string; asn?: number; as_org?: string; isp?: string
  network_type: string; proxy: Record<string, string>; data_source: string
  updated_at: number; geo_version?: string; asn_version?: string; isp_version?: string
  proxy_version?: string; field_sources?: Record<string, string>
  intelligence?: { matches: IntelligenceMatch[]; available: boolean; version: string }
}
type NetworkMetadata = {
  [key: string]: unknown
  history_complete?: boolean
  geo_available?: boolean
  asn_available?: boolean
  isp_available?: boolean
  proxy_database_available?: boolean
  last_call_sync?: string
  call_cursor?: string
  event_sources?: Partial<Record<EventSource, { status: string; observed_events: number; coverage_status?: string }>>
  ip_database_versions?: Record<string, string>
  intelligence?: IntelligenceStatus
  sync_job?: SyncJob
}
type SyncJob = { id: string; state: string; progress: number | null; imported: number; cursor: number
  watermark: number; started_at: number; finished_at: number; error?: string; checkpoint_error?: string; worker_ready: boolean }
type NetworkAnalysis = {
  event_type: EventSource; window_start: number; window_end: number; as_of: number
  timezone: string; sample_mode: string; completeness: string; summary: Bucket
  coverage: Record<string, number | string | null>; metadata: NetworkMetadata
  address_classes: Bucket[]; geography: Bucket[]; regions: Bucket[]; cities: Bucket[]
  asn: Bucket[]; network_types: Bucket[]; proxy_attributes: Bucket[]; trend: Bucket[]
  warnings: string[]
}
type RelationPage = {
  event_type: EventSource; items: Bucket[]; total: number; page: number; page_size: number
  window_start: number; window_end: number; as_of: number; metadata: NetworkMetadata
  user_labels?: Record<string, { id: number; username: string; display_name: string; status: number; known: boolean }>
  token_labels?: Record<string, { id: number; name: string; user_id: number; status: number; known: boolean }>
  warnings?: string[]
}
type Filters = { userID: string; tokenID: string; ip: string }
type GeoAsset = {
  type: string
  features: { properties: { name: string }; [key: string]: unknown }[]
}

const sources: { id: EventSource; label: string; short: string; icon: typeof Globe }[] = [
  { id: 'api_call', label: '调用 IP', short: '调用', icon: Globe },
  { id: 'login', label: '登录 IP', short: '登录', icon: LogIn },
  { id: 'registration', label: '注册 IP', short: '注册', icon: UserPlus },
]
const periods = [
  ['1h', '最近 1 小时'], ['3h', '最近 3 小时'], ['6h', '最近 6 小时'],
  ['12h', '最近 12 小时'], ['24h', '最近 24 小时'], ['3d', '最近 3 天'],
  ['7d', '最近 7 天'], ['30d', '最近 30 天'], ['90d', '最近 90 天'],
]
const defaultFilters: Filters = { userID: '', tokenID: '', ip: '' }
const pageSize = 15
const numberFormat = new Intl.NumberFormat('zh-CN')
const classLabels: Record<string, string> = {
  public: '公网', private: '内网', loopback: '回环地址', link_local: '链路本地',
  reserved: '保留地址', missing: '未记录 IP', invalid: '无效 IP', unknown: '未知',
}
const networkLabels: Record<string, string> = {
  unknown: '网络类型未知', hosting: '托管网络', residential: '住宅网络',
  mobile: '移动网络', business: '企业网络', corporate: '企业网络',
  non_residential: '非住宅出口（机房 / 代理）',
}
const proxyLabels: Record<string, string> = {
  vpn: 'VPN', public_proxy: '公共代理', residential_proxy: '住宅代理', tor: 'Tor',
}
const intelligenceKindLabels: Record<string, string> = {
  vpn: 'VPN 名单', non_residential: '非住宅出口名单', tor: 'Tor 出口名单',
}
const intelligenceFileLabels: Record<string, string> = {
  'vpn-ipv4.txt': 'VPN · IPv4', 'vpn-ipv6.txt': 'VPN · IPv6',
  'non-residential-ipv4.txt': '非住宅出口 · IPv4', 'non-residential-ipv6.txt': '非住宅出口 · IPv6',
  'tor-exits.txt': 'Tor 出口',
}
const intelligenceSourceLabels: Record<string, string> = {
  x4bnet_vpn: 'X4BNet VPN 名单', x4bnet_non_residential: 'X4BNet 非住宅出口名单',
  tor_project_exit_list: 'Tor Project 官方出口名单',
}
const chinaNames: Record<string, string> = {
  Beijing: '北京', Tianjin: '天津', Hebei: '河北', Shanxi: '山西', 'Inner Mongolia': '内蒙古',
  'Nei Mongol': '内蒙古', Liaoning: '辽宁', Jilin: '吉林', Heilongjiang: '黑龙江',
  Shanghai: '上海', Jiangsu: '江苏', Zhejiang: '浙江', Anhui: '安徽', Fujian: '福建',
  Jiangxi: '江西', Shandong: '山东', Henan: '河南', Hubei: '湖北', Hunan: '湖南',
  Guangdong: '广东', Guangxi: '广西', 'Guangxi Zhuang': '广西', Hainan: '海南',
  Chongqing: '重庆', Sichuan: '四川', Guizhou: '贵州', Yunnan: '云南', Tibet: '西藏',
  Xizang: '西藏', Shaanxi: '陕西', Gansu: '甘肃', Qinghai: '青海', Ningxia: '宁夏',
  'Ningxia Hui': '宁夏', Xinjiang: '新疆', 'Xinjiang Uyghur': '新疆', Taiwan: '台湾',
  'Hong Kong': '香港', Macau: '澳门', Macao: '澳门',
}
const mapAliases: Record<string, string[]> = {
  US: ['United States of America', 'United States'], RU: ['Russia', 'Russian Federation'],
  KR: ['South Korea', 'Korea'], KP: ['North Korea'], CD: ['Dem. Rep. Congo', 'Democratic Republic of the Congo'],
  CG: ['Congo', 'Republic of the Congo'], CI: ['Ivory Coast', "Côte d'Ivoire"],
  CZ: ['Czech Republic', 'Czechia'], LA: ['Laos'], SY: ['Syria'], IR: ['Iran'],
  TZ: ['Tanzania', 'United Republic of Tanzania'], VN: ['Vietnam', 'Viet Nam'],
}

function count(value?: number | null) { return value == null ? '—' : numberFormat.format(value) }
function rate(value: number | string | null | undefined) {
  return typeof value === 'number' && Number.isFinite(value) ? `${(value * 100).toFixed(1)}%` : '—'
}
function databaseState(available?: boolean) {
  if (UI_PREVIEW_ONLY || available == null) return '未请求'
  return available ? '已配置' : '未配置'
}
function time(value?: number | string, timezone?: string) {
  const stamp = Number(value)
  if (!stamp || !Number.isFinite(stamp)) return '尚未记录'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    ...(timezone ? { timeZone: timezone } : {}),
  }).format(new Date(stamp * 1000))
}
function countryName(code: string, language = 'zh-CN') {
  if (!/^[A-Z]{2}$/.test(code)) return classLabels[code] ?? code
  try { return new Intl.DisplayNames([language], { type: 'region' }).of(code) ?? code } catch { return code }
}
function geoParts(bucket: Bucket): string[] {
  try {
    const parsed: unknown = JSON.parse(bucket.key)
    if (Array.isArray(parsed) && parsed.every(value => typeof value === 'string')) return parsed
  } catch { /* Country buckets use plain country codes. */ }
  return [bucket.key]
}
function geoLabel(bucket: Bucket, level: GeoLevel) {
  if (level === 'country') return countryName(bucket.key)
  const parts = geoParts(bucket)
  return parts.map((part, index) => index === 0 ? countryName(part) : chinaNames[part] ?? classLabels[part] ?? part).join(' / ')
}
function sourceName(source: EventSource) { return sources.find(item => item.id === source)?.short ?? '事件' }
function initialNetworkQuery() {
  const params = new URLSearchParams(window.location.search)
  const rawSource = params.get('source')
  const source: EventSource = rawSource === 'login' || rawSource === 'registration' ? rawSource : 'api_call'
  const positiveID = (key: string) => {
    const value = params.get(key) ?? ''
    return /^[1-9]\d*$/.test(value) ? value : ''
  }
  const start = Number(params.get('start')), end = Number(params.get('end'))
  const fixedWindow = Number.isSafeInteger(start) && start >= 0 && Number.isSafeInteger(end) &&
    end > start && end <= Math.floor(Date.now() / 1000) + 1 && end - start <= 366 * 86400
    ? { window_start: start, window_end: end } : undefined
  const rawView = params.get('view')
  const relation: RelationView = rawView === 'shared' || rawView === 'users' ||
    rawView === 'tokens' && source === 'api_call' || rawView === 'user_ips' && positiveID('user_id')
    ? rawView as RelationView : 'addresses'
  return {
    source,
    fixedWindow,
    relation,
    period: periods.some(([id]) => id === params.get('period')) ? params.get('period')! : '7d',
    filters: {
      userID: positiveID('user_id'),
      tokenID: source === 'api_call' ? positiveID('token_id') : '',
      ip: (params.get('ip') ?? '').slice(0, 255),
    },
  }
}
function sourceParams(source: EventSource, period: string, filters: Filters, snapshot?: Pick<NetworkAnalysis, 'window_start' | 'window_end'>) {
  const params = new URLSearchParams({ source, period })
  if (filters.userID) params.set('user_id', filters.userID)
  if (source === 'api_call' && filters.tokenID) params.set('token_id', filters.tokenID)
  if (filters.ip) params.set('ip', filters.ip)
  if (snapshot) {
    params.set('start', String(snapshot.window_start))
    params.set('end', String(snapshot.window_end))
  }
  return params
}
function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : '网络数据暂时不可用，请重试。'
}
function Chart({ option, className = '', label }: { option: Record<string, unknown>; className?: string; label: string }) {
  const chart = useRef<ReactECharts>(null)
  const wrapper = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = wrapper.current
    if (!element || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => chart.current?.getEchartsInstance().resize())
    observer.observe(element)
    return () => observer.disconnect()
  }, [])
  return <div ref={wrapper} className={`ip-chart ${className}`} role="img" aria-label={label}>
    <ReactECharts ref={chart} echarts={echarts} option={option} notMerge lazyUpdate style={{ height: '100%', width: '100%' }} />
  </div>
}

function NetworkMap({ data, view, source }: { data: NetworkAnalysis | null; view: MapView; source: EventSource }) {
  const [asset, setAsset] = useState<GeoAsset | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    setAsset(null)
    setError('')
    fetch(`/network-${view}.json`, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error('地图资源暂时不可用')
      const next = await response.json() as GeoAsset
      if (next.type !== 'FeatureCollection' || !Array.isArray(next.features)) throw new Error('地图资源格式不可用')
      echarts.registerMap(`siteadmin-${view}`, next as unknown as Parameters<typeof echarts.registerMap>[1])
      if (!controller.signal.aborted) setAsset(next)
    }).catch(cause => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    })
    return () => controller.abort()
  }, [view])
  const option = useMemo(() => {
    if (!asset) return {}
    const validNames = new Set(asset.features.map(feature => feature.properties.name))
    const records: { name: string; value: number }[] = []
    if (view === 'world') {
      for (const row of data?.geography ?? []) {
        if (!/^[A-Z]{2}$/.test(row.key)) continue
        const candidates = [...(mapAliases[row.key] ?? []), countryName(row.key, 'en-US'), row.label]
        const name = candidates.find(candidate => validNames.has(candidate))
        if (name) records.push({ name, value: row.events })
      }
    } else {
      for (const row of data?.regions ?? []) {
        const [country, region] = geoParts(row)
        if (country !== 'CN' || !region || region === 'unknown') continue
        const name = chinaNames[region] ?? region.replace(/壮族自治区|回族自治区|维吾尔自治区|特别行政区|自治区|省|市$/g, '')
        if (validNames.has(name)) records.push({ name, value: row.events })
      }
      for (const [code, name] of [['HK', '香港'], ['MO', '澳门'], ['TW', '台湾']]) {
        const row = (data?.geography ?? []).find(item => item.key === code)
        if (row && validNames.has(name)) records.push({ name, value: row.events })
      }
    }
    return {
      animationDuration: 200,
      tooltip: {
        trigger: 'item', renderMode: 'richText',
        formatter: (params: { name: string; value: number }) =>
          `${params.name}\n${Number.isFinite(params.value) ? `${count(params.value)} 条${sourceName(source)}事件` : UI_PREVIEW_ONLY ? '数据获取已暂停' : '暂无已解析记录'}`,
      },
      visualMap: records.length ? {
        min: 0, max: Math.max(1, ...records.map(row => row.value)), left: 4, bottom: 8,
        itemWidth: 9, itemHeight: 75, text: ['多', '少'], textStyle: { color: '#78838e', fontSize: 10 },
        inRange: { color: ['#e7f0f8', '#a4c8e7', '#377fb4', '#1b527d'] }, calculable: false,
      } : undefined,
      series: [{
        name: `${sourceName(source)}出口`, type: 'map', map: `siteadmin-${view}`,
        roam: true, scaleLimit: { min: 1, max: 7 }, zoom: view === 'world' ? 1.05 : 1.1,
        left: view === 'world' ? '7%' : '14%', right: '2%', top: 12, bottom: 12,
        data: records, selectedMode: false,
        itemStyle: { areaColor: '#e9edf3', borderColor: '#ffffff', borderWidth: 0.7 },
        emphasis: { itemStyle: { areaColor: '#bfdbfe' }, label: { show: false } },
        label: { show: false },
      }],
    }
  }, [asset, data, view, source])
  if (error) return <div className="ip-map-state text-muted-foreground"><AlertTriangle className="h-10 w-10 text-amber-500" /><span>{error}</span><span className="text-xs">地域排行与其他统计仍可查看。</span></div>
  if (!asset) return <div className="ip-map-state text-muted-foreground" role="status"><Loader2 className="h-8 w-8 animate-spin" />加载地图中...</div>
  const hasRecords = !!(option as { series?: { data?: unknown[] }[] }).series?.[0]?.data?.length
  return <div className="relative">
    <Chart option={option} label={`${sourceName(source)}出口${view === 'world' ? '世界' : '中国'}地域分布地图`} className="ip-map-chart" />
    {!hasRecords && <span className="pointer-events-none absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full border bg-background/90 px-3 py-1 text-xs text-muted-foreground shadow-sm">{UI_PREVIEW_ONLY ? '静态底图 · 数据获取已暂停' : '静态底图 · 暂无已解析记录'}</span>}
  </div>
}

function IPStatCard({ title, value, icon: Icon, color, detail }: {
  title: string; value: string; icon: typeof Globe; color: string; detail?: string
}) {
  const themes: Record<string, string> = {
    blue: 'bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-300',
    emerald: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300',
    orange: 'bg-orange-50 text-orange-700 dark:bg-orange-950 dark:text-orange-300',
    cyan: 'bg-cyan-50 text-cyan-700 dark:bg-cyan-950 dark:text-cyan-300',
    slate: 'bg-slate-50 text-slate-700 dark:bg-slate-900 dark:text-slate-300',
  }
  return <Card className="overflow-hidden transition-all duration-200 hover:shadow-md">
    <CardContent className="p-5">
      <div className="flex items-start justify-between gap-2"><div className="min-w-0 space-y-2">
        <p className="text-sm font-medium text-muted-foreground">{title}</p>
        <div className="text-2xl font-bold text-foreground">{value}</div>
      </div><div className={cn('shrink-0 rounded-xl p-2.5', themes[color] ?? themes.blue)}><Icon className="h-5 w-5" /></div></div>
      {detail && <p className="mt-3 text-xs leading-5 text-muted-foreground">{detail}</p>}
    </CardContent>
  </Card>
}

function MenuChoice({ label, children, items, value, onChange, ariaLabel }: {
  label?: string; children?: ReactNode
  items: { value: string; label: string; icon?: typeof Globe }[]
  value: string; onChange: (value: string) => void; ariaLabel: string
}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const options = useRef<(HTMLButtonElement | null)[]>([])
  useEffect(() => {
    if (!open) return
    const close = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false) }
    document.addEventListener('pointerdown', close)
    options.current[Math.max(0, items.findIndex(item => item.value === value))]?.focus()
    return () => document.removeEventListener('pointerdown', close)
    // Countdown renders must not move focus back to the selected menu item.
  }, [open])
  return <div className="relative shrink-0" ref={root}>
    <Button ref={trigger} type="button" variant="outline" size="sm" className="h-9 gap-1.5"
      aria-label={ariaLabel} aria-haspopup="menu" aria-expanded={open}
      onClick={() => setOpen(current => !current)}
      onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); setOpen(true) } }}>
      {children}{label}<ChevronDown className={cn('h-3.5 w-3.5 transition-transform', open && 'rotate-180')} />
    </Button>
    {open && <div role="menu" aria-label={ariaLabel} className="absolute right-0 top-full z-50 mt-1 min-w-[150px] rounded-md border bg-popover p-1 text-popover-foreground shadow-lg"
      onKeyDown={event => {
        const current = options.current.indexOf(document.activeElement as HTMLButtonElement)
        const target = event.key === 'ArrowDown' ? (current + 1) % items.length
          : event.key === 'ArrowUp' ? (current - 1 + items.length) % items.length
            : event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : -1
        if (target >= 0) { event.preventDefault(); options.current[target]?.focus() }
        if (event.key === 'Escape') { event.preventDefault(); setOpen(false); trigger.current?.focus() }
        if (event.key === 'Tab') setOpen(false)
      }}>
      {items.map((item, index) => <button key={item.value} type="button" ref={element => { options.current[index] = element }}
        role="menuitemradio" aria-checked={value === item.value}
        className={cn('flex w-full items-center gap-2 rounded px-3 py-2 text-left text-sm outline-none transition-colors hover:bg-accent focus:bg-accent', value === item.value && 'bg-accent text-accent-foreground')}
        onClick={() => { onChange(item.value); setOpen(false); trigger.current?.focus() }}>
        {item.icon && <item.icon className="h-4 w-4" />}<span className="flex-1">{item.label}</span>{value === item.value && <Check className="h-3.5 w-3.5" />}
      </button>)}
    </div>}
  </div>
}

function EmptyRows({ columns, children }: { columns: number; children?: ReactNode }) {
  return <TableRow><TableCell colSpan={columns} className="h-36 text-center text-muted-foreground">{children ?? (UI_PREVIEW_ONLY ? '数据获取已暂停' : '暂无数据')}</TableCell></TableRow>
}

export default function IPAnalysis({ tokenVersion = 0, onUnauthorized }: {
  tokenVersion?: number; onUnauthorized: () => void
}) {
  const [initial] = useState(initialNetworkQuery)
  const { openIP, openUser, openToken } = useIPDetails()
  const [source, setSource] = useState<EventSource>(initial.source)
  const [period, setPeriod] = useState(initial.period)
  const [fixedWindow, setFixedWindow] = useState(initial.fixedWindow)
  const [draft, setDraft] = useState<Filters>(initial.filters)
  const [filters, setFilters] = useState<Filters>(initial.filters)
  const [refresh, setRefresh] = useState(0)
  const [dataState, setData] = useState<{ key: string; value: NetworkAnalysis } | null>(null)
  const [busy, setBusy] = useState(!UI_PREVIEW_ONLY)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [syncStarting, setSyncStarting] = useState(false)
  const [syncJob, setSyncJob] = useState<SyncJob | null>(null)
  const [exporting, setExporting] = useState(false)
  const syncActive = syncJob?.state === 'running' || syncJob?.state === 'queued'
  const syncing = syncStarting || syncActive
  const [geoLevel, setGeoLevel] = useState<GeoLevel>('country')
  const [geoPage, setGeoPage] = useState(1)
  const [asnPage, setASNPage] = useState(1)
  const [mapView, setMapView] = useState<MapView>('world')
  const [relation, setRelation] = useState<RelationView>(initial.relation)
  const [page, setPage] = useState(1)
  const [relationsState, setRelations] = useState<{ key: string; value: RelationPage } | null>(null)
  const [relationsBusy, setRelationsBusy] = useState(false)
  const [relationsError, setRelationsError] = useState('')
  const [lookupDraft, setLookupDraft] = useState('')
  const [lookup, setLookup] = useState<IPInfo | null>(null)
  const [lookupBusy, setLookupBusy] = useState(false)
  const [lookupError, setLookupError] = useState('')
  const [showFilters, setShowFilters] = useState(() => Object.values(initial.filters).some(Boolean))
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [countdown, setCountdown] = useState(0)
  const lookupRequest = useRef<AbortController | null>(null)
  const lookupInput = useRef<HTMLInputElement>(null)
  const activeSource = sources.find(item => item.id === source)!
  const session = sessionStorage.getItem('siteadmin-token')
  const analysisScope = JSON.stringify([source, period, filters, fixedWindow, refresh, tokenVersion, session])
  const data = dataState?.key === analysisScope ? dataState.value : null
  const relationScope = JSON.stringify([analysisScope, data?.window_start, data?.window_end, relation, page])
  const relations = relationsState?.key === relationScope ? relationsState.value : null
  useEffect(() => {
    const params = sourceParams(source, period, filters, fixedWindow)
    params.set('view', relation)
    window.history.replaceState(window.history.state, '', `${window.location.pathname}?${params}`)
  }, [source, period, filters, fixedWindow, relation])
  useEffect(() => {
    const update = () => setRefresh(value => value + 1)
    window.addEventListener('siteadmin:entities-changed', update)
    return () => window.removeEventListener('siteadmin:entities-changed', update)
  }, [])

  const handleError = useCallback((cause: unknown, setter: (value: string) => void) => {
    if (cause instanceof ApiError && cause.status === 401) {
      lookupRequest.current?.abort()
      setLookupBusy(false)
      setLookup(null)
      setData(null)
      setRelations(null)
      onUnauthorized()
    }
    else setter(errorMessage(cause))
  }, [onUnauthorized])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) {
      setBusy(false)
      setData(null)
      setRelations(null)
      setRelationsBusy(false)
      setError('')
      setRelationsError('')
      setMessage('')
      return
    }
    const controller = new AbortController()
    setBusy(true)
    setError('')
    setData(null)
    setRelations(null)
    setRelationsBusy(false)
    setRelationsError('')
    setPage(1)
    requestJSON<NetworkAnalysis>(`/api/ip/stats?${sourceParams(source, period, filters, fixedWindow)}`, {}, controller.signal)
      .then(next => {
        if (!controller.signal.aborted) {
          if (next.event_type !== source || !next.summary || !Number.isSafeInteger(next.window_start) ||
            !Number.isSafeInteger(next.window_end) || next.window_end <= next.window_start) throw new Error('IP 统计返回的来源或时间窗口不一致。')
          setData({ key: analysisScope, value: next })
          if (next.metadata.sync_job) setSyncJob(next.metadata.sync_job)
        }
      })
      .catch(cause => { if (!controller.signal.aborted) handleError(cause, setError) })
      .finally(() => { if (!controller.signal.aborted) setBusy(false) })
    return () => controller.abort()
  }, [source, period, filters, fixedWindow, refresh, tokenVersion, handleError, analysisScope])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    requestJSON<SyncJob>('/api/ip/sync/status', {}, controller.signal).then(next => {
      if (!controller.signal.aborted) setSyncJob(next)
    }).catch(cause => { if (!controller.signal.aborted) handleError(cause, setError) })
    return () => controller.abort()
  }, [tokenVersion, handleError])
  useEffect(() => {
    if (UI_PREVIEW_ONLY || !syncActive) return
    const controller = new AbortController()
    const timer = window.setInterval(() => {
      requestJSON<SyncJob>('/api/ip/sync/status', {}, controller.signal).then(next => {
        if (controller.signal.aborted) return
        setSyncJob(next)
        if (next.state !== 'queued' && next.state !== 'running') {
          setMessage(next.state === 'completed' ? `同步完成，本次新增 ${count(next.imported)} 条调用事件。` : next.error || '任务已停止，已提交游标保留。')
          setRefresh(value => value + 1)
        }
      }).catch(cause => { if (!controller.signal.aborted) handleError(cause, setError) })
    }, 2000)
    return () => { controller.abort(); window.clearInterval(timer) }
  }, [syncActive, handleError])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) {
      setRelationsBusy(false)
      setRelations(null)
      setRelationsError('')
      return
    }
    if (!data || data.event_type !== source) return
    const controller = new AbortController()
    setRelationsBusy(true)
    setRelationsError('')
    setRelations(null)
    const params = sourceParams(source, period, filters, data)
    params.set('page', String(page))
    params.set('page_size', String(pageSize))
    params.set('sync', 'false')
    const path = relation === 'addresses' ? '/api/ip/addresses' : relation === 'users' ? '/api/ip/multi-ip-users'
      : relation === 'tokens' ? '/api/ip/multi-ip-tokens'
        : relation === 'user_ips' ? `/api/ip/users/${filters.userID}/ips` : '/api/ip/shared'
    requestJSON<RelationPage>(`${path}?${params}`, {}, controller.signal)
      .then(next => {
        if (!controller.signal.aborted) {
          if (next.event_type !== source || next.window_start !== data.window_start || next.window_end !== data.window_end) {
            throw new Error('出口关联与汇总窗口不一致，请刷新。')
          }
          setRelations({ key: relationScope, value: next })
          const lastPage = Math.max(1, Math.ceil(next.total / pageSize))
          if (page > lastPage) setPage(lastPage)
        }
      })
      .catch(cause => { if (!controller.signal.aborted) handleError(cause, setRelationsError) })
      .finally(() => { if (!controller.signal.aborted) setRelationsBusy(false) })
    return () => controller.abort()
  }, [data, source, period, filters, relation, page, handleError, relationScope])

  useEffect(() => () => lookupRequest.current?.abort(), [])
  useEffect(() => {
    lookupRequest.current?.abort()
    setLookup(null)
    setLookupBusy(false)
    setLookupError('')
  }, [tokenVersion])
  useEffect(() => {
    setCountdown(refreshInterval)
    if (UI_PREVIEW_ONLY || refreshInterval === 0 || busy) return
    const deadline = Date.now() + refreshInterval * 1000
    const timer = window.setInterval(() => {
      const remaining = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setCountdown(remaining)
      if (remaining === 0 && document.visibilityState === 'visible') {
        window.clearInterval(timer)
        setRefresh(value => value + 1)
      }
    }, 1000)
    return () => window.clearInterval(timer)
  }, [refreshInterval, busy, source, period, filters, refresh])

  function selectSource(next: EventSource) {
    if (next === source) return
    setSource(next)
    setData(null)
    setRelations(null)
    setRelation('addresses')
    setPage(1)
    setMessage('')
    setDraft(value => ({ ...value, tokenID: '' }))
    setFilters(value => ({ ...value, tokenID: '' }))
  }

  function submitFilters(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY) return
    if (draft.userID && !/^[1-9]\d*$/.test(draft.userID) ||
        source === 'api_call' && draft.tokenID && !/^[1-9]\d*$/.test(draft.tokenID)) {
      setError('用户和令牌 ID 须为正整数。')
      return
    }
    const next = { userID: draft.userID.trim(), tokenID: source === 'api_call' ? draft.tokenID.trim() : '', ip: draft.ip.trim() }
    if (relation === 'user_ips' && !next.userID || next.tokenID || next.ip) setRelation('addresses')
    setFilters(next)
    setPage(1)
    setMessage('')
  }

  async function syncCalls() {
    if (UI_PREVIEW_ONLY) return
    if (syncing) return
    setSyncStarting(true)
    setMessage('')
    setError('')
    try {
      const result = await requestJSON<SyncJob>('/api/ip/sync', { method: 'POST' })
      setSyncJob(result)
      setMessage('调用日志已交给后台导入，切换页面后任务仍会继续。')
    } catch (cause) { handleError(cause, setError) } finally { setSyncStarting(false) }
  }
  async function cancelSync() {
    try { setSyncJob(await requestJSON<SyncJob>('/api/ip/sync/cancel', { method: 'POST' })) }
    catch (cause) { handleError(cause, setError) }
  }
  async function exportIPs() {
    if (exporting || !data) return
    setExporting(true)
    try { await downloadCSV(`/api/ip/export?${sourceParams(source, period, filters, data)}`, `IP分析-${activeSource.short}.csv`) }
    catch (cause) { handleError(cause, setError) } finally { setExporting(false) }
  }

  async function lookupIP(ip: string) {
    if (UI_PREVIEW_ONLY) { setLookupBusy(false); return }
    ip = ip.trim()
    if (!ip) { setLookupError('请输入 IPv4 或 IPv6 地址。'); lookupInput.current?.focus(); return }
    lookupRequest.current?.abort()
    const controller = new AbortController()
    lookupRequest.current = controller
    setLookup(null)
    setLookupBusy(true)
    setLookupError('')
    try {
      const result = await requestJSON<IPInfo>(`/api/ip/lookup/${encodeURIComponent(ip)}`, {}, controller.signal)
      if (!controller.signal.aborted) setLookup(result)
    } catch (cause) { if (!controller.signal.aborted) handleError(cause, setLookupError) }
    finally { if (!controller.signal.aborted) setLookupBusy(false) }
  }

  function inspectIP(ip: string) {
    openIP({ ip, source, period, userId: filters.userID ? Number(filters.userID) : undefined,
      tokenId: source === 'api_call' && filters.tokenID ? Number(filters.tokenID) : undefined,
      start: data?.window_start ?? fixedWindow?.window_start, end: data?.window_end ?? fixedWindow?.window_end })
  }

  function selectPeriod(next: string) { setPeriod(next); setFixedWindow(undefined); setPage(1) }
  function filterEntity(id: string, kind: 'userID' | 'tokenID') {
    const next = { ...filters, [kind]: id }
    setDraft(next); setFilters(next); setPage(1); setRelation('addresses'); setShowFilters(true)
  }
  useEffect(() => { setGeoPage(1); setASNPage(1) }, [source, period, filters, geoLevel])

  const trendOption = useMemo(() => {
    if (!data) return {}
    const rows = [...(data.trend ?? [])].sort((a, b) => Number(a.key) - Number(b.key))
    return {
      animationDuration: 250, color: ['#3879bd', '#1b9877', '#d38c43'],
      grid: { top: 38, bottom: 8, left: 3, right: 12, containLabel: true },
      legend: { top: 0, right: 0, itemWidth: 10, itemHeight: 7, textStyle: { color: '#73808e', fontSize: 11 } },
      tooltip: { trigger: 'axis', renderMode: 'richText' },
      xAxis: {
        type: 'category', boundaryGap: false, data: rows.map(row => time(row.key, data.timezone)),
        axisTick: { show: false }, axisLine: { lineStyle: { color: '#e4e9ee' } },
        axisLabel: { fontSize: 10, color: '#788592', hideOverlap: true },
      },
      yAxis: {
        type: 'value', minInterval: 1, splitNumber: 4, axisLabel: { fontSize: 10, color: '#788592' },
        splitLine: { lineStyle: { color: '#edf0f3', type: 'dashed' } },
      },
      series: [
        { name: '全部事件', type: 'line', data: rows.map(row => row.events), symbol: 'none', smooth: 0.2, lineStyle: { width: 2 }, areaStyle: { opacity: 0.06 } },
        { name: '成功', type: 'line', data: rows.map(row => row.success), symbol: 'none', smooth: 0.2, lineStyle: { width: 1.5 } },
        { name: '失败', type: 'line', data: rows.map(row => row.failure), symbol: 'none', smooth: 0.2, lineStyle: { width: 1.5 } },
      ],
    }
  }, [data])
  const geoRows = data ? (geoLevel === 'country' ? data.geography : geoLevel === 'region' ? data.regions : data.cities) ?? [] : []
  const geoPages = Math.max(1, Math.ceil(geoRows.length / 20))
  const currentGeoPage = Math.min(geoPage, geoPages)
  const asnPages = Math.max(1, Math.ceil((data?.asn?.length ?? 0) / 10))
  const currentASNPage = Math.min(asnPage, asnPages)
  const addressOption = useMemo(() => ({
    animationDuration: 250,
    color: ['#4388b9', '#7eb9a7', '#b7c3d1', '#dbaf78', '#ba9ca9', '#d0d6dc'],
    tooltip: { trigger: 'item', renderMode: 'richText' },
    legend: { bottom: 0, left: 'center', itemWidth: 8, itemHeight: 8, textStyle: { fontSize: 10, color: '#667888' } },
    series: [{
      name: '地址类型', type: 'pie', radius: ['42%', '65%'], center: ['50%', '43%'],
      avoidLabelOverlap: true, label: { show: false }, itemStyle: { borderWidth: 3, borderColor: '#fff' },
      data: (data?.address_classes ?? []).map(row => ({ name: classLabels[row.key] ?? row.label, value: row.events })),
    }],
  }), [data])
  const proxyRows = useMemo(() => Object.entries(proxyLabels).map(([key, label]) => {
    const entries = data?.proxy_attributes ?? []
    return {
      label,
      yes: entries.find(row => row.key === `${key}:yes`)?.events ?? 0,
      no: entries.find(row => row.key === `${key}:no`)?.events ?? 0,
      unknown: entries.find(row => row.key === `${key}:unknown`)?.events ?? 0,
    }
  }), [data])
  const total = data?.summary.events ?? 0
  const observed = data?.metadata.event_sources?.[source]
  const intelligence = data?.metadata.intelligence
  const pending = data ? Math.max(0, total - data.summary.success - data.summary.failure) : 0
  const relationChoices: { id: RelationView; label: string }[] = [
    { id: 'addresses', label: '全部出口' }, { id: 'shared', label: '共享出口（≥2 账号）' }, { id: 'users', label: '多出口账号（≥2 IP）' },
    ...(source === 'api_call' ? [{ id: 'tokens' as const, label: '多出口令牌（≥2 IP）' }] : []),
    ...(filters.userID ? [{ id: 'user_ips' as const, label: '用户出口明细' }] : []),
  ]
  const isIPRelation = relation === 'addresses' || relation === 'shared' || relation === 'user_ips'

  const currentPeriodLabel = periods.find(([value]) => value === period)?.[1].replace('最近 ', '') ?? period
  const unavailable = UI_PREVIEW_ONLY ? '数据获取已暂停' : busy ? '正在读取数据...' : '暂无数据'
  const geoTitles: Record<GeoLevel, string> = { country: '国家 / 地区', region: '省 / 州', city: '城市' }
  const relationColumns = source === 'api_call' ? 8 : 7
  const relationSection = <section id="ip-relations" className="ip-relations-section" aria-labelledby="ip-relations-title">
    <div className="ip-relations-heading">
      <div><h3 id="ip-relations-title"><Network className="h-5 w-5 text-primary" />出口与账号关联</h3>
        <p>{activeSource.label} · {data ? `${time(data.window_start, data.timezone)} 至 ${time(data.window_end, data.timezone)}` : '当前时间范围'}</p></div>
      <Select aria-label="网络关联类型" value={relation} onChange={event => { setRelation(event.target.value as RelationView); setPage(1) }}>
        {relationChoices.map(item => <option key={item.id} value={item.id}>{item.label}</option>)}
      </Select>
    </div>
    {relationsError && <Notice kind="error">{relationsError}</Notice>}
    {!!relations?.warnings?.length && <Notice kind="warning">{relations.warnings.join(' ')}</Notice>}
    <Table className="min-w-[880px]" containerClassName="scroll-region ip-relations-table" containerLabel="出口与账号关联列表">
      <TableHeader><TableRow>
        <TableHead>{isIPRelation ? '出口 IP / 关联账号' : relation === 'tokens' ? '令牌 / 所属用户' : '用户'}</TableHead>
        <TableHead className="text-right">事件数</TableHead><TableHead className="text-right">出口数</TableHead><TableHead className="text-right">账号数</TableHead>
        {source === 'api_call' && <TableHead className="text-right">令牌数</TableHead>}
        <TableHead className="text-right">成功 / 失败</TableHead><TableHead>最近出现</TableHead><TableHead className="text-right">操作</TableHead>
      </TableRow></TableHeader>
      <TableBody>{relations?.items?.length ? relations.items.map(row => {
        const user = relation === 'users' ? relations.user_labels?.[row.key] : undefined
        const token = relation === 'tokens' ? relations.token_labels?.[row.key] : undefined
        return <TableRow key={row.key}>
          <TableCell className="ip-relation-identity">
            {isIPRelation ? <>
              <button type="button" className="ip-inline-button font-mono text-sm font-semibold" onClick={() => inspectIP(row.key)}
                title="查看 IP 详情" aria-label={`查看 IP ${row.key} 的详情`} aria-haspopup="dialog"><Globe className="h-3.5 w-3.5 shrink-0" /><code>{row.key}</code></button>
              {!!row.user_ids?.length && <div className="ip-relation-users">{row.user_ids.slice(0, 3).map(id => {
                const label = relations.user_labels?.[String(id)]
                return <UserIdentityButton key={id} userId={id} username={label?.username} displayName={label?.display_name} compact onClick={() => openUser(id, label?.username)} />
              })}{row.users > 3 && <button type="button" className="ip-inline-button text-xs text-muted-foreground" onClick={() => inspectIP(row.key)}>另 {count(row.users - 3)} 个账号<ChevronRight className="h-3 w-3" /></button>}</div>}
            </> : relation === 'users' ? <UserIdentityButton userId={Number(row.key)} username={user?.username} displayName={user?.display_name} compact onClick={() => openUser(Number(row.key), user?.username)} />
              : <div className="space-y-1"><button type="button" className="ip-entity-button" onClick={() => openToken(Number(row.key), token?.name, period, data ? { start: data.window_start, end: data.window_end } : undefined)}><KeyRound className="h-4 w-4" /><span>{token?.name || `令牌 #${row.key}`}<small>#{row.key}</small></span></button>
                {token?.user_id ? <UserIdentityButton userId={token.user_id} username={relations.user_labels?.[String(token.user_id)]?.username}
                  displayName={relations.user_labels?.[String(token.user_id)]?.display_name} compact onClick={() => openUser(token.user_id, relations.user_labels?.[String(token.user_id)]?.username)} /> : null}</div>}
          </TableCell>
          <TableCell className="text-right tabular-nums font-medium">{count(row.events)}</TableCell>
          <TableCell className="text-right tabular-nums">{count(row.ips)}</TableCell><TableCell className="text-right tabular-nums">{count(row.users)}</TableCell>
          {source === 'api_call' && <TableCell className="text-right tabular-nums">{count(row.tokens)}</TableCell>}
          <TableCell className="whitespace-nowrap text-right tabular-nums"><span className="text-emerald-600">{count(row.success)}</span><span className="text-muted-foreground"> / </span><span className={row.failure ? 'text-red-600' : 'text-muted-foreground'}>{count(row.failure)}</span></TableCell>
          <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{time(row.last_seen_at, data?.timezone)}</TableCell>
          <TableCell className="text-right">
            <Button type="button" variant="ghost" size="sm" className="h-8 gap-1" onClick={() => isIPRelation ? inspectIP(row.key) : filterEntity(row.key, relation === 'tokens' ? 'tokenID' : 'userID')}>
              {isIPRelation ? <><Search className="h-3.5 w-3.5" />详情</> : <><Filter className="h-3.5 w-3.5" />查看出口</>}
            </Button>
          </TableCell>
        </TableRow>
      }) : <EmptyRows columns={relationColumns}>{relationsBusy ? '正在读取关联记录…' : unavailable}</EmptyRows>}</TableBody>
    </Table>
    <nav className="ip-relations-pagination" aria-label="出口关联分页">
      <span>共 {relations ? count(relations.total) : '—'} 项 · 每页 {pageSize} 项{relation === 'shared' ? ' · 共享出口不等于同一人' : ''}</span>
      <div><Button type="button" variant="outline" size="icon" className="h-8 w-8" disabled={!relations || page <= 1 || relationsBusy} onClick={() => setPage(value => value - 1)} aria-label="出口关联上一页"><ChevronLeft className="h-4 w-4" /></Button>
        <span>{relations ? `${page} / ${Math.max(1, Math.ceil(relations.total / pageSize))}` : '— / —'}</span>
        <Button type="button" variant="outline" size="icon" className="h-8 w-8" disabled={!relations || page >= Math.ceil(relations.total / pageSize) || relationsBusy} onClick={() => setPage(value => value + 1)} aria-label="出口关联下一页"><ChevronRight className="h-4 w-4" /></Button></div>
    </nav>
  </section>

  return <div className="ip-analysis-page space-y-6 animate-in fade-in duration-500">
    <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
      <div><h1 className="flex items-center gap-2 text-2xl font-bold"><Globe className="h-7 w-7 text-primary" />IP 分析</h1><p className="mt-1 text-sm text-muted-foreground">调用出口 · 登录与注册 · 账号及令牌关联</p></div>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || !data || exporting} onClick={() => void exportIPs()}>{exporting ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Download className="mr-2 h-4 w-4" />}导出 IP</Button>
        <Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy} onClick={() => setRefresh(value => value + 1)} className="h-9"><RefreshCw className={cn('mr-2 h-4 w-4', busy && 'animate-spin')} />{busy ? '正在获取最新数据...' : !UI_PREVIEW_ONLY && refreshInterval ? `刷新 (${countdown}s)` : '刷新'}</Button>
        <MenuChoice value={String(refreshInterval)} onChange={value => setRefreshInterval(Number(value))} ariaLabel="自动刷新间隔"
          items={[0, 30, 60, 300, 600].map(value => ({ value: String(value), label: value === 0 ? '关闭自动刷新' : value < 60 ? `${value} 秒` : `${value / 60} 分钟` }))}>
          <Timer className="h-4 w-4" /><span>{UI_PREVIEW_ONLY ? '刷新间隔' : refreshInterval > 0 ? `${countdown}s` : '自动刷新'}</span>
        </MenuChoice>
        <div className="inline-flex rounded-lg border bg-muted/50 p-1" role="group" aria-label="快捷统计时间范围">{[['1h', '1小时'], ['6h', '6小时'], ['24h', '24小时'], ['7d', '7天']].map(([value, label]) => <Button key={value} type="button" variant={period === value && !fixedWindow ? 'default' : 'ghost'} size="sm" onClick={() => selectPeriod(value)} className="h-7 px-3 text-xs" aria-pressed={period === value && !fixedWindow}>{label}</Button>)}</div>
      </div>
    </div>

    <Tabs value={source} onValueChange={value => selectSource(value as EventSource)} className="w-full">
      <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-center">
        <TabsList className="grid w-full max-w-md grid-cols-3">{sources.map(item => <TabsTrigger key={item.id} value={item.id} className="gap-2"><item.icon className="h-4 w-4" />{item.label}</TabsTrigger>)}</TabsList>
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><Badge variant={observed?.status === 'observed' ? 'success' : 'outline'}>{UI_PREVIEW_ONLY ? '数据暂停' : observed?.status === 'observed' ? '已观测' : busy ? '读取中' : '尚未观测'}</Badge><span>{data ? `更新于 ${time(data.as_of, data.timezone)}` : '尚未获取数据'}</span><Button type="button" variant="ghost" size="sm" className="h-8 gap-1.5 px-2" onClick={() => setShowFilters(value => !value)} aria-expanded={showFilters} aria-controls="ip-filter-panel"><Filter className="h-4 w-4" />筛选<ChevronDown className={cn('h-3.5 w-3.5 transition-transform', showFilters && 'rotate-180')} /></Button></div>
      </div>
      <TabsContent value={source} className="mt-6 space-y-6">
        {(Object.values(filters).some(Boolean) || fixedWindow) && <div className="ip-active-filters">
          {([['userID', '用户'], ['tokenID', '令牌'], ['ip', 'IP']] as const).filter(([field]) => filters[field]).map(([field, label]) => <button key={field} type="button"
            onClick={() => { const next = { ...filters, [field]: '' }; setDraft(next); setFilters(next); setRelation('addresses'); setPage(1) }}
            aria-label={`移除${label}筛选`}>{label} {filters[field]}<X className="h-3 w-3" /></button>)}
          {fixedWindow && <button type="button" onClick={() => setFixedWindow(undefined)}>{time(fixedWindow.window_start, data?.timezone)} 至 {time(fixedWindow.window_end, data?.timezone)}<X className="h-3 w-3" /><span className="sr-only">恢复滚动时间窗口</span></button>}
        </div>}
        {error && <Notice kind="error">{error}</Notice>}
        {message && <Notice kind="success">{message}</Notice>}
        <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6" aria-busy={busy}>
          <IPStatCard title="独立 IP 数" value={count(data?.summary.ips)} icon={MapPin} color="blue" />
          <IPStatCard title={`${activeSource.short}事件`} value={count(data?.summary.events)} icon={Activity} color="emerald" />
          <IPStatCard title="关联账号" value={count(data?.summary.users)} icon={Users} color="orange" />
          <IPStatCard title="成功占比" value={data && total > 0 ? rate(data.summary.success / total) : '—'} icon={TrendingUp} color="blue" />
          <IPStatCard title="IP 采集率" value={rate(data?.coverage.ip_capture_rate)} icon={Database} color="cyan" />
          <IPStatCard title="GeoIP" value={databaseState(data?.metadata.geo_available)} icon={CheckCircle2} color={data?.metadata.geo_available ? 'emerald' : 'slate'} />
        </div>

        {showFilters && <Card id="ip-filter-panel">
          <CardHeader className="pb-3"><CardTitle className="flex items-center gap-2 text-base font-medium"><Filter className="h-4 w-4" />筛选条件</CardTitle><CardDescription>按账号、令牌或出口查询当前来源，统计使用同一时间窗口。</CardDescription></CardHeader>
          <CardContent><form className="space-y-4" onSubmit={submitFilters}>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <label className="ip-reference-field"><span>时间范围</span><Select aria-label="IP 统计时间范围" value={period} onChange={event => selectPeriod(event.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</Select></label>
              <label className="ip-reference-field"><span>用户 ID</span><Input aria-label="用户 ID" inputMode="numeric" value={draft.userID} onChange={event => setDraft(value => ({ ...value, userID: event.target.value }))} placeholder="全部账号" /></label>
              {source === 'api_call' && <label className="ip-reference-field"><span>令牌 ID</span><Input aria-label="令牌 ID" inputMode="numeric" value={draft.tokenID} onChange={event => setDraft(value => ({ ...value, tokenID: event.target.value }))} placeholder="全部令牌" /></label>}
              <label className="ip-reference-field"><span>出口 IP</span><Input aria-label="出口 IP" value={draft.ip} onChange={event => setDraft(value => ({ ...value, ip: event.target.value }))} placeholder="IPv4 / IPv6" autoCapitalize="none" autoCorrect="off" /></label>
            </div>
            <div className="flex flex-wrap justify-end gap-2"><Button type="button" variant="outline" size="sm" onClick={() => { setDraft(defaultFilters); setFilters(defaultFilters); setFixedWindow(undefined); setRelation('addresses'); setPage(1); setMessage('') }}>重置</Button><Button type="submit" size="sm" disabled={UI_PREVIEW_ONLY || busy}><Search className="mr-2 h-4 w-4" />查询</Button></div>
          </form></CardContent>
        </Card>}

        <Card className="shadow-sm">
          <CardHeader className="pb-3"><CardTitle className="flex items-center gap-2 text-lg"><Search className="h-5 w-5 text-muted-foreground" />IP 画像查询</CardTitle><CardDescription>输入 IP 地址，查看地域、网络组织、运营商和代理属性。</CardDescription></CardHeader>
          <CardContent className="space-y-4"><form className="flex flex-col gap-3 sm:flex-row" onSubmit={event => { event.preventDefault(); void lookupIP(lookupDraft) }}>
            <div className="relative flex-1"><Input ref={lookupInput} aria-label="IPv4 / IPv6 地址" value={lookupDraft} onChange={event => { setLookupDraft(event.target.value); setLookupError('') }} placeholder="输入 IP 地址，如 192.168.1.1 或 2001:db8::1" className="pr-10" autoCapitalize="none" autoCorrect="off" />{lookupBusy && <Loader2 className="absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 animate-spin text-muted-foreground" />}</div>
            <Button type="submit" disabled={UI_PREVIEW_ONLY || lookupBusy} className="h-9 px-4"><Search className="mr-1.5 h-4 w-4" />查询</Button>
          </form>
            {lookupError && <Notice kind="error">{lookupError}</Notice>}
            {lookup && <div className="space-y-3 animate-in fade-in slide-in-from-top-2 duration-300">{lookup.address_class === 'public' && <IPAttributeEditor ip={lookup.ip} onUnauthorized={onUnauthorized} onSaved={() => { setRefresh(value => value + 1); void lookupIP(lookup.ip) }} />}<div className="grid grid-cols-2 gap-3 sm:grid-cols-4">{[
              ['国家 / 地区', lookup.country || (lookup.country_code ? countryName(lookup.country_code) : '未知')],
              ['地区', [lookup.region, lookup.city].filter(Boolean).join(' · ') || '未知'],
              ['ASN', lookup.asn ? `AS${lookup.asn}` : '未知'], ['运营商 ISP', lookup.isp || '未知'],
            ].map(([label, value]) => <div key={label} className="rounded-lg border bg-muted/30 p-3"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 break-words text-sm font-semibold">{value}</p></div>)}</div><div className="flex flex-wrap items-center gap-2"><Badge variant="secondary">{classLabels[lookup.address_class] ?? lookup.address_class}</Badge><span className="text-xs text-muted-foreground">{lookup.as_org || '网络组织未知'}</span><Button type="button" variant="outline" size="sm" className="ml-auto h-8" disabled={lookup.address_class === 'invalid'} onClick={() => inspectIP(lookup.ip)}>行为与网络详情<ChevronRight className="ml-1 h-3.5 w-3.5" /></Button></div>
              {lookup.intelligence && <div className="ip-lookup-intelligence">
                {lookup.intelligence.matches.length
                  ? lookup.intelligence.matches.map(match => <Badge key={`${match.kind}:${match.prefix}`} variant={match.kind === 'non_residential' ? 'outline' : 'warning'}>{intelligenceKindLabels[match.kind] ?? match.kind}命中</Badge>)
                  : <span>{lookup.intelligence.available ? '当前 IP 未命中已加载名单，代理属性仍可能未知。' : '本地识别名单尚不可用。'}</span>}
              </div>}
            </div>}
          </CardContent>
        </Card>

        {relationSection}

        <Card className="shadow-sm">
          <CardHeader className="pb-2"><div className="flex flex-wrap items-start justify-between gap-3"><div><CardTitle className="flex items-center gap-2 text-lg"><Globe className="h-5 w-5 text-muted-foreground" />{activeSource.short}出口分布（按{mapView === 'world' ? '国家 / 地区' : '省份'}）</CardTitle><CardDescription className="mt-1.5">过去 {currentPeriodLabel} · 已采集事件全量统计{data && ` · 地域解析率 ${rate(data.coverage.geo_resolution_rate)}`}</CardDescription></div>
            <MenuChoice value={mapView} onChange={value => setMapView(value as MapView)} ariaLabel="地图范围" items={[{ value: 'world', label: '世界地图', icon: Globe }, { value: 'china', label: '中国地图', icon: MapIcon }]}><span className="flex items-center gap-1.5">{mapView === 'world' ? <Globe className="h-4 w-4" /> : <MapIcon className="h-4 w-4" />}{mapView === 'world' ? '世界地图' : '中国地图'}</span></MenuChoice>
          </div></CardHeader>
          <CardContent className="p-0"><NetworkMap data={data} view={mapView} source={source} /></CardContent>
        </Card>

        <Card className="shadow-sm">
          <CardHeader className="pb-3"><div className="flex flex-wrap items-start justify-between gap-3"><div><CardTitle className="text-lg">{activeSource.short}出口地域排名</CardTitle><CardDescription className="mt-1.5">过去 {currentPeriodLabel} · 全量事件聚合，按地域内去重账号计数。</CardDescription></div>
            <Tabs value={geoLevel} onValueChange={value => setGeoLevel(value as GeoLevel)}><TabsList className="h-9">{([['country', '国家 / 地区'], ['region', '省 / 州'], ['city', '城市']] as const).map(([id, label]) => <TabsTrigger key={id} value={id} className="px-2.5 text-xs">{label}</TabsTrigger>)}</TabsList></Tabs>
          </div></CardHeader>
          <CardContent><Table className="min-w-[540px]" containerClassName="scroll-region scroll-region--md" containerLabel="IP 地域排名"><TableHeader><TableRow><TableHead>{geoTitles[geoLevel]}</TableHead><TableHead className="text-right">IP 数</TableHead><TableHead className="text-right">事件量</TableHead><TableHead className="text-right">关联账号</TableHead><TableHead className="text-right">占比</TableHead></TableRow></TableHeader><TableBody>
            {geoRows.length ? geoRows.slice((currentGeoPage - 1) * 20, currentGeoPage * 20).map(row => <TableRow key={row.key}><TableCell>{geoLabel(row, geoLevel)}</TableCell><TableCell className="text-right tabular-nums text-muted-foreground">{count(row.ips)}</TableCell><TableCell className="text-right font-medium tabular-nums">{count(row.events)}</TableCell><TableCell className="text-right tabular-nums text-muted-foreground">{count(row.users)}</TableCell><TableCell className="text-right tabular-nums text-muted-foreground">{total ? rate(row.events / total) : '—'}</TableCell></TableRow>) : <EmptyRows columns={5}>{unavailable}</EmptyRows>}
          </TableBody></Table>
            <div className="ip-relations-pagination mt-3"><span>共 {data ? count(geoRows.length) : '—'} 个地域 · 地域内账号去重</span><div>
              <Button type="button" variant="outline" size="icon" className="h-8 w-8" aria-label="地域排行上一页" disabled={currentGeoPage <= 1} onClick={() => setGeoPage(currentGeoPage - 1)}><ChevronLeft className="h-4 w-4" /></Button><span>{currentGeoPage} / {geoPages}</span>
              <Button type="button" variant="outline" size="icon" className="h-8 w-8" aria-label="地域排行下一页" disabled={currentGeoPage >= geoPages} onClick={() => setGeoPage(currentGeoPage + 1)}><ChevronRight className="h-4 w-4" /></Button>
            </div></div>
            <p className="mt-3 text-xs leading-5 text-muted-foreground">未知、未记录、内网及保留地址独立归类，不计入境外；同一账号在不同地域出现时分别计数，地域人数不相加。</p></CardContent>
        </Card>

        <Card className="shadow-sm">
          <CardHeader className="pb-2"><CardTitle className="flex items-center gap-2 text-lg"><Activity className="h-5 w-5 text-muted-foreground" />{activeSource.short}事件趋势</CardTitle><CardDescription>同一来源与时间窗口内的成功、失败和全部事件。</CardDescription></CardHeader>
          <CardContent><div className="ip-trend-chart">{data?.trend?.length ? <Chart option={trendOption} label={`${activeSource.short}事件趋势`} className="h-full" /> : <div className="ip-chart-empty"><Activity className="h-8 w-8 opacity-30" /><span>{unavailable}</span></div>}</div></CardContent>
        </Card>

        <div className="grid gap-6 lg:grid-cols-2">
          <Card className="shadow-sm"><CardHeader className="pb-3"><CardTitle className="flex items-center gap-2 text-lg"><Network className="h-5 w-5 text-muted-foreground" />ASN 与网络归属</CardTitle><CardDescription>自治系统与运营商归属独立于代理判断。</CardDescription></CardHeader><CardContent>
            <Table className="min-w-[380px]" containerClassName="scroll-region scroll-region--md" containerLabel="ASN 与网络归属列表"><TableHeader><TableRow><TableHead>ASN / 组织</TableHead><TableHead className="text-right">事件量</TableHead><TableHead className="text-right">账号数</TableHead></TableRow></TableHeader><TableBody>{data?.asn?.length ? data.asn.slice((currentASNPage - 1) * 10, currentASNPage * 10).map(row => <TableRow key={row.key}><TableCell><div className="font-medium">{row.key === 'unknown' ? '未知 ASN' : `AS${row.key}`}</div><div className="max-w-[220px] truncate text-xs text-muted-foreground" title={row.label}>{row.label || '组织未知'}</div></TableCell><TableCell className="text-right tabular-nums">{count(row.events)}</TableCell><TableCell className="text-right tabular-nums text-muted-foreground">{count(row.users)}</TableCell></TableRow>) : <EmptyRows columns={3}>{unavailable}</EmptyRows>}</TableBody></Table>
            <div className="ip-relations-pagination mt-3"><span>共 {data ? count(data.asn.length) : '—'} 个 ASN 分组</span><div>
              <Button type="button" variant="outline" size="icon" className="h-8 w-8" aria-label="ASN 上一页" disabled={currentASNPage <= 1} onClick={() => setASNPage(currentASNPage - 1)}><ChevronLeft className="h-4 w-4" /></Button><span>{currentASNPage} / {asnPages}</span>
              <Button type="button" variant="outline" size="icon" className="h-8 w-8" aria-label="ASN 下一页" disabled={currentASNPage >= asnPages} onClick={() => setASNPage(currentASNPage + 1)}><ChevronRight className="h-4 w-4" /></Button>
            </div></div>
          </CardContent></Card>
          <Card className="shadow-sm"><CardHeader className="pb-2"><CardTitle className="flex items-center gap-2 text-lg"><Globe className="h-5 w-5 text-muted-foreground" />地址类型</CardTitle><CardDescription>公网、内网、保留地址和未记录 IP 分别统计。</CardDescription></CardHeader><CardContent><div className="ip-small-chart">{data?.address_classes?.length ? <Chart option={addressOption} label="地址类型分布" className="h-full" /> : <div className="ip-chart-empty"><Globe className="h-8 w-8 opacity-30" /><span>{unavailable}</span></div>}</div></CardContent></Card>
        </div>

        <div className="grid min-w-0 gap-6 lg:grid-cols-2">
          <Card><CardHeader className="pb-3"><CardTitle className="flex items-center gap-2 text-lg"><Network className="h-5 w-5 text-muted-foreground" />网络类型分布</CardTitle><CardDescription>按事件量统计，网络类型未知单独保留。</CardDescription></CardHeader>
            <CardContent><Table containerClassName="scroll-region scroll-region--sm" containerLabel="网络类型分布"><TableHeader><TableRow><TableHead>网络类型</TableHead><TableHead className="text-right">事件</TableHead><TableHead className="text-right">IP</TableHead><TableHead className="text-right">账号</TableHead></TableRow></TableHeader>
              <TableBody>{data?.network_types?.length ? data.network_types.map(row => <TableRow key={row.key}><TableCell>{networkLabels[row.key] ?? row.key}</TableCell><TableCell className="text-right tabular-nums">{count(row.events)}</TableCell><TableCell className="text-right tabular-nums">{count(row.ips)}</TableCell><TableCell className="text-right tabular-nums">{count(row.users)}</TableCell></TableRow>) : <EmptyRows columns={4}>{unavailable}</EmptyRows>}</TableBody>
            </Table></CardContent>
          </Card>
          <Card className="shadow-sm"><CardHeader className="pb-3"><CardTitle className="flex items-center gap-2 text-lg"><ShieldCheck className="h-5 w-5 text-muted-foreground" />代理属性</CardTitle><CardDescription>代理标签可能重叠，托管网络不直接等同于代理或恶意行为。</CardDescription></CardHeader><CardContent><Table className="min-w-[380px]"><TableHeader><TableRow><TableHead>网络属性</TableHead><TableHead className="text-right">命中</TableHead><TableHead className="text-right">未命中</TableHead><TableHead className="text-right">未知</TableHead></TableRow></TableHeader><TableBody>{proxyRows.map(row => <TableRow key={row.label}><TableCell className="font-medium">{row.label}</TableCell><TableCell className="text-right tabular-nums text-amber-600">{data ? count(row.yes) : '—'}</TableCell><TableCell className="text-right tabular-nums text-emerald-600">{data ? count(row.no) : '—'}</TableCell><TableCell className="text-right tabular-nums text-muted-foreground">{data ? count(row.unknown) : '—'}</TableCell></TableRow>)}</TableBody></Table></CardContent></Card>
        </div>

        <Card className="shadow-sm"><CardHeader className="pb-3"><CardTitle className="flex items-center gap-2 text-lg"><Database className="h-5 w-5 text-muted-foreground" />数据质量</CardTitle><CardDescription>展示采集覆盖与解析状态，未知数据不解释为零风险。</CardDescription></CardHeader><CardContent className="space-y-4">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">{[['IP 采集率', 'ip_capture_rate'], ['账号归属率', 'account_attribution_rate'], ['地域解析率', 'geo_resolution_rate'], ['ASN 覆盖率', 'asn_resolution_rate'], ['代理检测覆盖率', 'proxy_detection_rate']].map(([label, key]) => <div key={key} className="rounded-lg border bg-muted/30 p-3"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 text-lg font-semibold">{rate(data?.coverage[key])}</p></div>)}</div>
          <section className="ip-intelligence-sources" aria-labelledby="ip-intelligence-title">
            <div className="ip-intelligence-heading"><div><h4 id="ip-intelligence-title"><ShieldCheck className="h-4 w-4" />IP 识别数据源</h4>
              <p>{intelligence ? `名单加载于 ${time(intelligence.loaded_at, data?.timezone)}` : UI_PREVIEW_ONLY ? '数据获取已暂停' : busy ? '正在读取数据源状态…' : '尚未获取数据源状态'}</p></div>
              <Badge variant={intelligence?.available ? 'success' : 'outline'}>{intelligence?.available ? '本地名单可用' : intelligence?.configured ? '名单待就绪' : '未加载名单'}</Badge></div>
            {intelligence && <>
              <Table className="min-w-[610px]" containerClassName="scroll-region scroll-region--sm" containerLabel="IP 识别名单加载状态">
                <TableHeader><TableRow><TableHead>识别名单</TableHead><TableHead>来源</TableHead><TableHead className="text-right">网段 / 出口条数</TableHead><TableHead>文件更新</TableHead><TableHead>状态</TableHead></TableRow></TableHeader>
                <TableBody>{intelligence.files.map(file => <TableRow key={file.name}>
                  <TableCell className="font-medium">{intelligenceFileLabels[file.name] ?? file.name}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{intelligenceSourceLabels[file.source] ?? file.source}</TableCell>
                  <TableCell className="text-right tabular-nums">{file.status === 'loaded' ? count(file.prefix_count) : '—'}</TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{time(file.last_modified, data?.timezone)}</TableCell>
                  <TableCell><Badge variant={file.status === 'loaded' ? 'success' : file.status === 'invalid' ? 'warning' : 'outline'}>{file.status === 'loaded' ? '已加载' : file.status === 'invalid' ? '文件不可用' : '未安装'}</Badge></TableCell>
                </TableRow>)}{!intelligence.files.length && <EmptyRows columns={5}>尚未安装本地识别名单</EmptyRows>}</TableBody>
              </Table>
              {!!intelligence.warnings.length && <details className="ip-intelligence-warnings"><summary>数据源提示 · {intelligence.warnings.length} 项</summary><ul>{intelligence.warnings.map(warning => <li key={warning}>{warning}</li>)}</ul></details>}
              <p className="ip-intelligence-scope">VPN 与 Tor 按名单命中提供线索，未命中保留未知。非住宅名单包含机房或代理出口，不能据此认定为 VPN。
                {!(intelligence.geo?.anonymous_available ?? data?.metadata.proxy_database_available) && ' 当前未接入公共代理、住宅代理的独立识别数据源。'}</p>
              <details className="ip-intelligence-warnings"><summary>名单版本</summary><p>{intelligence.version || '暂无版本信息'}</p></details>
            </>}
          </section>
          <IPMaintenancePanel onUnauthorized={onUnauthorized} onChanged={() => setRefresh(value => value + 1)} />
          {syncJob && syncJob.state !== 'idle' && <section className="space-y-3 rounded-xl border bg-muted/20 p-4" aria-label="调用日志后台导入进度">
            <div className="flex flex-wrap items-center justify-between gap-2"><div className="flex items-center gap-2 text-sm font-medium"><Database className="h-4 w-4 text-primary" />调用日志后台同步<Badge variant={syncJob.state === 'completed' ? 'success' : syncJob.state === 'failed' ? 'warning' : 'outline'}>{({ queued: '排队中', running: '导入中', completed: '已完成', failed: '导入失败', cancelled: '已停止', interrupted: '等待续传' } as Record<string, string>)[syncJob.state] ?? syncJob.state}</Badge></div>{syncActive && <Button size="sm" variant="outline" onClick={() => void cancelSync()}>停止本次任务</Button>}</div>
            {syncJob.progress != null && <div><div className="mb-1 flex justify-between text-xs text-muted-foreground"><span>本次源日志 ID 游标进度</span><span>{syncJob.progress}%</span></div><div className="h-1.5 overflow-hidden rounded-full bg-muted"><div className="h-full bg-primary transition-[width]" style={{ width: `${syncJob.progress}%` }} /></div></div>}
            <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted-foreground"><span>本次已提交 {count(syncJob.imported)} 条事件</span><span>游标 {count(syncJob.cursor)} / {count(syncJob.watermark)}</span><span>任务中断后从已提交游标继续</span></div>
            {(syncJob.error || syncJob.checkpoint_error) && <p className="text-xs text-amber-600">{syncJob.error || syncJob.checkpoint_error}</p>}
          </section>}
          <details className="ip-reference-details rounded-lg border"><summary className="flex cursor-pointer items-center justify-between gap-2 p-3 text-sm font-medium"><span className="flex items-center gap-2"><Database className="h-4 w-4 text-muted-foreground" />采集范围与数据源</span><ChevronDown className="h-4 w-4 text-muted-foreground" /></summary><dl className="ip-reference-dl border-t px-3 py-3">
            <div><dt>统计范围</dt><dd>{UI_PREVIEW_ONLY ? '数据获取已暂停' : '已采集事件全量统计'}</dd></div><div><dt>时间窗口</dt><dd>{data ? `${time(data.window_start, data.timezone)} — ${time(data.window_end, data.timezone)}` : '—'}</dd></div><div><dt>最近调用同步</dt><dd>{data ? time(data.metadata.last_call_sync, data.timezone) : '未请求'}</dd></div><div><dt>地域 / ASN 库</dt><dd>{databaseState(data?.metadata.geo_available)} / {databaseState(data?.metadata.asn_available)}</dd></div><div><dt>ISP / 代理库</dt><dd>{databaseState(data?.metadata.isp_available)} / {databaseState(data?.metadata.proxy_database_available)}</dd></div><div><dt>探针历史覆盖</dt><dd>{UI_PREVIEW_ONLY ? '未请求' : '未测量，不以零事件表示没有行为'}</dd></div>{Object.entries(data?.metadata.ip_database_versions ?? {}).map(([key, value]) => <div key={key}><dt>{key} 版本</dt><dd>{value}</dd></div>)}
          </dl></details>
          {!busy && data && source !== 'api_call' && observed?.status !== 'observed' && <Notice kind="info">尚未观测到{activeSource.short}事件。探针接入后在此统计，不能据此判断没有{activeSource.short}行为。</Notice>}
          {!busy && data && source === 'api_call' && !data.metadata.history_complete && <Notice kind="warning">当前覆盖已保留的调用记录，历史完整性尚未确认。</Notice>}
          <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground"><span>{UI_PREVIEW_ONLY ? '同步尚未启用' : data ? `失败 ${count(data.summary.failure)} 次${pending ? ` · 待完成 ${count(pending)} 次` : ''}` : '尚未获取数据'}</span>{source === 'api_call' && <Button type="button" variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || syncing || busy} onClick={() => void syncCalls()}><Database className={cn('mr-2 h-4 w-4', syncing && 'animate-spin')} />{syncing ? '同步中...' : '同步调用日志'}</Button>}</div>
        </CardContent></Card>
      </TabsContent>
    </Tabs>
  </div>
}
