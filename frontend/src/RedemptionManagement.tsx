// Adapted from new_api_tools (Redemptions.tsx), MIT License; see tools-reference-LICENSE.txt.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useToast } from './components/Toast'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, Notice } from './ModuleUI'
import RiskUserCard from './RiskUserCard'
import { Trash2, Copy, Ticket, Loader2, RefreshCw, Filter, Search, Calendar, Tag, AlertCircle, CheckCircle2, XCircle } from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Button } from './components/ui/button'
import { Badge } from './components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ReferenceTable'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './components/ui/dialog'
import { Select } from './components/ui/select'
import { Input } from './components/ui/input'
import { StatCard } from './components/ReferenceStatCard'
import { cn } from './lib/utils'

interface RedemptionCode {
  id: number; key: string; name: string; quota: number; created_time: number; redeemed_time: number
  used_user_id: number; used_username: string; expired_time: number
  status: 'unused' | 'used' | 'expired' | 'disabled' | 'unknown'
}
interface RedemptionStatistics {
  total_count: number; unused_count: number; used_count: number; expired_count: number
  total_quota: number; unused_quota: number; used_quota: number; expired_quota: number
  unknown_count?: number; unknown_quota?: number
  disabled_count?: number; disabled_quota?: number
}
interface PaginatedResponse {
  items: RedemptionCode[]; total: number; page: number; page_size: number; total_pages: number; quota_per_unit?: number
}
type StatusFilter = '' | 'unused' | 'used' | 'expired'
const statusNames = { unused: '未使用', used: '已使用', expired: '已过期', disabled: '已禁用', unknown: '未知状态' }

function appendUserSearchParam(params: URLSearchParams, query: string) {
  const value = query.trim()
  if (value) params.set(/^\d+$/.test(value) ? 'user_id' : 'username', value)
}

export function RedemptionManagement({ tokenVersion = 0, quotaPerUnit = 500000, timezone = 'Asia/Shanghai', onUnauthorized }: {
  tokenVersion?: number; quotaPerUnit?: number; timezone?: string; onUnauthorized: () => void
}) {
  const { showToast } = useToast()
  const [codes, setCodes] = useState<RedemptionCode[]>([])
  const [statistics, setStatistics] = useState<RedemptionStatistics | null>(null)
  const [loading, setLoading] = useState(!UI_PREVIEW_ONLY)
  const [statsLoading, setStatsLoading] = useState(!UI_PREVIEW_ONLY)
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [page, setPage] = useState(1)
  const pageSize = 20
  const [total, setTotal] = useState(0)
  const [totalPages, setTotalPages] = useState(1)
  const [nameFilter, setNameFilter] = useState('')
  const [userFilter, setUserFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('')
  const [startDate, setStartDate] = useState('')
  const [endDate, setEndDate] = useState('')
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; type: 'single' | 'batch'; id?: number }>({ open: false, type: 'single' })
  const [deleting, setDeleting] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [analysisDialogOpen, setAnalysisDialogOpen] = useState(false)
  const [selectedUser, setSelectedUser] = useState<{ id: number; username: string } | null>(null)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const [unit, setUnit] = useState(quotaPerUnit)
  const serial = useRef(0)
  const deleteAbort = useRef<AbortController | null>(null)
  const deletePending = useRef(false)
  const currentSession = sessionStorage.getItem('siteadmin-token')
  const session = useRef(currentSession)
  session.current = currentSession
  const report = useCallback((cause: unknown) => {
    if (cause instanceof ApiError && cause.status === 401) { onUnauthorized(); return '管理员会话已失效，请重新连接。' }
    return cause instanceof Error ? cause.message : '暂时无法读取兑换码，请重试。'
  }, [onUnauthorized])

  useEffect(() => { setPage(1) }, [nameFilter, userFilter, statusFilter, startDate, endDate])
  useEffect(() => { setSelectedIds(new Set()) }, [page, nameFilter, userFilter, statusFilter, startDate, endDate, tokenVersion])
  useEffect(() => {
    deleteAbort.current?.abort()
    deletePending.current = false
    setCodes([])
    setStatistics(null)
    setTotal(0)
    setTotalPages(1)
    setSelectedIds(new Set())
    setUnit(quotaPerUnit)
    setDeleting(false)
    setDeleteDialog({ open: false, type: 'single' })
    setSelectedUser(null)
    setAnalysisDialogOpen(false)
    return () => { deleteAbort.current?.abort() }
  }, [tokenVersion, currentSession])

  useEffect(() => {
    const controller = new AbortController()
    const id = ++serial.current
    const live = () => !controller.signal.aborted && id === serial.current && session.current === currentSession
    setError('')
    if (UI_PREVIEW_ONLY) { setCodes([]); setStatistics(null); setLoading(false); setStatsLoading(false); return () => controller.abort() }
    if (startDate && endDate && startDate > endDate) {
      setError('开始日期不能晚于结束日期。'); setCodes([]); setStatistics(null); setLoading(false); setStatsLoading(false); setRefreshing(false)
      return () => controller.abort()
    }
    setLoading(true)
    setStatsLoading(true)
    const timer = window.setTimeout(() => {
      const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) })
      if (nameFilter.trim()) params.set('name', nameFilter.trim())
      appendUserSearchParam(params, userFilter)
      if (statusFilter) params.set('status', statusFilter)
      const dates = new URLSearchParams()
      if (startDate) { params.set('start_date', startDate); dates.set('start_date', startDate) }
      if (endDate) { params.set('end_date', endDate); dates.set('end_date', endDate) }
      void Promise.allSettled([
        requestJSON<PaginatedResponse>(`/api/redemptions?${params}`, {}, controller.signal),
        requestJSON<RedemptionStatistics>(`/api/redemptions/statistics?${dates}`, {}, controller.signal),
      ]).then(([list, stats]) => {
        if (!live()) return
        const errors: string[] = []
        if (list.status === 'fulfilled') {
          const value = list.value
          if (!Array.isArray(value.items) || !Number.isFinite(value.total) || value.total < 0) {
            setCodes([]); setTotal(0); errors.push('兑换码列表返回不完整，请重新读取。')
          }
          else {
            const pages = Math.max(1, value.total_pages || Math.ceil(value.total / pageSize))
            if (page > pages) { setPage(pages); return }
            setCodes(value.items); setTotal(value.total); setTotalPages(pages)
            if (value.quota_per_unit && value.quota_per_unit > 0) setUnit(value.quota_per_unit)
          }
        } else { setCodes([]); setTotal(0); errors.push(report(list.reason)) }
        if (stats.status === 'fulfilled') setStatistics(stats.value)
        else { setStatistics(null); errors.push(report(stats.reason)) }
        setError([...new Set(errors)].join(' '))
      }).finally(() => { if (live()) { setLoading(false); setStatsLoading(false); setRefreshing(false) } })
    }, nameFilter || userFilter ? 250 : 0)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [page, nameFilter, userFilter, statusFilter, startDate, endDate, revision, tokenVersion, currentSession, report])

  const formatTimestamp = (value: number) => !value || value <= 0 ? '-' : new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(value * 1000)
  const formatQuota = (quota: number) => `$${(quota / unit).toFixed(2)}`
  const handleSelectAll = (checked: boolean) => setSelectedIds(checked ? new Set(codes.map(code => code.id)) : new Set())
  const handleSelectOne = (id: number, checked: boolean) => setSelectedIds(previous => {
    const next = new Set(previous); checked ? next.add(id) : next.delete(id); return next
  })
  async function confirmDelete() {
    if (deletePending.current || UI_PREVIEW_ONLY) return
    const ids = deleteDialog.type === 'single' ? [deleteDialog.id!] : [...selectedIds]
    if (!ids.length) return
    const controller = new AbortController()
    deleteAbort.current = controller
    deletePending.current = true
    setDeleting(true)
    const beforeSession = currentSession
    try {
      const result = await requestJSON<{ deleted_count: number }>(deleteDialog.type === 'single' ? `/api/redemptions/${ids[0]}` : '/api/redemptions/batch', {
        method: 'DELETE', ...(deleteDialog.type === 'batch' ? { body: JSON.stringify({ ids }) } : {}),
      }, controller.signal)
      if (controller.signal.aborted || session.current !== beforeSession) return
      if (!Number.isSafeInteger(result.deleted_count) || result.deleted_count < 0) {
        throw new Error('删除结果未能确认，请刷新列表核对。')
      }
      showToast('success', result.deleted_count ? `成功删除 ${result.deleted_count} 个兑换码` : '所选兑换码已不存在，列表已刷新。')
      setSelectedIds(new Set()); setDeleteDialog({ open: false, type: 'single' }); setRevision(value => value + 1)
    } catch (cause) { if (!controller.signal.aborted && session.current === beforeSession) showToast('error', report(cause)) }
    finally { if (deleteAbort.current === controller) { deletePending.current = false; setDeleting(false); deleteAbort.current = null } }
  }
  function handleRefresh() { setRefreshing(true); setSelectedIds(new Set()); setRevision(value => value + 1) }
  async function copyToClipboard(text: string) {
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(text)
      else {
        const textarea = document.createElement('textarea'); textarea.value = text; textarea.style.position = 'fixed'; textarea.style.left = '-9999px'
        document.body.appendChild(textarea); textarea.select()
        const copied = document.execCommand('copy'); textarea.remove(); if (!copied) throw new Error('copy failed')
      }
      showToast('success', '兑换码已复制')
    } catch { showToast('error', '复制失败，请手动复制') }
  }

  return (
    <div className="space-y-6 animate-in fade-in duration-500">
      {error && <Notice kind="error">{error}</Notice>}
      {UI_PREVIEW_ONLY && <Notice>数据获取已暂停。</Notice>}
      {/* Header */}
      <div className="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
        <div>
          <h2 className="text-3xl font-bold tracking-tight">兑换码管理</h2>
          <p className="text-muted-foreground mt-1">查询、管理或批量删除兑换码</p>
        </div>
        <div className="flex items-center gap-3">
          <Button variant="outline" size="sm" onClick={handleRefresh} disabled={refreshing || loading} className="h-9">
            <RefreshCw className={cn("h-4 w-4 mr-2", refreshing && "animate-spin")} />
            刷新
          </Button>
          {selectedIds.size > 0 && (
            <Button variant="destructive" size="sm" onClick={() => setDeleteDialog({ open: true, type: 'batch' })} className="h-9">
              <Trash2 className="h-4 w-4 mr-2" />
              删除选中 ({selectedIds.size})
            </Button>
          )}
        </div>
      </div>

      {/* Statistics Cards */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <StatCard
          title="未使用"
          value={statsLoading || !statistics ? '-' : `${statistics?.unused_count || 0} 个`}
          subValue={statsLoading || !statistics ? '-' : `${formatQuota(statistics?.unused_quota || 0)}`}
          icon={CheckCircle2}
          color="green"
          className="border-l-4 border-l-green-500"
          onClick={() => setStatusFilter('unused')}
        />
        <StatCard
          title="已使用"
          value={statsLoading || !statistics ? '-' : `${statistics?.used_count || 0} 个`}
          subValue={statsLoading || !statistics ? '-' : `${formatQuota(statistics?.used_quota || 0)}`}
          icon={Ticket}
          color="blue"
          className="border-l-4 border-l-blue-500"
          onClick={() => setStatusFilter('used')}
        />
        <StatCard
          title="已过期"
          value={statsLoading || !statistics ? '-' : `${statistics?.expired_count || 0} 个`}
          subValue={statsLoading || !statistics ? '-' : `${formatQuota(statistics?.expired_quota || 0)}`}
          icon={XCircle}
          color="red"
          className="border-l-4 border-l-red-500"
          onClick={() => setStatusFilter('expired')}
        />
      </div>

      {/* Total Stats Summary */}
      <Card className="bg-muted/30 border-dashed">
        <CardContent className="p-4 flex flex-wrap gap-x-8 gap-y-2 text-sm">
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground">总兑换码:</span>
            <span className="font-semibold">{statsLoading || !statistics ? '-' : statistics?.total_count || 0} 个</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground">总额度价值:</span>
            <span className="font-semibold text-primary">{statsLoading || !statistics ? '-' : formatQuota(statistics?.total_quota || 0)}</span>
          </div>
          {!!statistics?.disabled_count && <div className="flex items-center gap-2 text-muted-foreground"><span>已禁用:</span><span>{statistics.disabled_count} 个 · {formatQuota(statistics.disabled_quota || 0)}</span></div>}
          {userFilter.trim() && (
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground">该用户匹配:</span>
              <span className="font-semibold text-primary">{loading ? '-' : `${total} 个`}</span>
            </div>
          )}
        </CardContent>
      </Card>

      {!!statistics?.unknown_count && <Notice kind="warning">有 {statistics.unknown_count} 个兑换码处于未识别状态，已单独标记，未计入未使用数量。</Notice>}
      {/* Filters */}
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-medium flex items-center gap-2">
            <Filter className="w-4 h-4" />
            筛选条件
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-6 gap-4">
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">名称搜索</label>
              <div className="relative">
                <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input
                  type="text"
                  value={nameFilter}
                  onChange={(e) => setNameFilter(e.target.value)}
                  placeholder="搜索兑换码名称..."
                  className="pl-9"
                />
              </div>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">使用用户</label>
              <div className="relative">
                <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input
                  type="text"
                  value={userFilter}
                  onChange={(e) => setUserFilter(e.target.value)}
                  placeholder="用户名模糊查 / 用户ID精确查"
                  className="pl-9"
                  spellCheck={false}
                  autoComplete="off"
                />
              </div>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">状态</label>
              <div className="relative">
                <Tag className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground z-10" />
                <Select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as StatusFilter)} className="pl-9">
                  <option value="">全部状态</option>
                  <option value="unused">未使用</option>
                  <option value="used">已使用</option>
                  <option value="expired">已过期</option>
                </Select>
              </div>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">开始日期</label>
              <div className="relative">
                <Calendar className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input type="date" value={startDate} onChange={(e) => setStartDate(e.target.value)} className="pl-9" />
              </div>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">结束日期</label>
              <div className="relative">
                <Calendar className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input type="date" value={endDate} onChange={(e) => setEndDate(e.target.value)} className="pl-9" />
              </div>
            </div>
            <div className="flex items-end">
              <Button variant="ghost" className="w-full text-muted-foreground hover:text-foreground" onClick={() => { setNameFilter(''); setUserFilter(''); setStatusFilter(''); setStartDate(''); setEndDate('') }}>
                清除筛选
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Table */}
      <Card>
        <CardContent className="p-0">
          {loading ? (
            <div className="flex justify-center items-center py-20">
              <Loader2 className="h-10 w-10 animate-spin text-primary" />
            </div>
          ) : codes.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-20 text-center">
              <div className="bg-muted/50 p-4 rounded-full mb-4">
                <Ticket className="h-8 w-8 text-muted-foreground" />
              </div>
              <h3 className="text-lg font-medium">暂无兑换码</h3>
              <p className="text-muted-foreground mt-1 max-w-sm">
                当前没有找到任何兑换码。请尝试调整筛选条件或前往生成器创建新的兑换码。
              </p>
            </div>
          ) : (
            <>
            {/* Mobile cards */}
            <div className="md:hidden divide-y divide-border/60 border-t border-b">
              {codes.map((code) => {
                const expired = code.expired_time > 0 && code.expired_time * 1000 < Date.now()
                return (
                  <div key={code.id} className="px-3 py-3 space-y-2 hover:bg-muted/30">
                    <div className="flex items-start gap-2">
                      <input
                        type="checkbox"
                        aria-label={`选择兑换码 ${code.name || code.id}`} checked={selectedIds.has(code.id)}
                        onChange={(e) => handleSelectOne(code.id, e.target.checked)}
                        className="mt-1 rounded border-input w-4 h-4"
                      />
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-medium text-sm truncate">{code.name || '未命名'}</span>
                          <Badge variant={code.status === 'unused' ? 'success' : code.status === 'used' ? 'secondary' : code.status === 'expired' ? 'destructive' : 'outline'}>
                            {statusNames[code.status] || '未知状态'}
                          </Badge>
                        </div>
                        <div className="mt-1 flex items-center gap-2">
                          <code className="text-[11px] font-mono bg-muted px-1.5 py-0.5 rounded truncate flex-1">{code.key}</code>
                          <button onClick={() => copyToClipboard(code.key)} className="text-muted-foreground hover:text-primary shrink-0" title="复制">
                            <Copy className="h-3.5 w-3.5" />
                          </button>
                        </div>
                        <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
                          <div><span className="text-muted-foreground">额度：</span><span className="text-green-600 font-medium">{formatQuota(code.quota)}</span></div>
                          <div className="text-muted-foreground truncate">
                            {code.expired_time > 0 ? (
                              <span className={cn("flex items-center gap-1", expired && "text-red-500")}>
                                {expired && <AlertCircle className="h-3 w-3" />}
                                {formatTimestamp(code.expired_time)}
                              </span>
                            ) : '永不过期'}
                          </div>
                          <div className="col-span-2 text-muted-foreground">创建：{formatTimestamp(code.created_time)}</div>
                          {code.used_user_id > 0 && (
                            <div className="col-span-2 flex items-center gap-2 flex-wrap">
                              <button
                                type="button"
                                onClick={() => {
                                  setSelectedUser({ id: code.used_user_id, username: code.used_username || `用户 #${code.used_user_id}` })
                                  setAnalysisDialogOpen(true)
                                }}
                                className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-muted/50 hover:bg-primary/10 hover:text-primary text-xs"
                              >
                                <div className="w-4 h-4 rounded-full bg-primary/10 flex items-center justify-center border border-primary/20 text-[9px] text-primary font-bold">
                                  {(code.used_username || '#')[0]?.toUpperCase()}
                                </div>
                                {code.used_username || `用户 #${code.used_user_id}`}
                              </button>
                              <button
                                type="button"
                                className="text-[11px] text-muted-foreground hover:text-primary"
                                title="按此用户筛选"
                                onClick={() => setUserFilter(String(code.used_user_id))}
                              >
                                ID: {code.used_user_id}
                              </button>
                            </div>
                          )}
                        </div>
                      </div>
                      <Button
                        variant="ghost"
                        size="icon"
                        onClick={() => setDeleteDialog({ open: true, type: 'single', id: code.id })}
                        className="h-8 w-8 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                )
              })}
            </div>

            {/* Desktop table */}
            <div className="hidden md:block rounded-md border-t border-b sm:border-0">
              <Table>
                <TableHeader className="bg-muted/50">
                  <TableRow>
                    <TableHead className="w-12 text-center">
                      <input
                        type="checkbox"
                        aria-label="选择当前页全部兑换码" checked={selectedIds.size === codes.length && codes.length > 0}
                        onChange={(e) => handleSelectAll(e.target.checked)}
                        className="rounded border-input w-4 h-4 align-middle"
                      />
                    </TableHead>
                    <TableHead>兑换码</TableHead>
                    <TableHead>名称</TableHead>
                    <TableHead>额度 (USD)</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>使用用户</TableHead>
                    <TableHead>创建时间</TableHead>
                    <TableHead>过期时间</TableHead>
                    <TableHead className="w-16 text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {codes.map((code) => (
                    <TableRow key={code.id} className="hover:bg-muted/50">
                      <TableCell className="text-center">
                        <input
                          type="checkbox"
                          aria-label={`选择兑换码 ${code.name || code.id}`} checked={selectedIds.has(code.id)}
                          onChange={(e) => handleSelectOne(code.id, e.target.checked)}
                          className="rounded border-input w-4 h-4 align-middle"
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2 group">
                          <code className="text-xs font-mono bg-muted px-1.5 py-0.5 rounded">{code.key}</code>
                          <button
                            onClick={() => copyToClipboard(code.key)}
                            className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-primary transition-opacity"
                            title="复制"
                          >
                            <Copy className="h-3.5 w-3.5" />
                          </button>
                        </div>
                      </TableCell>
                      <TableCell className="font-medium text-sm">{code.name}</TableCell>
                      <TableCell className="font-medium text-green-600">{formatQuota(code.quota)}</TableCell>
                      <TableCell>
                        <Badge variant={code.status === 'unused' ? 'success' : code.status === 'used' ? 'secondary' : code.status === 'expired' ? 'destructive' : 'outline'}>
                          {statusNames[code.status] || '未知状态'}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        {code.used_user_id > 0 ? (
                          <div className="flex flex-col gap-0.5">
                            <button
                              type="button"
                              className="flex items-center gap-2 px-2 py-1 rounded-full bg-muted/50 hover:bg-primary/10 hover:text-primary transition-all border border-transparent hover:border-primary/20 w-fit"
                              onClick={() => {
                                setSelectedUser({ id: code.used_user_id, username: code.used_username || `用户 #${code.used_user_id}` })
                                setAnalysisDialogOpen(true)
                              }}
                              title="查看用户分析"
                            >
                              <div className="w-5 h-5 rounded-full bg-primary/10 flex items-center justify-center border border-primary/20 text-[10px] text-primary font-bold">
                                {(code.used_username || '#')[0]?.toUpperCase()}
                              </div>
                              <span className="font-medium text-sm whitespace-nowrap">
                                {code.used_username || `用户 #${code.used_user_id}`}
                              </span>
                            </button>
                            <button
                              type="button"
                              className="text-[11px] text-muted-foreground hover:text-primary pl-2 w-fit"
                              title="按此用户筛选其使用的兑换码"
                              onClick={() => setUserFilter(String(code.used_user_id))}
                            >
                              ID: {code.used_user_id}
                            </button>
                          </div>
                        ) : (
                          <span className="text-sm text-muted-foreground">-</span>
                        )}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{formatTimestamp(code.created_time)}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {code.expired_time > 0 ? (
                           <div className="flex items-center gap-1">
                             {code.expired_time * 1000 < Date.now() && <AlertCircle className="w-3 h-3 text-red-500" />}
                             {formatTimestamp(code.expired_time)}
                           </div>
                        ) : '永不过期'}
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="ghost"
                          size="icon"
                          onClick={() => setDeleteDialog({ open: true, type: 'single', id: code.id })}
                          className="h-8 w-8 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            </>
          )}

          {/* Pagination */}
          {total > 0 && (
            <div className="px-4 py-4 border-t flex items-center justify-between">
              <div className="text-sm text-muted-foreground">
                {userFilter.trim()
                  ? `显示 ${codes.length} 条，该用户共使用 ${total} 个兑换码`
                  : `显示 ${codes.length} 条，共 ${total} 条`}
              </div>
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={page === 1}>上一页</Button>
                <div className="flex items-center px-2 text-sm font-medium">
                  {page} / {totalPages}
                </div>
                <Button variant="outline" size="sm" onClick={() => setPage((p) => Math.min(totalPages, p + 1))} disabled={page === totalPages}>下一页</Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Delete Dialog */}
      <Dialog open={deleteDialog.open} onOpenChange={(open: boolean) => { if (!deleting) setDeleteDialog(prev => ({ ...prev, open })) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认删除</DialogTitle>
            <DialogDescription>
              {deleteDialog.type === 'single' ? '确定要删除这个兑换码吗？此操作不可恢复。' : `确定要删除选中的 ${selectedIds.size} 个兑换码吗？此操作不可恢复。`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteDialog({ open: false, type: 'single' })} disabled={deleting}>取消</Button>
            <Button variant="destructive" onClick={confirmDelete} disabled={deleting}>
              {deleting ? <><Loader2 className="h-4 w-4 mr-2 animate-spin" />删除中...</> : '确认删除'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* User Analysis Dialog */}
      {selectedUser && (
        analysisDialogOpen && <AdminDialog wide title={`用户详情 · ${selectedUser.username}`} onClose={() => setAnalysisDialogOpen(false)}>
          <RiskUserCard userId={selectedUser.id} username={selectedUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
        </AdminDialog>
      )}
    </div>
  )
}
