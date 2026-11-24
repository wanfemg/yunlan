import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Activity, AlertTriangle, Check, ChevronLeft, ChevronRight, Clock3, Filter, Globe,
  Infinity as InfinityIcon, KeyRound, RefreshCw, Search, ShieldBan, ShieldCheck,
  X,
} from 'lucide-react'
import './token-management.css'
import { authenticatedHeaders, notifyUnauthorized, UI_PREVIEW_ONLY } from './api'
import { useIPDetails } from './IPDetailsContext'
import { AdminDialog } from './ModuleUI'
import { StatCard } from './components/StatCard'
import { UserIdentityButton } from './components/UserIdentityButton'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import { cn } from './lib/utils'

type TokenRecord = {
  id: number
  key: string
  name: string
  user_id: number
  username: string
  status: number
  quota: number
  used_quota: number
  remain_quota: number
  unlimited_quota: boolean
  models: string
  subnet: string
  group: string
  created_time: number
  accessed_time: number
  expired_time: number
  ip_count?: number
  request_count?: number
}

type TokenGroup = { group_name: string; token_count: number; active_count: number }
type Statistics = { total: number; active: number; disabled: number; expired: number }
type TokenPage = { items: TokenRecord[]; total: number; page: number; page_size: number; total_pages: number }
type Filters = { status: string; name: string; group: string; risk: string; user_id: string }
type Reply<T> = { success: boolean; data: T; message?: string }
type TokenTarget = Pick<TokenRecord, 'id' | 'name'>
type StatusFields = Pick<TokenRecord, 'status' | 'expired_time'>
type ChangeCount = { requested: number; enabled?: number; disabled?: number }

const PAGE_SIZE = 20
const defaultFilters: Filters = { status: '', name: '', group: '', risk: '', user_id: '' }
const numberFormat = new Intl.NumberFormat('zh-CN')

function initialTokenQuery() {
  const params = new URLSearchParams(window.location.search)
  const positiveID = (key: string) => {
    const value = params.get(key) ?? ''
    return /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value)) ? value : ''
  }
  const id = positiveID('token_id')
  return {
    filters: { ...defaultFilters, user_id: positiveID('user_id') },
    target: id ? { id: Number(id), name: '' } : null,
    window: tokenWindow(params.get('period') ?? params.get('window') ?? undefined),
    range: tokenRange({ start: Number(positiveID('start')), end: Number(positiveID('end')) }),
  }
}

class RequestError extends Error {
  constructor(message: string, readonly status?: number) {
    super(message)
  }
}

async function request<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  if (UI_PREVIEW_ONLY) throw new RequestError('数据获取已暂停')
  const headers = authenticatedHeaders(init.headers, init.method ?? 'GET')
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(`${import.meta.env.VITE_API_BASE ?? ''}${path}`, {
    ...init,
    cache: 'no-store',
    credentials: 'same-origin',
    signal,
    headers,
  })
  const payload = await response.json().catch(() => null) as Reply<T> | { error?: string } | null
  if (response.status === 401) {
    notifyUnauthorized(response.status, path)
    throw new RequestError('登录已失效，请重新登录。', response.status)
  }
  if (!response.ok) {
    const message = payload && 'message' in payload ? payload.message : payload && 'error' in payload ? payload.error : null
    throw new RequestError(message || `请求失败 (${response.status})`, response.status)
  }
  if (payload && 'success' in payload) {
    if (!payload.success) throw new RequestError(payload.message || '操作失败。')
    return payload.data
  }
  throw new RequestError('服务器返回了无法识别的数据。')
}

function formatTime(timestamp: number) {
  if (!timestamp || timestamp < 1) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(new Date(timestamp * 1000))
}

function formatQuota(value: number, quotaPerUnit: number) {
  return `$${(Number(value || 0) / quotaPerUnit).toFixed(2)}`
}

function maskKey(key: string) {
  if (!key) return '—'
  if (/[•*]/.test(key)) return key
  if (key.length <= 8) return `${key.slice(0, 2)}••••`
  return `${key.slice(0, 4)}••••••${key.slice(-4)}`
}

function isExpired(record: StatusFields) {
  return record.status === 3 || (record.expired_time > 0 && record.expired_time * 1000 <= Date.now())
}

function TokenStatus({ record }: { record: StatusFields }) {
  if (isExpired(record)) return <Badge variant="warning">已过期</Badge>
  if (record.status === 1) return <Badge variant="success">启用</Badge>
  if (record.status === 4) return <Badge variant="warning">额度用尽</Badge>
  return <Badge variant="destructive">{record.status === 2 ? '禁用' : '不可用'}</Badge>
}

function RiskBadges({ record }: { record: TokenRecord }) {
  return <span className="tm-risks">
    {!record.subnet && <Badge variant="outline" className="text-xs font-normal gap-1 border-orange-200 text-orange-600 dark:border-orange-900" title="未设置 IP 白名单"><Globe size={12} />无 IP 限制</Badge>}
    {record.unlimited_quota && <Badge variant="outline" className="text-xs font-normal gap-1 border-blue-200 text-blue-600 dark:border-blue-900" title="无限额度"><InfinityIcon size={12} />无限额度</Badge>}
    {record.expired_time <= 0 && <Badge variant="outline" className="text-xs font-normal gap-1" title="永不过期"><Clock3 size={12} />永久</Badge>}
  </span>
}

function changeStatus(action: 'enable' | 'disable', ids: number[]) {
  return request<ChangeCount>(`/api/tokens/batch-${action}`, {
    method: 'POST',
    body: JSON.stringify({ ids }),
  })
}

export default function TokenManagement({
  tokenVersion = 0,
  quotaPerUnit = 500000,
  onUnauthorized,
}: {
  tokenVersion?: number
  quotaPerUnit?: number
  onUnauthorized: () => void
}) {
  const { openUser } = useIPDetails()
  const [initial] = useState(initialTokenQuery)
  const [draft, setDraft] = useState(initial.filters)
  const [filters, setFilters] = useState(initial.filters)
  const [page, setPage] = useState(1)
  const [tokens, setTokens] = useState<TokenRecord[]>([])
  const [statistics, setStatistics] = useState<Statistics | null>(null)
  const [groups, setGroups] = useState<TokenGroup[]>([])
  const [total, setTotal] = useState(0)
  const [totalPages, setTotalPages] = useState(0)
  const [ipCounts, setIpCounts] = useState<Map<number, number>>(new Map())
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [operating, setOperating] = useState(false)
  const [error, setError] = useState('')
  const [ipError, setIPError] = useState('')
  const [message, setMessage] = useState('')
  const [showLookup, setShowLookup] = useState(false)
  const [analysis, setAnalysis] = useState<TokenTarget | null>(initial.target)
  const [refreshVersion, setRefreshVersion] = useState(0)
  const [lookupDraft, setLookupDraft] = useState('')
  const [confirmation, setConfirmation] = useState<{ action: 'enable' | 'disable'; ids: number[] } | null>(null)
  const currentSession = useRef(tokenVersion)

  const handleError = useCallback((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 401) {
      onUnauthorized()
      return '管理员令牌已失效，请重新验证。'
    }
    return cause instanceof Error ? cause.message : '读取令牌数据失败。'
  }, [onUnauthorized])

  useEffect(() => {
    const controller = new AbortController()
    const load = async () => {
      if (UI_PREVIEW_ONLY) { setLoading(false); return }
      setLoading(true)
      setError('')
      setIPError('')
      setIpCounts(new Map())
      setSelected(new Set())
      const params = new URLSearchParams({ page: String(page), page_size: String(PAGE_SIZE) })
      for (const [key, value] of Object.entries(filters)) {
        if (value.trim()) params.set(key, value.trim())
      }
      try {
        const leakParams = new URLSearchParams(params)
        for (const key of ['page', 'page_size', 'risk']) leakParams.delete(key)
        leakParams.set('hours', '24')
        leakParams.set('min_ips', '5')
        leakParams.set('limit', '100')
        const [stats, tokenGroups, result] = await Promise.all([
          request<Statistics>('/api/tokens/statistics', {}, controller.signal),
          request<TokenGroup[]>('/api/tokens/groups', {}, controller.signal),
          filters.risk === 'leak'
            ? request<TokenRecord[]>(`/api/tokens/suspected-leaks?${leakParams}`, {}, controller.signal)
            : request<TokenPage>(`/api/tokens?${params}`, {}, controller.signal),
        ])
        if (controller.signal.aborted) return
        setStatistics(stats)
        setGroups(tokenGroups ?? [])

        if (Array.isArray(result)) {
          setTokens(result)
          setTotal(result.length)
          setTotalPages(1)
          setIpCounts(new Map(result.map(item => [item.id, Number(item.ip_count) || 0])))
          return
        }

        if (result.total_pages > 0 && page > result.total_pages) {
          setPage(result.total_pages)
          return
        }
        setTokens(result.items ?? [])
        setTotal(result.total ?? 0)
        setTotalPages(result.total_pages ?? 0)
        const ids = (result.items ?? []).map(item => item.id)
        if (!ids.length) return
        try {
          const ipStats = await request<{ token_id: number; ip_count: number }[]>(
            `/api/tokens/ip-stats?hours=24&ids=${ids.join(',')}`, {}, controller.signal,
          )
          if (controller.signal.aborted) return
          const counts = new Map(ids.map(id => [id, 0]))
          for (const item of ipStats ?? []) counts.set(item.token_id, item.ip_count)
          setIpCounts(counts)
        } catch (cause) {
          if (!controller.signal.aborted) setIPError(handleError(cause))
        }
      } catch (cause) {
        if (!controller.signal.aborted) {
          setTokens([])
          setTotal(0)
          setTotalPages(0)
          setStatistics(null)
          setGroups([])
          setError(handleError(cause))
        }
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false)
          setRefreshing(false)
        }
      }
    }
    void load()
    return () => controller.abort()
  }, [filters, page, refreshVersion, tokenVersion, handleError])

  useEffect(() => {
    const sessionChanged = currentSession.current !== tokenVersion
    currentSession.current = tokenVersion
    setShowLookup(false)
    if (sessionChanged) setAnalysis(null)
    setConfirmation(null)
    setLookupDraft('')
    setTokens([])
    setStatistics(null)
    setMessage('')
    setError('')
    setOperating(false)
  }, [tokenVersion])

  const selectedCount = selected.size
  const pageIds = useMemo(() => tokens.map(token => token.id), [tokens])
  const disableIds = tokens.filter(token => selected.has(token.id) && token.status === 1).map(token => token.id)
  const enableIds = tokens.filter(token => selected.has(token.id) && token.status === 2 && !isExpired(token)).map(token => token.id)

  function apply(next = draft) {
    setDraft(next)
    setPage(1)
    setSelected(new Set())
    setFilters({ ...next })
  }

  function refresh() {
    setRefreshing(true)
    setRefreshVersion(version => version + 1)
  }

  function toggleToken(id: number) {
    setSelected(previous => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function togglePage() {
    const allSelected = pageIds.length > 0 && pageIds.every(id => selected.has(id))
    setSelected(previous => {
      const next = new Set(previous)
      for (const id of pageIds) allSelected ? next.delete(id) : next.add(id)
      return next
    })
  }

  async function batchOperate(action: 'enable' | 'disable', ids: number[]) {
    if (UI_PREVIEW_ONLY || !ids.length || operating) return
    const verb = action === 'disable' ? '禁用' : '启用'
    const session = currentSession.current
    setOperating(true)
    setError('')
    setMessage('')
    try {
      const result = await changeStatus(action, ids)
      if (session !== currentSession.current) return
      const changed = action === 'enable' ? result.enabled : result.disabled
      setSelected(new Set())
      refresh()
      setMessage(`已${verb} ${numberFormat.format(changed ?? 0)} 个令牌。`)
      setConfirmation(null)
    } catch (cause) {
      if (session === currentSession.current) setError(handleError(cause))
    } finally {
      if (session === currentSession.current) setOperating(false)
    }
  }

  function selectStatus(status: string) {
    apply({ ...filters, status, risk: '' })
  }

  const allPageSelected = pageIds.length > 0 && pageIds.every(id => selected.has(id))

  return (
    <div className="token-management space-y-6 animate-in fade-in duration-500">
      <section className="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
        <div><h1 className="text-3xl font-bold">令牌管理</h1><p className="text-muted-foreground mt-1">查看所有令牌的状态与使用情况</p></div>
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="outline" size="sm" type="button" className="h-9" onClick={() => setShowLookup(true)}><KeyRound className="h-4 w-4 mr-2" />按 Key 查找</Button>
          <Button variant="outline" size="sm" type="button" className="h-9" onClick={refresh} disabled={UI_PREVIEW_ONLY || loading || refreshing}>
            <RefreshCw className={cn('h-4 w-4 mr-2', refreshing && 'animate-spin')} />刷新
          </Button>
        </div>
      </section>

      {error && <Notice text={error} onDismiss={() => setError('')} />}
      {message && <Notice text={message} success onDismiss={() => setMessage('')} />}
      {ipError && <Notice text={`读取来源 IP 统计失败。 ${ipError}`} onDismiss={() => setIPError('')} />}

      <section className="grid grid-cols-2 md:grid-cols-4 gap-4" aria-label="令牌统计">
        <StatCard title="总令牌" value={statistics ? numberFormat.format(statistics.total) : '—'} icon={KeyRound} color="blue" className={cn('border-l-4 border-l-blue-500', filters.status === '' && 'ring-1 ring-primary/30')} onClick={() => selectStatus('')} />
        <StatCard title="活跃令牌" value={statistics ? numberFormat.format(statistics.active) : '—'} icon={ShieldCheck} color="green" className={cn('border-l-4 border-l-green-500', filters.status === 'active' && 'ring-1 ring-primary/30')} onClick={() => selectStatus('active')} />
        <StatCard title="禁用令牌" value={statistics ? numberFormat.format(statistics.disabled) : '—'} icon={ShieldBan} color="red" className={cn('border-l-4 border-l-red-500', filters.status === 'disabled' && 'ring-1 ring-primary/30')} onClick={() => selectStatus('disabled')} />
        <StatCard title="已过期" value={statistics ? numberFormat.format(statistics.expired) : '—'} icon={Clock3} color="yellow" className={cn('border-l-4 border-l-yellow-500', filters.status === 'expired' && 'ring-1 ring-primary/30')} onClick={() => selectStatus('expired')} />
      </section>

      <Card>
        <CardHeader className="pb-3"><CardTitle id="tm-filter-title" className="text-base font-medium flex items-center gap-2"><Filter className="w-4 h-4" />筛选条件{filters.risk === 'leak' && <Badge variant="warning" className="ml-2">24 小时疑似泄漏</Badge>}</CardTitle></CardHeader>
        <CardContent>
          <div className="mb-4 space-y-1"><label htmlFor="tm-key-search" className="text-xs font-medium text-muted-foreground">令牌 Key 精确查找</label>
            <div className="flex gap-2"><div className="relative flex-1 min-w-0"><KeyRound className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" /><Input id="tm-key-search" value={lookupDraft} onChange={event => setLookupDraft(event.target.value)} placeholder="粘贴完整令牌 Key，查找所属用户..." className="pl-9 font-mono" spellCheck={false} autoComplete="off" /></div><Button variant="outline" type="button" onClick={() => setShowLookup(true)}><Search className="h-4 w-4 sm:mr-2" /><span className="hidden sm:inline">查找</span></Button></div>
          </div>
          <form onSubmit={event => { event.preventDefault(); if (!UI_PREVIEW_ONLY) apply() }}>
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-4">
              <label className="tm-field"><span>名称搜索</span><div className="relative"><Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" /><Input aria-label="令牌名称" value={draft.name} placeholder="搜索令牌名称..." className="pl-9" onChange={event => setDraft({ ...draft, name: event.target.value })} /></div></label>
              <label className="tm-field"><span>状态</span><Select aria-label="令牌状态" value={draft.status} onChange={event => apply({ ...draft, status: event.target.value })}><option value="">全部状态</option><option value="active">启用</option><option value="disabled">禁用</option><option value="expired">已过期</option></Select></label>
              <label className="tm-field"><span>令牌分组</span><Select aria-label="令牌分组" value={draft.group} onChange={event => apply({ ...draft, group: event.target.value })}><option value="">全部分组</option>{groups.map(group => <option key={group.group_name} value={group.group_name}>{group.group_name} ({group.token_count})</option>)}</Select></label>
              <label className="tm-field"><span>安全风险</span><Select aria-label="令牌安全风险" value={draft.risk} onChange={event => apply({ ...draft, risk: event.target.value })}><option value="">全部令牌</option><option value="leak">疑似泄漏（24h 多 IP）</option><option value="no_ip_limit">无 IP 白名单</option><option value="unlimited">无限额度</option><option value="never_expire">永不过期</option></Select></label>
              <label className="tm-field"><span>用户 ID</span><Input aria-label="令牌所属用户 ID" inputMode="numeric" value={draft.user_id} placeholder="全部用户" onChange={event => setDraft({ ...draft, user_id: event.target.value.replace(/\D/g, '') })} /></label>
            </div>
            <div className="flex justify-end gap-2 mt-4"><Button variant="ghost" size="sm" type="button" className="text-muted-foreground" onClick={() => apply({ ...defaultFilters })}>重置筛选</Button><Button size="sm" type="submit" disabled={UI_PREVIEW_ONLY || loading}><Search className="h-4 w-4 mr-2" />查询</Button></div>
          </form>
        </CardContent>
      </Card>

      {selectedCount > 0 && <div className="sticky top-2 z-20 flex flex-wrap items-center gap-3 px-4 py-2.5 rounded-lg border bg-background/95 backdrop-blur shadow-md">
        <span className="text-sm">已选 <strong>{selectedCount}</strong> 个令牌</span><div className="flex-1" />
        <Button variant="outline" size="sm" type="button" disabled={loading || operating || !enableIds.length} onClick={() => setConfirmation({ action: 'enable', ids: enableIds })}><ShieldCheck className="h-3.5 w-3.5 mr-1.5" />启用所选</Button>
        <Button variant="destructive" size="sm" type="button" disabled={loading || operating || !disableIds.length} onClick={() => setConfirmation({ action: 'disable', ids: disableIds })}><ShieldBan className="h-3.5 w-3.5 mr-1.5" />禁用所选</Button>
        <Button variant="ghost" size="sm" type="button" onClick={() => setSelected(new Set())}>取消</Button>
      </div>}
      <Card className="min-w-0" aria-labelledby="tm-table-title" aria-busy={loading}>
        <CardHeader className="pb-3"><CardTitle id="tm-table-title" className="text-base font-medium flex items-center justify-between gap-3"><span>令牌列表</span><span className="text-sm text-muted-foreground font-normal">共 {UI_PREVIEW_ONLY || error || loading && !tokens.length ? '—' : numberFormat.format(total)} 个</span></CardTitle></CardHeader>
        <CardContent className="p-0">
          <div role="region" tabIndex={0} aria-label="令牌列表，可横向滚动">
          <Table className="tm-table">
            <TableHeader><TableRow><TableHead className="w-10"><input type="checkbox" className="accent-primary h-4 w-4" aria-label="选择当前页全部令牌" checked={allPageSelected} disabled={UI_PREVIEW_ONLY || loading || operating || !tokens.length} onChange={togglePage} /></TableHead><TableHead>令牌</TableHead><TableHead>用户</TableHead><TableHead>分组</TableHead><TableHead>状态 / 风险</TableHead><TableHead>额度</TableHead><TableHead>24h IP</TableHead><TableHead>创建时间</TableHead><TableHead>最近使用</TableHead><TableHead>过期时间</TableHead><TableHead>操作</TableHead></TableRow></TableHeader>
            <TableBody>{!tokens.length ? <TableRow><TableCell colSpan={11} className="h-48 text-center text-muted-foreground">{loading ? <span className="inline-flex items-center gap-2"><RefreshCw className="h-5 w-5 animate-spin" />正在读取令牌数据…</span> : UI_PREVIEW_ONLY ? '数据获取已暂停' : error ? '令牌数据暂不可用' : '暂无符合筛选条件的令牌'}</TableCell></TableRow> : tokens.map(record => {
              const ipCount = ipCounts.get(record.id) ?? record.ip_count
              const action = record.status === 1 ? 'disable' : record.status === 2 && !isExpired(record) ? 'enable' : null
              return <TableRow key={record.id} data-state={selected.has(record.id) ? 'selected' : undefined}>
                <TableCell><input type="checkbox" className="accent-primary h-4 w-4" aria-label={`选择令牌 ${record.name || record.id}`} checked={selected.has(record.id)} disabled={loading || operating} onChange={() => toggleToken(record.id)} /></TableCell>
                <TableCell><div className="tm-token-name">
                  <Button variant="link" type="button" className="h-auto p-0 justify-start" title="查看令牌分析" onClick={() => setAnalysis(record)}>{record.name || `令牌 #${record.id}`}</Button>
                  <code title="令牌 Key 已隐藏">{maskKey(record.key)}</code>
                </div></TableCell>
                <TableCell><div className="tm-user">
                  <UserIdentityButton userId={record.user_id} username={record.username} compact onClick={() => openUser(record.user_id, record.username)} />
                  <small>ID {record.user_id}</small>
                </div></TableCell>
                <TableCell><Badge variant="outline">{record.group || 'default'}</Badge></TableCell>
                <TableCell><div className="tm-state-cell"><TokenStatus record={record} /><RiskBadges record={record} /></div></TableCell>
                <TableCell><div className="tm-quota"><strong>{record.unlimited_quota ? '无限' : formatQuota(record.remain_quota, quotaPerUnit)}</strong><small>已用 {formatQuota(record.used_quota, quotaPerUnit)}</small></div></TableCell>
                <TableCell><Badge variant={(ipCount ?? 0) >= 5 ? 'warning' : 'secondary'} title="最近 24 小时不同来源 IP 数">{ipCount ?? '—'}</Badge></TableCell>
                <TableCell className="text-xs whitespace-nowrap text-muted-foreground">{formatTime(record.created_time)}</TableCell><TableCell className="text-xs whitespace-nowrap text-muted-foreground">{formatTime(record.accessed_time)}</TableCell><TableCell className="text-xs whitespace-nowrap text-muted-foreground">{record.expired_time > 0 ? formatTime(record.expired_time) : '永不过期'}</TableCell>
                <TableCell>{action ? <Button variant="ghost" size="sm" type="button" className={action === 'disable' ? 'text-destructive hover:text-destructive' : 'text-emerald-600'} disabled={loading || operating} onClick={() => setConfirmation({ action, ids: [record.id] })}>{action === 'disable' ? '禁用' : '启用'}</Button> : '—'}</TableCell>
              </TableRow>
            })}</TableBody>
          </Table>
          </div>
          <div className="tm-pagination"><span>第 {UI_PREVIEW_ONLY ? '—' : page} / {UI_PREVIEW_ONLY ? '—' : Math.max(totalPages, 1)} 页</span><div><Button variant="outline" size="sm" type="button" className="h-8 w-8 p-0" aria-label="上一页" disabled={UI_PREVIEW_ONLY || loading || page <= 1 || filters.risk === 'leak'} onClick={() => setPage(current => current - 1)}><ChevronLeft size={16} /></Button><Button variant="outline" size="sm" type="button" className="h-8 w-8 p-0" aria-label="下一页" disabled={UI_PREVIEW_ONLY || loading || page >= totalPages || filters.risk === 'leak'} onClick={() => setPage(current => current + 1)}><ChevronRight size={16} /></Button></div></div>
        </CardContent>
      </Card>
      {showLookup && <LookupDialog
        handleError={handleError}
        initialInput={lookupDraft}
        quotaPerUnit={quotaPerUnit}
        onClose={() => { setShowLookup(false); setLookupDraft('') }}
        onChanged={refresh}
        onAnalyze={target => { setShowLookup(false); setLookupDraft(''); setAnalysis(target) }}
      />}
      {confirmation && <AdminDialog title={confirmation.action === 'disable' ? '确认禁用令牌' : '确认启用令牌'} onClose={() => { if (!operating) setConfirmation(null) }}>
        <p className="text-sm text-muted-foreground leading-relaxed">将{confirmation.action === 'disable' ? '禁用' : '启用'}所选的 <strong className="text-foreground">{confirmation.ids.length}</strong> 个令牌。{confirmation.action === 'disable' ? '禁用后对应令牌将无法继续调用。' : '已过期或额度耗尽的令牌不会被恢复。'}</p>
        {error && <Notice text={error} />}
        <div className="tm-dialog-actions"><Button variant="outline" type="button" disabled={operating} onClick={() => setConfirmation(null)}>取消</Button><Button variant={confirmation.action === 'disable' ? 'destructive' : 'default'} type="button" disabled={operating || UI_PREVIEW_ONLY} onClick={() => void batchOperate(confirmation.action, confirmation.ids)}>{operating ? <RefreshCw className="h-4 w-4 mr-2 animate-spin" /> : <ShieldCheck className="h-4 w-4 mr-2" />}确认{confirmation.action === 'disable' ? '禁用' : '启用'}</Button></div>
      </AdminDialog>}
      {analysis && <TokenAnalysisDialog
        key={analysis.id}
        target={analysis}
        tokenVersion={tokenVersion}
        handleError={handleError}
        quotaPerUnit={quotaPerUnit}
        onClose={() => setAnalysis(null)}
        onChanged={refresh}
        initialWindow={initial.window}
        initialRange={analysis.id === initial.target?.id ? initial.range : undefined}
        onViewUser={id => {
          setAnalysis(null)
          openUser(id)
        }}
      />}
    </div>
  )
}

function Notice({ text, success, onDismiss }: { text: string; success?: boolean; onDismiss?: () => void }) {
  return <div className={cn('tm-notice', success ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300' : 'border-destructive/20 bg-destructive/5 text-destructive')} role={success ? 'status' : 'alert'}>
    {success ? <Check size={15} /> : <AlertTriangle size={15} />}
    <span>{text}</span>
    {onDismiss && <Button variant="ghost" size="sm" type="button" className="h-7 w-7 p-0 ml-auto" aria-label="关闭提示" onClick={onDismiss}><X size={15} /></Button>}
  </div>
}

function TokenDialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  return <AdminDialog title={title} onClose={onClose} wide><div className="tm-dialog">{children}</div></AdminDialog>
}

type LookupItem = {
  key_masked: string
  found: boolean
  id?: number
  name?: string
  username?: string
  user_id?: number
  status?: number
  expired_time?: number
  group?: string
  used_quota?: number
}
type LookupResult = { items: LookupItem[]; found: number; not_found: number }

function LookupDialog({ handleError, quotaPerUnit, onClose, onChanged, onAnalyze, initialInput = '' }: {
  handleError: (error: unknown) => string
  quotaPerUnit: number
  onClose: () => void
  onChanged: () => void
  onAnalyze: (target: TokenTarget) => void
  initialInput?: string
}) {
  const { openUser } = useIPDetails()
  const [input, setInput] = useState(initialInput)
  const [result, setResult] = useState<LookupResult | null>(null)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [disableConfirm, setDisableConfirm] = useState(false)
  const keys = useMemo(() => Array.from(new Set(input.split(/[;,，；\s]+/).filter(Boolean))), [input])
  const disableableIds = (result?.items ?? []).filter(item => item.found && item.id && item.status === 1).map(item => item.id!)

  async function lookup() {
    if (UI_PREVIEW_ONLY || busy || !keys.length) return
    setBusy(true)
    setError('')
    try {
      const reply = await request<LookupResult>('/api/tokens/lookup', {
        method: 'POST',
        body: JSON.stringify({ keys }),
      })
      setInput('')
      setResult(reply)
      setSelected(new Set())
    } catch (cause) {
      setError(handleError(cause))
    } finally {
      setBusy(false)
    }
  }

  async function disable() {
    if (UI_PREVIEW_ONLY || !result || !selected.size || busy) return
    if (!disableConfirm) { setDisableConfirm(true); return }
    const ids = Array.from(selected)
    setBusy(true)
    setError('')
    setMessage('')
    try {
      const reply = await changeStatus('disable', ids)
      const disabled = reply.disabled ?? 0
      setMessage(`已禁用 ${numberFormat.format(disabled)} 个令牌。`)
      if (disabled === ids.length) {
        setResult({ ...result, items: result.items.map(item => item.id && selected.has(item.id) ? { ...item, status: 2 } : item) })
      } else {
        setError('部分匹配结果的状态已变化，重新查找可查看最新状态。')
      }
      setSelected(new Set())
      setDisableConfirm(false)
      onChanged()
    } catch (cause) {
      setError(handleError(cause))
    } finally {
      setBusy(false)
    }
  }

  function toggle(id: number, checked: boolean) {
    setDisableConfirm(false)
    setSelected(previous => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  return <TokenDialog title="按 Key 查找令牌" onClose={() => { if (!busy) onClose() }}>
    {error && <Notice text={error} />}
    {message && <Notice text={message} success />}
    {!result ? <form onSubmit={event => { event.preventDefault(); void lookup() }}>
      <p className="text-sm text-muted-foreground leading-relaxed">粘贴一个或多个完整 Key，用换行、空格或逗号分隔。匹配完成后可查看详情或勾选并禁用令牌。</p>
      {UI_PREVIEW_ONLY && <p className="text-xs text-muted-foreground mt-3">当前仅预览输入与操作界面，查找和禁用暂未启用。</p>}
      <label className="tm-field mt-4">
        <span>令牌 Key</span>
        <textarea aria-label="待查找的令牌 Key" className="tm-textarea font-mono" value={input} onChange={event => setInput(event.target.value)} spellCheck={false} autoComplete="off" rows={7} disabled={busy} placeholder={'sk-…\nsk-…'} />
      </label>
      <footer className="tm-dialog-actions">
        <span className="text-sm text-muted-foreground mr-auto">{numberFormat.format(keys.length)} 个 Key</span>
        <Button variant="outline" type="button" disabled={busy} onClick={onClose}>取消</Button>
        <Button type="submit" disabled={UI_PREVIEW_ONLY || busy || !keys.length}><Search className="h-4 w-4 mr-2" />{busy ? '查找中…' : '查找'}</Button>
      </footer>
    </form> : <>
      <p>匹配 {result.found} 个，未匹配 {result.not_found} 个。</p>
      <div className="tm-detail-table" role="region" tabIndex={0} aria-label="令牌匹配结果">
        <Table>
          <TableHeader><TableRow>
            <TableHead className="w-10"><input type="checkbox" className="h-4 w-4 accent-primary" aria-label="选择全部可禁用令牌" checked={disableableIds.length > 0 && disableableIds.every(id => selected.has(id))} disabled={busy || !disableableIds.length} onChange={event => { setDisableConfirm(false); setSelected(new Set(event.target.checked ? disableableIds : [])) }} /></TableHead>
            <TableHead>Key</TableHead><TableHead>令牌</TableHead><TableHead>所属用户</TableHead><TableHead>分组</TableHead><TableHead>已用额度</TableHead><TableHead>状态</TableHead>
          </TableRow></TableHeader>
          <TableBody>{result.items.map((item, index) => <TableRow key={item.id ?? `missing-${index}`}>
            <TableCell>{item.id != null && item.status === 1 && <input type="checkbox" className="h-4 w-4 accent-primary" aria-label={`选择令牌 ${item.name || item.id}`} disabled={busy} checked={selected.has(item.id)} onChange={event => toggle(item.id!, event.target.checked)} />}</TableCell>
            <TableCell><code>{maskKey(item.key_masked)}</code></TableCell>
            <TableCell>{item.id ? <Button variant="link" type="button" className="h-auto p-0" disabled={busy} onClick={() => onAnalyze({ id: item.id!, name: item.name || '' })}>{item.name || `#${item.id}`}</Button> : '—'}</TableCell>
            <TableCell>{item.user_id ? <UserIdentityButton userId={item.user_id} username={item.username} compact onClick={() => { onClose(); openUser(item.user_id!, item.username) }} /> : item.username || '—'}</TableCell><TableCell><Badge variant="outline">{item.group || '—'}</Badge></TableCell>
            <TableCell className="font-mono text-xs">{item.found ? formatQuota(item.used_quota ?? 0, quotaPerUnit) : '—'}</TableCell>
            <TableCell>{item.status == null ? '未匹配' : <TokenStatus record={{ status: item.status, expired_time: item.expired_time ?? 0 }} />}</TableCell>
          </TableRow>)}</TableBody>
        </Table>
      </div>
      <footer className="tm-dialog-actions">
        <Button variant="outline" type="button" disabled={busy} onClick={() => { setResult(null); setSelected(new Set()); setDisableConfirm(false); setError(''); setMessage('') }}>重新查找</Button>
        <Button variant="destructive" type="button" disabled={UI_PREVIEW_ONLY || busy || !selected.size} onClick={() => void disable()}><ShieldBan className="h-4 w-4 mr-2" />{busy ? '禁用中…' : disableConfirm ? `确认禁用 ${selected.size} 个？` : `禁用所选（${selected.size}）`}</Button>
      </footer>
    </>}
  </TokenDialog>
}

export type TokenWindow = '1h' | '3h' | '6h' | '12h' | '24h' | '3d' | '7d' | '30d' | '90d'
export type TokenRange = { start: number; end: number }
type AnalysisData = {
  range: { start_time: number; end_time: number; window_seconds: number }
  token: TokenTarget & StatusFields & {
    key: string; user_id: number; username: string; group: string; remain_quota: number; used_quota: number
    unlimited_quota: boolean; allow_ips?: string; model_limits?: string; created_time: number
  }
  summary: {
    total_requests: number; success_requests: number; failure_requests: number; quota_used: number
    prompt_tokens: number; completion_tokens: number; avg_use_time: number; unique_ips: number | null
    unique_models: number; empty_count?: number; failure_rate: number; empty_rate?: number
  }
  risk: { requests_per_minute: number; risk_flags: string[] }
  top_models: { model_name: string; requests: number; quota_used: number; failure_requests: number }[]
  top_ips: { ip: string; requests: number; first_seen: number; last_seen: number; geo_label?: string }[]
  hourly: { slot_idx: number; requests: number; quota_used: number }[]
  recent_logs: { id: number; created_at: number; type: number; model_name: string; quota: number; prompt_tokens: number; completion_tokens: number; use_time: number; ip: string }[]
  network_statistics?: {
    available: boolean; sync_complete: boolean; history_complete: boolean; source: string
    window_start: number; window_end: number; note: string; warnings: string[]
  }
}

const analysisWindows: { value: TokenWindow; label: string }[] = [
  { value: '1h', label: '近 1 小时' },
  { value: '3h', label: '近 3 小时' },
  { value: '6h', label: '近 6 小时' },
  { value: '12h', label: '近 12 小时' },
  { value: '24h', label: '近 24 小时' },
  { value: '3d', label: '近 3 天' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
  { value: '90d', label: '近 90 天' },
]
function tokenWindow(value?: string): TokenWindow {
  if (value == null) return '24h'
  return analysisWindows.some(item => item.value === value) ? value as TokenWindow : '7d'
}
function tokenRange(value?: TokenRange): TokenRange | undefined {
  return value && Number.isSafeInteger(value.start) && Number.isSafeInteger(value.end) &&
    value.start >= 0 && value.end > value.start && value.end - value.start <= 366 * 86400
    ? { start: value.start, end: value.end } : undefined
}
const riskLabels: Record<string, string> = {
  HIGH_RPM: '高频请求',
  HIGH_FAILURE_RATE: '高失败率',
  LEAK_SUSPECT: '疑似泄漏',
  MANY_IPS: '多 IP',
  CROSS_CITY_SWITCH: '跨城切换',
  CROSS_COUNTRY: '跨国来源',
  FREQUENT_IP_SWITCH: '频繁切换 IP',
}

export function TokenAnalysisDialog({ target, tokenVersion, handleError, quotaPerUnit, onClose, onChanged, onViewUser, initialWindow, initialRange }: {
  target: TokenTarget
  tokenVersion: number
  handleError: (error: unknown) => string
  quotaPerUnit: number
  onClose: () => void
  onChanged: () => void
  onViewUser: (id: number) => void
  initialWindow?: TokenWindow
  initialRange?: TokenRange
}) {
  const [duration, setDuration] = useState<TokenWindow>(() => tokenWindow(initialWindow))
  const [exactRange, setExactRange] = useState<TokenRange | undefined>(() => tokenRange(initialRange))
  const [version, setVersion] = useState(0)
  const [data, setData] = useState<AnalysisData | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [confirmOperation, setConfirmOperation] = useState(false)

  useEffect(() => {
    setDuration(tokenWindow(initialWindow))
    setExactRange(tokenRange(initialRange))
  }, [target.id, initialWindow, initialRange?.start, initialRange?.end])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) { setLoading(false); return }
    const controller = new AbortController()
    setLoading(true)
    setData(null)
    setError('')
    setConfirmOperation(false)
    const params = new URLSearchParams({ window: duration })
    if (exactRange) {
      params.set('start_time', String(exactRange.start))
      params.set('end_time', String(exactRange.end - 1))
    }
    request<AnalysisData>(`/api/tokens/${target.id}/analysis?${params}`, {}, controller.signal)
      .then(result => { if (!controller.signal.aborted) setData(result) })
      .catch(cause => { if (!controller.signal.aborted) setError(handleError(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [target.id, duration, exactRange?.start, exactRange?.end, version, tokenVersion, handleError])

  const token = data?.token
  const action = token?.status === 1 ? 'disable' : token?.status === 2 && !isExpired(token) ? 'enable' : null

  async function operate() {
    if (UI_PREVIEW_ONLY || !action || busy) return
    const verb = action === 'disable' ? '禁用' : '启用'
    if (!confirmOperation) { setConfirmOperation(true); return }
    setBusy(true)
    setError('')
    setMessage('')
    try {
      const result = await changeStatus(action, [target.id])
      const changed = action === 'disable' ? result.disabled : result.enabled
      setMessage(`已${verb} ${numberFormat.format(changed ?? 0)} 个令牌。`)
      setConfirmOperation(false)
      onChanged()
      setVersion(value => value + 1)
    } catch (cause) {
      setError(handleError(cause))
    } finally {
      setBusy(false)
    }
  }

  return <TokenDialog title={`令牌分析：${target.name || token?.name || `#${target.id}`}`} onClose={() => { if (!busy) onClose() }}>
    <div className="tm-analysis-controls">
      <label className="tm-field"><span>{exactRange ? '时间范围 · 已对齐来源窗口' : '时间范围'}</span><Select aria-label="令牌分析时间范围" value={duration} disabled={busy} onChange={event => { setExactRange(undefined); setDuration(tokenWindow(event.target.value)) }}>
        {analysisWindows.map(item => <option key={item.value} value={item.value}>{item.label}</option>)}
      </Select></label>
      <Button variant="outline" size="sm" type="button" disabled={UI_PREVIEW_ONLY || busy || loading} onClick={() => setVersion(value => value + 1)}><RefreshCw className="h-4 w-4 mr-2" />刷新</Button>
      {action && <Button variant={action === 'disable' ? 'destructive' : 'default'} size="sm" type="button" disabled={UI_PREVIEW_ONLY || busy || loading} onClick={() => void operate()}>{confirmOperation ? `确认${action === 'disable' ? '禁用' : '启用'}？` : action === 'disable' ? '禁用令牌' : '启用令牌'}</Button>}
      {confirmOperation && <Button variant="ghost" size="sm" type="button" disabled={busy} onClick={() => setConfirmOperation(false)}>取消</Button>}
    </div>
    {error && <Notice text={error} />}
    {message && <Notice text={message} success />}
    {loading ? <div className="tm-state" role="status">正在读取令牌分析…</div> : data && <AnalysisContent data={data} unit={quotaPerUnit} onViewUser={onViewUser} />}
  </TokenDialog>
}

function AnalysisContent({ data, unit, onViewUser }: { data: AnalysisData; unit: number; onViewUser: (id: number) => void }) {
  const { openIP } = useIPDetails()
  const { token, summary, risk, range } = data
  const network = data.network_statistics
  const inspectIP = (ip: string) => {
    openIP({
      ip, source: 'api_call', userId: token.user_id, tokenId: token.id,
      start: network?.window_start ?? range.start_time,
      end: network?.window_end ?? range.end_time + 1,
      period: Object.entries({ '1h': 3600, '3h': 10800, '6h': 21600, '12h': 43200, '24h': 86400, '3d': 259200, '7d': 604800, '30d': 2592000, '90d': 7776000 }).find(([, seconds]) => seconds >= range.window_seconds)?.[0] ?? '90d',
    })
  }
  const hourly = data.hourly ?? []
  const totalHours = Math.max(0, Math.ceil(range.window_seconds / 3600))
  const hoursPerSlot = Math.max(1, Math.ceil(totalHours / 168))
  const slotCount = Math.ceil(totalHours / hoursPerSlot)
  const slots = new Map<number, number>()
  for (const item of hourly) {
    if (item.slot_idx < 0 || item.slot_idx >= totalHours) continue
    const slot = Math.floor(item.slot_idx / hoursPerSlot)
    slots.set(slot, (slots.get(slot) ?? 0) + item.requests)
  }
  const maxRequests = Math.max(1, ...slots.values())
  const metric = (label: string, value: string | number) => <Card key={label} className="bg-muted/20"><CardContent className="p-4"><p className="text-xs text-muted-foreground">{label}</p><p className="text-lg font-semibold mt-2 break-words">{value}</p></CardContent></Card>
  const fraction = (value: number) => `${(value * 100).toFixed(1)}%`
  return <>
    <div className="tm-analysis-meta">
      <code>{maskKey(token.key)}</code>
      <TokenStatus record={token} />
      {token.group && <Badge variant="outline">{token.group}</Badge>}
      {token.user_id > 0 && <Button variant="link" size="sm" type="button" className="h-auto p-0" onClick={() => onViewUser(token.user_id)}>{token.username || `用户 #${token.user_id}`} 的令牌</Button>}
      <span>额度 {token.unlimited_quota ? '不限' : `剩余 ${formatQuota(token.remain_quota, unit)}`} · 已用 {formatQuota(token.used_quota, unit)}</span>
      <span>IP 白名单 {token.allow_ips || '未设置'}</span>
      <span>创建 {formatTime(token.created_time)}</span>
      <span>{token.expired_time > 0 ? `过期 ${formatTime(token.expired_time)}` : '永不过期'}</span>
    </div>
    <p className="tm-analysis-range">{formatTime(range.start_time)} 至 {formatTime(range.end_time)}</p>
    {(risk?.risk_flags?.length ?? 0) > 0 && <Notice text={risk.risk_flags.map(flag => riskLabels[flag] || flag).join(' · ')} />}
    <Tabs defaultValue="overview" className="mt-4">
    <TabsList className="grid w-full grid-cols-3"><TabsTrigger value="overview">使用概览</TabsTrigger><TabsTrigger value="sources">模型与来源</TabsTrigger><TabsTrigger value="logs">最近日志</TabsTrigger></TabsList>
    <TabsContent value="overview" className="mt-4 space-y-5">
    <div className="tm-analysis-metrics">
      {metric('总请求', numberFormat.format(summary.total_requests))}
      {metric('成功 / 失败', `${numberFormat.format(summary.success_requests)} / ${numberFormat.format(summary.failure_requests)}`)}
      {metric('失败率', fraction(summary.failure_rate))}
      {metric('消耗额度', formatQuota(summary.quota_used, unit))}
      {metric('平均耗时', `${(summary.avg_use_time || 0).toFixed(1)} 秒`)}
      {metric('唯一来源 IP', summary.unique_ips == null ? '—' : numberFormat.format(summary.unique_ips))}
      {metric('平均每分钟请求', numberFormat.format(risk?.requests_per_minute ?? 0))}
      {metric('输入 / 输出 Token', `${numberFormat.format(summary.prompt_tokens)} / ${numberFormat.format(summary.completion_tokens)}`)}
      {summary.empty_rate != null && metric('空回复率', fraction(summary.empty_rate))}
    </div>
    {slotCount > 0 && hourly.length > 0 && <section className="tm-analysis-section">
      <h3><Activity size={14} />{hoursPerSlot === 1 ? '每小时请求' : `请求趋势 · 每 ${hoursPerSlot} 小时`}</h3>
      <div className="tm-analysis-chart" role="img" aria-label={`每 ${hoursPerSlot} 小时请求量，最高 ${maxRequests} 次`}>
        {Array.from({ length: slotCount }, (_, slot) => {
          const requests = slots.get(slot) ?? 0
          return <span key={slot} className={requests ? 'has-requests' : ''} style={{ height: `${Math.max(4, requests / maxRequests * 100)}%` }} title={`${formatTime(range.start_time + slot * hoursPerSlot * 3600)} · ${requests} 次`} />
        })}
      </div>
    </section>}
    </TabsContent>
    <TabsContent value="sources" className="mt-4">
    {network?.note && <Notice text={network.note} />}
    {network?.warnings?.map(warning => <Notice key={warning} text={warning} />)}
    <div className="tm-analysis-tables">
      <AnalysisTable title="常用模型" columns={['模型', '请求', '失败', '消耗']}>
        {(data.top_models ?? []).map(item => <TableRow key={item.model_name}><TableCell>{item.model_name}</TableCell><TableCell>{item.requests}</TableCell><TableCell>{item.failure_requests}</TableCell><TableCell>{formatQuota(item.quota_used, unit)}</TableCell></TableRow>)}
      </AnalysisTable>
      <AnalysisTable title="请求来源 IP" columns={['IP', '请求', '首次出现', '最近出现']}>
        {(data.top_ips ?? []).map(item => <TableRow key={item.ip}><TableCell><button type="button" className="ip-inline-button" aria-label={`查看调用 IP ${item.ip} 的分析`} onClick={() => inspectIP(item.ip)}><code>{item.ip}</code></button>{item.geo_label ? ` · ${item.geo_label}` : ''}</TableCell><TableCell>{item.requests}</TableCell><TableCell>{formatTime(item.first_seen)}</TableCell><TableCell>{formatTime(item.last_seen)}</TableCell></TableRow>)}
      </AnalysisTable>
    </div>
    </TabsContent>
    <TabsContent value="logs" className="mt-4">
    <AnalysisTable title="最近请求日志" columns={['时间', '状态', '模型', '消耗', '耗时', '来源 IP']}>
      {(data.recent_logs ?? []).map(item => <TableRow key={item.id}>
        <TableCell>{formatTime(item.created_at)}</TableCell><TableCell>{item.type === 2 ? '成功' : item.type === 5 ? '失败' : item.type}</TableCell><TableCell>{item.model_name}</TableCell><TableCell>{formatQuota(item.quota, unit)}</TableCell><TableCell>{item.use_time} 秒</TableCell><TableCell>{item.ip ? <button type="button" className="ip-inline-button" aria-label={`查看调用 IP ${item.ip} 的分析`} onClick={() => inspectIP(item.ip)}><code>{item.ip}</code></button> : '—'}</TableCell>
      </TableRow>)}
    </AnalysisTable>
    </TabsContent>
    </Tabs>
  </>
}

function AnalysisTable({ title, columns, children }: { title: string; columns: string[]; children: ReactNode[] }) {
  return <Card className="min-w-0">
    <CardHeader className="pb-3"><CardTitle className="text-base font-medium">{title}</CardTitle></CardHeader>
    <CardContent className="p-0"><div className="tm-detail-table" role="region" tabIndex={0} aria-label={title}>
      <Table><TableHeader><TableRow>{columns.map(column => <TableHead key={column}>{column}</TableHead>)}</TableRow></TableHeader>
        <TableBody>{children.length ? children : <TableRow><TableCell colSpan={columns.length} className="h-24 text-center text-muted-foreground">暂无数据</TableCell></TableRow>}</TableBody>
      </Table>
    </div></CardContent>
  </Card>
}
