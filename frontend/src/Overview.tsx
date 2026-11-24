import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity, BarChart3, Box, Check, ChevronDown, Clock3, Database, Gauge, Hash,
  KeyRound, Loader2, RefreshCw, Server, Timer, TrendingUp, Users, WalletCards,
} from 'lucide-react'
import { ApiError, get, UI_PREVIEW_ONLY } from './api'
import type { DashboardData, DashboardPeriod } from './types'
import { StatCard } from './components/StatCard'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Button } from './components/ui/button'
import { Badge } from './components/ui/badge'
import { AdminDialog, Notice } from './ModuleUI'
import OverviewChampions from './OverviewChampions'
import RiskUserCard from './RiskUserCard'
import { cn } from './lib/utils'

const DashboardChart = lazy(() => import('./DashboardChart'))
const periodOptions: { value: DashboardPeriod; label: string; description: string }[] = [
  { value: '24h', label: '24小时', description: '最近 24 小时' },
  { value: '7d', label: '7天', description: '最近 7 天' },
  { value: '30d', label: '30天', description: '最近 30 天' },
]
const refreshOptions = [0, 30, 60, 120, 300]
const modelColors = ['bg-blue-500', 'bg-emerald-500', 'bg-violet-500', 'bg-orange-500', 'bg-pink-500', 'bg-cyan-500', 'bg-amber-500', 'bg-rose-500']
const modelChartColors = [
  ['#3b82f6', '#93c5fd'],
  ['#10b981', '#6ee7b7'],
  ['#8b5cf6', '#c4b5fd'],
  ['#f97316', '#fdba74'],
  ['#ec4899', '#f9a8d4'],
  ['#06b6d4', '#67e8f9'],
  ['#eab308', '#fde047'],
  ['#f43f5e', '#fda4af'],
]

function prettyModelName(model: string) {
  const value = model.trim()
  if (!value) return { short: '未知模型', vendor: 'unknown' }
  const normalized = value.toLowerCase()
  const vendorMap: [string, string][] = [
    ['deepseek', 'DeepSeek'],
    ['claude', 'Claude'],
    ['gpt', 'GPT'],
    ['gemini', 'Gemini'],
    ['qwen', 'Qwen'],
    ['llama', 'Llama'],
    ['mistral', 'Mistral'],
  ]
  const vendor = vendorMap.find(([keyword]) => normalized.includes(keyword))?.[1] ?? 'Model'
  const version = value.match(/(?:[^/\-]+\+)?([a-z0-9]+(?:\.[0-9]+)*|latest|preview|mini|flash|pro)$/i)?.[1]
  return { short: version ? `${vendor} ${version}` : vendor, vendor }
}

function count(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : value.toLocaleString('zh-CN')
}
function compact(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}
function money(value: number | null | undefined, unit: number | undefined) {
  if (value == null || !unit || !Number.isFinite(value)) return '—'
  return new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', maximumFractionDigits: value / unit < 1 ? 4 : 2 }).format(value / unit)
}
function timestamp(value: string | undefined, timezone = 'Asia/Shanghai') {
  if (!value) return '尚未获取数据'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: timezone,
  }).format(new Date(value))
}
function refreshLabel(value: number) {
  return value === 0 ? '关闭自动刷新' : value < 60 ? `${value} 秒` : `${value / 60} 分钟`
}
function makeTrendOption(data: DashboardData, period: DashboardPeriod) {
  const points = data.trend ?? []
  const label = new Intl.DateTimeFormat('zh-CN', {
    ...(period === '24h' ? { hour: '2-digit', minute: '2-digit' } : { month: 'numeric', day: 'numeric' }),
    timeZone: data.timezone,
  })
  return {
    animationDuration: 300,
    color: ['#2563eb', '#fb7185', '#059669'],
    backgroundColor: 'transparent',
    grid: { top: 64, right: 24, bottom: 24, left: 10, containLabel: true },
    tooltip: {
      trigger: 'axis', renderMode: 'richText',
      axisPointer: { type: 'line', lineStyle: { color: '#94a3b8', type: 'dashed' } },
    },
    legend: { top: 0, itemWidth: 14, itemHeight: 8, itemGap: 18, icon: 'roundRect', textStyle: { color: '#475569', fontSize: 13 } },
    xAxis: {
      type: 'category', boundaryGap: false, data: points.map(point => label.format(new Date(point.at))),
      axisTick: { show: false }, axisLine: { lineStyle: { color: '#dbeafe' } },
      axisLabel: { color: '#64748b', fontSize: 12, hideOverlap: true, margin: 12 },
    },
    yAxis: [
      {
        type: 'value', minInterval: 1, splitNumber: 4,
        axisLabel: { color: '#64748b', fontSize: 12 },
        splitLine: { lineStyle: { color: '#eef2f7', type: 'dashed' } },
      },
      { type: 'value', axisLabel: { color: '#64748b', fontSize: 12, formatter: '${value}' }, splitLine: { show: false } },
    ],
    series: [
      {
        name: '请求日志', type: 'line', symbol: 'circle', symbolSize: 5, showSymbol: false, smooth: .3,
        lineStyle: { width: 3, shadowColor: 'rgba(37,99,235,.16)', shadowBlur: 10, shadowOffsetY: 5 },
        areaStyle: {
          color: {
            type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(37,99,235,.22)' },
              { offset: 1, color: 'rgba(37,99,235,0)' },
            ],
          },
        },
        data: points.map(point => point.requests),
      },
      { name: '错误日志', type: 'bar', barWidth: '42%', itemStyle: { borderRadius: [4, 4, 0, 0], color: '#fb7185' }, data: points.map(point => point.errors) },
      {
        name: '消耗额度', type: 'line', yAxisIndex: 1, symbol: 'none', smooth: .3,
        lineStyle: { width: 2, type: 'dashed' }, data: points.map(point => point.quota / data.quota_per_unit),
      },
    ],
  }
}

function makeModelOption(models: { model: string; requests: number; quota: number }[], metric: 'requests' | 'quota', period: string, quotaPerUnit?: number) {
  const values = models.map(item => metric === 'quota' ? item.quota / (quotaPerUnit || 1) : item.requests)
  const labels = models.map(item => prettyModelName(item.model).short)
  return {
    animationDuration: 500,
    backgroundColor: 'transparent',
    grid: { top: 20, left: 6, right: 42, bottom: 4, containLabel: true },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow', shadowStyle: { color: 'rgba(148,163,184,.08)' } },
      formatter: (item: { dataIndex: number; value: number }) => {
        const model = models[item.dataIndex]?.model ?? '未知模型'
        const value = metric === 'quota' ? `$${item.value.toFixed(4)}` : item.value.toLocaleString('zh-CN')
        return `${model}<br/>${metric === 'quota' ? '消耗额度' : '请求数量'}：${value}`
      },
    },
    xAxis: { type: 'value', show: false, max: Math.max(...values, 1) },
    yAxis: {
      type: 'category', inverse: true, data: labels,
      axisTick: { show: false }, axisLine: { show: false },
      axisLabel: {
        color: '#334155', fontSize: 12, fontWeight: 600, margin: 12, width: 118, overflow: 'truncate',
        formatter: (value: string) => `{vendor|${value}}`,
        rich: {
          vendor: {
            color: '#334155', fontSize: 12, fontWeight: 600,
            backgroundColor: '#f1f5f9', padding: [4, 8, 5, 8], borderRadius: 7,
          },
        },
      },
    },
    series: [{
      type: 'bar', barWidth: '55%', z: 2,
      itemStyle: { borderRadius: [0, 8, 8, 0] },
      label: {
        show: true, position: 'right', distance: 8, color: '#475569', fontSize: 12, fontWeight: 600,
        formatter: (item: { value: number }) => metric === 'quota' ? `$${item.value.toFixed(2)}` : item.value.toLocaleString('zh-CN'),
      },
      data: values.map((value, index) => ({
        value,
        itemStyle: {
          borderRadius: [0, 8, 8, 0],
          color: {
            type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
            colorStops: [
              { offset: 0, color: modelChartColors[index % modelChartColors.length][0] },
              { offset: 1, color: modelChartColors[index % modelChartColors.length][1] },
            ],
          },
        },
      })),
    }],
    graphic: models.map((item, index) => {
      const color = modelChartColors[index % modelChartColors.length][0]
      return {
        type: 'rect', left: 12, top: (index * 54) + 26, shape: { width: 5, height: 18, r: [3, 3, 3, 3] },
        style: { fill: color },
      }
    }),
  }
}

export default function Overview({ tokenVersion = 0, onUnauthorized }: {
  tokenVersion?: number; onUnauthorized: () => void
}) {
  const [period, setPeriod] = useState<DashboardPeriod>('7d')
  const [revision, setRevision] = useState(0)
  const [snapshot, setSnapshot] = useState<{ tokenVersion: number; period: DashboardPeriod; data: DashboardData } | null>(null)
  const [busy, setBusy] = useState(!UI_PREVIEW_ONLY)
  const [error, setError] = useState('')
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [countdown, setCountdown] = useState(0)
  const [refreshMenuOpen, setRefreshMenuOpen] = useState(false)
  const [modelMetric, setModelMetric] = useState<'requests' | 'quota'>('requests')
  const [selectedUser, setSelectedUser] = useState<{ id: number; username?: string } | null>(null)
  const menu = useRef<HTMLDivElement>(null)
  const menuTrigger = useRef<HTMLButtonElement>(null)
  const menuOptions = useRef<(HTMLButtonElement | null)[]>([])
  const requestVersion = useRef(0)

  useEffect(() => {
    if (UI_PREVIEW_ONLY) { setBusy(false); setSnapshot(null); setError(''); return }
    const controller = new AbortController()
    const serial = ++requestVersion.current
    setBusy(true)
    setError('')
    get<DashboardData>(`/api/dashboard?period=${period}`, controller.signal).then(result => {
      if (controller.signal.aborted || serial !== requestVersion.current) return
      if (result.period !== period) throw new ApiError('概览时间范围与请求不一致，请重试。', 503)
      setSnapshot({ tokenVersion, period, data: result })
    }).catch(cause => {
      if (controller.signal.aborted || serial !== requestVersion.current) return
      if (cause instanceof ApiError && cause.status === 401) {
        setSnapshot(null)
        onUnauthorized()
      } else setError(cause instanceof Error ? cause.message : '暂时无法读取概览，请稍后重试。')
    }).finally(() => {
      if (!controller.signal.aborted && serial === requestVersion.current) setBusy(false)
    })
    return () => controller.abort()
  }, [period, revision, tokenVersion, onUnauthorized])

  useEffect(() => {
    setCountdown(refreshInterval)
    if (UI_PREVIEW_ONLY || !refreshInterval || busy) return
    const deadline = Date.now() + refreshInterval * 1000
    const timer = window.setInterval(() => {
      const remaining = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setCountdown(remaining)
      if (!remaining && document.visibilityState === 'visible') {
        window.clearInterval(timer)
        setRevision(current => current + 1)
      }
    }, 1000)
    return () => window.clearInterval(timer)
  }, [refreshInterval, busy, period, revision, tokenVersion])

  useEffect(() => {
    if (!refreshMenuOpen) return
    const close = (event: PointerEvent) => { if (!menu.current?.contains(event.target as Node)) setRefreshMenuOpen(false) }
    document.addEventListener('pointerdown', close)
    menuOptions.current[Math.max(0, refreshOptions.indexOf(refreshInterval))]?.focus()
    return () => document.removeEventListener('pointerdown', close)
  }, [refreshMenuOpen])

  const data = !UI_PREVIEW_ONLY && snapshot?.period === period && snapshot.tokenVersion === tokenVersion ? snapshot.data : null
  const usage = data?.usage
  const inventory = data?.inventory
  const periodLabel = periodOptions.find(item => item.value === period)!.description
  const totalTokens = usage?.prompt_tokens == null || usage.completion_tokens == null ? null : usage.prompt_tokens + usage.completion_tokens
  const models = useMemo(() => [...(data?.models ?? [])].sort((a, b) => b[modelMetric] - a[modelMetric]).slice(0, 8), [data, modelMetric])
  const modelMax = Math.max(1, ...models.map(model => model[modelMetric]))
  const trendOption = useMemo(() => data ? makeTrendOption(data, period) : {}, [data, period])
  const modelOption = useMemo(() => makeModelOption(models, modelMetric, periodLabel, data?.quota_per_unit), [models, modelMetric, periodLabel, data?.quota_per_unit])
  const emptyLabel = UI_PREVIEW_ONLY ? '数据获取已暂停' : busy ? '正在读取数据...' : '暂无数据'

  return <div className="space-y-8 animate-in fade-in duration-500">
    <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
      <div><h2 className="text-3xl font-bold">仪表盘</h2><p className="mt-1 text-muted-foreground">系统运行状态与数据概览</p></div>
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2">
          <Button type="button" variant="outline" size="sm" className="h-9" disabled={UI_PREVIEW_ONLY || busy} onClick={() => setRevision(current => current + 1)}><RefreshCw className={cn('mr-2 h-4 w-4', busy && 'animate-spin')} />{busy ? '刷新中...' : '刷新'}</Button>
          <div className="relative" ref={menu}>
            <Button ref={menuTrigger} type="button" variant="outline" size="sm" className="h-9 min-w-[100px]" aria-label="概览自动刷新间隔" aria-haspopup="menu" aria-expanded={refreshMenuOpen} onClick={() => setRefreshMenuOpen(current => !current)} onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); setRefreshMenuOpen(true) } }}><Timer className="mr-2 h-4 w-4" />{UI_PREVIEW_ONLY || !refreshInterval ? '自动刷新' : <span className="font-medium text-primary">{countdown}s</span>}<ChevronDown className="ml-1 h-3 w-3" /></Button>
            {refreshMenuOpen && <div className="absolute right-0 z-50 mt-1 w-48 rounded-md border bg-popover text-popover-foreground shadow-lg" role="menu" aria-label="概览自动刷新间隔" onKeyDown={event => {
              const current = menuOptions.current.indexOf(document.activeElement as HTMLButtonElement)
              const next = event.key === 'ArrowDown' ? (current + 1) % refreshOptions.length : event.key === 'ArrowUp' ? (current - 1 + refreshOptions.length) % refreshOptions.length : event.key === 'Home' ? 0 : event.key === 'End' ? refreshOptions.length - 1 : -1
              if (next >= 0) { event.preventDefault(); menuOptions.current[next]?.focus() }
              if (event.key === 'Escape') { event.preventDefault(); setRefreshMenuOpen(false); menuTrigger.current?.focus() }
              if (event.key === 'Tab') setRefreshMenuOpen(false)
            }}>
              <div className="border-b p-2"><p className="text-xs text-muted-foreground">刷新间隔</p></div>
              <div className="p-1">{refreshOptions.map((value, index) => <button key={value} ref={element => { menuOptions.current[index] = element }} type="button" role="menuitemradio" aria-checked={value === refreshInterval} className={cn('flex w-full items-center justify-between rounded px-3 py-2 text-left text-sm outline-none transition-colors hover:bg-accent focus:bg-accent', value === refreshInterval && 'bg-accent text-accent-foreground')} onClick={() => { setRefreshInterval(value); setRefreshMenuOpen(false); menuTrigger.current?.focus() }}>{refreshLabel(value)}{value === refreshInterval && <Check className="h-3.5 w-3.5" />}</button>)}</div>
              <div className="border-t p-2 text-xs leading-5 text-muted-foreground">{UI_PREVIEW_ONLY ? '数据读取暂停，当前不会自动请求。' : `上次刷新：${timestamp(data?.generated_at, data?.timezone)}`}</div>
            </div>}
          </div>
        </div>
        <div className="inline-flex rounded-lg border bg-muted/50 p-1" role="group" aria-label="概览统计时间范围">{periodOptions.map(item => <Button key={item.value} type="button" variant={period === item.value ? 'default' : 'ghost'} size="sm" className="h-7 px-3 text-xs" aria-pressed={period === item.value} onClick={() => setPeriod(item.value)}>{item.label}</Button>)}</div>
      </div>
    </div>

    {error && <Notice kind="error">{error}{data && <span className="mt-1 block text-xs">当前保留上次读取结果，统计于 {timestamp(data.generated_at, data.timezone)}。</span>}</Notice>}
    {data?.warnings.map(warning => <Notice key={warning} kind="warning">{warning}</Notice>)}

    <section className="space-y-4" aria-labelledby="overview-inventory">
      <h3 id="overview-inventory" className="flex items-center gap-2 text-lg font-semibold"><Database className="h-5 w-5 text-primary" />平台资源</h3>
      <div className="grid grid-cols-1 gap-3 xs:grid-cols-2 sm:gap-4 lg:grid-cols-4">
        <StatCard title="用户总数" value={count(inventory?.total_users)} subValue={usage?.active_users == null ? '活跃情况未读取' : `${count(usage.active_users)} 活跃（${periodLabel}）`} icon={Users} color="blue" />
        <StatCard title="令牌总数" value={count(inventory?.total_tokens)} subValue={inventory?.active_tokens == null ? '启用情况未读取' : `${count(inventory.active_tokens)} 个启用`} icon={KeyRound} color="emerald" />
        <StatCard title="渠道总数" value={count(inventory?.total_channels)} subValue={inventory?.active_channels == null ? '渠道状态未读取' : `${count(inventory.active_channels)} 个启用`} icon={Server} color="purple" />
        <StatCard title="模型数量" value={count(inventory?.total_models)} subValue={UI_PREVIEW_ONLY ? '未读取模型资料' : '实例模型资料'} icon={Box} color="orange" />
      </div>
    </section>

    <section className="space-y-4" aria-labelledby="overview-traffic">
      <div className="flex flex-wrap items-center justify-between gap-2"><h3 id="overview-traffic" className="flex items-center gap-2 text-lg font-semibold"><Activity className="h-5 w-5 text-primary" />流量分析（{periodLabel}）</h3><span className="text-xs text-muted-foreground">{UI_PREVIEW_ONLY ? '未获取数据' : data ? `更新于 ${timestamp(data.generated_at, data.timezone)}` : '尚未读取数据'}</span></div>
      <div className="grid grid-cols-1 gap-3 xs:grid-cols-2 sm:gap-4 md:grid-cols-3">
        <StatCard title="请求日志" value={compact(usage?.requests)} icon={BarChart3} color="indigo" variant="compact" />
        <StatCard title="消耗额度" value={money(usage?.quota, data?.quota_per_unit)} icon={WalletCards} color="amber" variant="compact" />
        <StatCard title="活跃用户" value={count(usage?.active_users)} icon={Users} color="emerald" variant="compact" />
        <StatCard title="日志成功率" value={usage?.success_rate == null ? '—' : `${usage.success_rate.toFixed(1)}%`} icon={Gauge} color="blue" variant="compact" />
        <StatCard title="总 Token" value={compact(totalTokens)} icon={Hash} color="purple" variant="compact" />
        <StatCard title="平均响应" value={usage?.avg_duration_seconds == null ? '—' : `${(usage.avg_duration_seconds * 1000).toFixed(0)}ms`} icon={Clock3} color="rose" variant="compact" />
      </div>
    </section>

    <div className="grid grid-cols-1 items-stretch gap-6 lg:grid-cols-2">
      <Card className="glass-card flex h-full flex-col overflow-hidden border-border/50 shadow-sm transition-all duration-300 hover:shadow-lg">
        <CardHeader className="shrink-0 pb-0"><div className="flex items-center justify-between gap-3"><div className="space-y-1"><CardTitle className="flex items-center gap-2 text-lg"><span className="rounded-lg bg-primary/10 p-2 text-primary"><TrendingUp className="h-5 w-5" /></span>{period === '24h' ? '每小时请求趋势' : '每日请求趋势'}</CardTitle><CardDescription>{periodLabel}数据概览</CardDescription></div><div className="hidden text-right sm:block"><div className="text-2xl font-bold text-primary">{count(usage?.requests)}</div><div className="text-xs font-medium text-muted-foreground">请求日志数</div></div></div></CardHeader>
        <CardContent className="flex flex-1 flex-col justify-end pb-8 pt-6">
          {busy && !data ? <div className="flex h-72 items-center justify-center xl:h-96"><Loader2 className="h-10 w-10 animate-spin text-primary" /></div>
            : data?.trend.length ? <div className="h-72 w-full xl:h-96"><Suspense fallback={<div className="flex h-full items-center justify-center text-muted-foreground"><Loader2 className="h-6 w-6 animate-spin" /></div>}><DashboardChart option={trendOption} /></Suspense></div>
              : <div className="flex h-72 flex-col items-center justify-center rounded-xl border border-dashed border-muted bg-muted/5 text-muted-foreground xl:h-96"><BarChart3 className="mb-2 h-10 w-10 opacity-20" /><p className="text-sm">{UI_PREVIEW_ONLY ? '数据获取已暂停' : '暂无趋势数据'}</p><p className="mt-1 text-xs">{periodLabel} · 请求 / 错误 / 消耗额度</p></div>}
        </CardContent>
      </Card>

      <Card className="flex h-full flex-col overflow-hidden border-border/60 shadow-sm transition-all duration-300 hover:shadow-lg">
        <CardHeader><div className="flex flex-wrap items-start justify-between gap-3"><div><CardTitle className="flex items-center gap-2 text-lg"><Box className="h-5 w-5 text-muted-foreground" />模型使用分布</CardTitle><CardDescription className="mt-1.5">{periodLabel}内 Top 8 活跃模型</CardDescription></div><div className="inline-flex rounded-lg border bg-muted/50 p-1" role="group" aria-label="模型用量排序"><Button type="button" size="sm" variant={modelMetric === 'requests' ? 'default' : 'ghost'} className="h-7 px-2.5 text-xs" aria-pressed={modelMetric === 'requests'} onClick={() => setModelMetric('requests')}>按请求</Button><Button type="button" size="sm" variant={modelMetric === 'quota' ? 'default' : 'ghost'} className="h-7 px-2.5 text-xs" aria-pressed={modelMetric === 'quota'} onClick={() => setModelMetric('quota')}>按消耗</Button></div></div></CardHeader>
        <CardContent className="flex-1 overflow-hidden pb-6 pt-4">
          {models.length ? <div className="h-full min-h-[300px]"><Suspense fallback={<div className="flex h-full items-center justify-center text-muted-foreground"><Loader2 className="h-6 w-6 animate-spin" /></div>}><DashboardChart option={modelOption} /></Suspense></div>
            : <div className="flex h-full min-h-[300px] items-center justify-center rounded-lg bg-muted/20 text-sm text-muted-foreground">{emptyLabel}</div>}
        </CardContent>
      </Card>
    </div>

    <OverviewChampions tokenVersion={tokenVersion} refreshVersion={revision} onUnauthorized={onUnauthorized} onViewUser={user => setSelectedUser(user)} />

    {selectedUser && <AdminDialog title={`用户详情 · ${selectedUser.username || `#${selectedUser.id}`}`} wide onClose={() => setSelectedUser(null)}>
      <RiskUserCard userId={selectedUser.id} username={selectedUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
    </AdminDialog>}
  </div>
}
