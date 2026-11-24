import { Fragment, useCallback, useEffect, useState } from 'react'
import { CheckCircle2, ChevronDown, ChevronRight, Clock, Coins, Filter, Link2, ListChecks, Loader2, RefreshCw, Search, XCircle } from 'lucide-react'
import { ApiError, get, UI_PREVIEW_ONLY } from './api'
import { StatCard } from './components/StatCard'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import type { DashboardPeriod } from './types'
import './task-logs.css'

const statuses = [
  { value: 'SUCCESS', label: '成功' },
  { value: 'FAILURE', label: '失败' },
  { value: 'IN_PROGRESS', label: '进行中' },
  { value: 'QUEUED', label: '排队中' },
  { value: 'SUBMITTED', label: '已提交' },
  { value: 'NOT_START', label: '未开始' },
  { value: 'UNKNOWN', label: '未知' },
] as const

const periods = [
  { value: '24h', label: '最近 24 小时' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
] as const

type TaskFilters = {
  period: DashboardPeriod
  status: string
  platform: string
  task_id: string
  user: string
}

const defaultFilters: TaskFilters = { period: '7d', status: '', platform: '', task_id: '', user: '' }

type TaskLog = {
  id: number
  task_id: string
  platform: string
  action: string
  user_id: number
  username: string
  channel_id: number
  status: string
  progress: string
  quota: number
  submit_time: number
  start_time: number
  finish_time: number
}

type TaskLogResponse = {
  timezone: string
  quota_per_unit: number
  summary: { total: number; succeeded: number; failed: number; quota: number }
  items: TaskLog[]
  total: number
  page: number
  total_pages: number
}

type Result =
  | { key: string; data: TaskLogResponse }
  | { key: string; error: string }

const numberFormat = new Intl.NumberFormat('zh-CN')
const quotaFormat = new Intl.NumberFormat('en-US', {
  style: 'currency', currency: 'USD', minimumFractionDigits: 4, maximumFractionDigits: 4,
})

function formattedQuota(quota: number, quotaPerUnit: number) {
  return quotaPerUnit > 0 ? quotaFormat.format(quota / quotaPerUnit) : '—'
}

function duration(task: TaskLog) {
  if (task.finish_time <= 0 || task.submit_time <= 0) return '—'
  const seconds = Math.max(0, task.finish_time - task.submit_time)
  return seconds >= 60 ? `${Math.floor(seconds / 60)}m${seconds % 60}s` : `${seconds}s`
}

function StatusBadge({ status }: { status: string }) {
  const label = statuses.find(item => item.value === status)?.label ?? (status || '未知')
  const variant = status === 'SUCCESS' ? 'success' : status === 'FAILURE' ? 'destructive'
    : ['IN_PROGRESS', 'QUEUED', 'SUBMITTED'].includes(status) ? 'warning' : 'secondary'
  return <Badge variant={variant}>{label}</Badge>
}

type UsageEntry = {
  id: number
  created_at: number
  kind: 'consume' | 'refund' | 'error'
  model: string
  request_id: string
  quota: number
}

type TaskUsage = {
  logs: UsageEntry[]
  quota_per_unit: number
  truncated: boolean
}

const usageKinds = { consume: '消费', refund: '退款', error: '错误' }

function TaskUsageDetails({ task, formatTime, onCollapse, onUnauthorized }: {
  task: TaskLog
  formatTime: (timestamp: number) => string
  onCollapse: (id: number) => void
  onUnauthorized: () => void
}) {
  const [result, setResult] = useState<{ data?: TaskUsage; error?: string } | null>(null)
  const [retry, setRetry] = useState(0)

  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    void get<TaskUsage>(`/api/task-logs/${task.id}/logs`, controller.signal).then(
      data => { if (!controller.signal.aborted) setResult({ data }) },
      cause => {
        if (controller.signal.aborted) return
        if (cause instanceof ApiError && cause.status === 401) {
          onCollapse(task.id)
          onUnauthorized()
          return
        }
        setResult({ error: cause instanceof Error ? cause.message : '无法读取关联使用日志。' })
      },
    )
    return () => controller.abort()
  }, [task.id, retry, onCollapse, onUnauthorized])

  const usage = result?.data

  return (
    <div id={`task-usage-${task.id}`} className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Link2 className="h-3.5 w-3.5" />
        关联使用日志 {usage ? `${numberFormat.format(usage.logs.length)} 条` : ''}
        {!result && !UI_PREVIEW_ONLY && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
      </div>
      {result?.error && <div className="flex items-center justify-between gap-3 text-xs text-destructive" role="alert">
        <p>{result.error}</p>
        <Button variant="outline" size="sm" onClick={() => { setResult(null); setRetry(current => current + 1) }}>重试</Button>
      </div>}
      <Table className="min-w-[680px] bg-background">
        <TableHeader>
          <TableRow>
            {['时间', '类型', '模型', '请求 ID', '额度'].map(label => <TableHead key={label} className="h-8 whitespace-nowrap text-xs">{label}</TableHead>)}
          </TableRow>
        </TableHeader>
        <TableBody>
          {usage?.logs.map(log => <TableRow key={log.id}>
            <TableCell className="whitespace-nowrap py-2 text-xs text-muted-foreground">{formatTime(log.created_at)}</TableCell>
            <TableCell className="py-2"><Badge variant={log.kind === 'error' ? 'destructive' : log.kind === 'refund' ? 'secondary' : 'success'} className="px-1.5 py-0 text-[10px]">{usageKinds[log.kind]}</Badge></TableCell>
            <TableCell className="py-2 font-mono text-xs">{log.model || '—'}</TableCell>
            <TableCell className="max-w-[240px] truncate py-2 font-mono text-xs" title={log.request_id}>{log.request_id || '—'}</TableCell>
            <TableCell className="py-2 font-mono text-xs">{formattedQuota(log.quota, usage.quota_per_unit)}</TableCell>
          </TableRow>)}
        </TableBody>
      </Table>
      {!usage?.logs.length && <p className="py-3 text-xs text-muted-foreground" role="status">
        {UI_PREVIEW_ONLY ? '数据获取已暂停' : !result ? '正在读取关联使用日志...' : result.error ? '关联日志暂不可用' : '暂无明确关联的使用日志'}
      </p>}
      {usage?.truncated && <p className="text-xs text-muted-foreground">仅显示最近的匹配记录。</p>}
    </div>
  )
}

export default function TaskLogs({ tokenVersion, onUnauthorized }: { tokenVersion: number; onUnauthorized: () => void }) {
  const [draft, setDraft] = useState(defaultFilters)
  const [query, setQuery] = useState({ filters: defaultFilters, page: 1, revision: 0 })
  const [result, setResult] = useState<Result | null>(null)
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const collapseTask = useCallback((id: number) => setExpanded(previous => {
    const next = new Set(previous)
    next.delete(id)
    return next
  }), [])

  const params = new URLSearchParams({ period: query.filters.period, page: String(query.page), page_size: '20' })
  for (const key of ['status', 'platform', 'task_id', 'user'] as const) {
    if (query.filters[key]) params.set(key, query.filters[key])
  }
  const path = `/api/task-logs?${params}`
  const requestKey = `${tokenVersion}:${query.revision}:${path}`

  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    void get<TaskLogResponse>(path, controller.signal).then(
      data => {
        if (!controller.signal.aborted) setResult({ key: requestKey, data })
      },
      cause => {
        if (controller.signal.aborted) return
        const unauthorized = cause instanceof ApiError && cause.status === 401
        const error = unauthorized ? '请输入有效的管理员令牌。'
          : cause instanceof Error ? cause.message : '无法读取任务日志。'
        setResult({ key: requestKey, error })
        if (unauthorized) onUnauthorized()
      },
    )
    return () => controller.abort()
  }, [onUnauthorized, path, requestKey])

  const current = result?.key === requestKey ? result : null
  const data = current && 'data' in current ? current.data : null
  const error = current && 'error' in current ? current.error : null
  const busy = !UI_PREVIEW_ONLY && !current
  const dateFormat = new Intl.DateTimeFormat('zh-CN', {
    timeZone: data?.timezone ?? 'Asia/Shanghai',
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  })
  const formatTime = (timestamp: number) => timestamp > 0 ? dateFormat.format(timestamp * 1000) : '—'

  function applyFilters(filters: TaskFilters) {
    setExpanded(new Set())
    setQuery(previous => ({
      filters: { ...filters, platform: filters.platform.trim(), task_id: filters.task_id.trim(), user: filters.user.trim() },
      page: 1,
      revision: previous.revision + 1,
    }))
  }

  function updatePage(page: number) {
    setExpanded(new Set())
    setQuery(previous => ({ ...previous, page, revision: previous.revision + 1 }))
  }

  function filterStatus(status: string) {
    const filters = { ...draft, status }
    setDraft(filters)
    applyFilters(filters)
  }

  function reset() {
    setDraft(defaultFilters)
    applyFilters(defaultFilters)
  }

  function toggleTask(id: number) {
    setExpanded(previous => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const summary = data?.summary
  const page = data?.page ?? query.page
  const pages = Math.max(1, data?.total_pages ?? 1)
  const emptyMessage = UI_PREVIEW_ONLY ? '数据获取已暂停' : busy ? '正在加载任务日志...' : error ? '任务日志暂不可用' : '没有找到符合条件的任务记录'

  return (
    <div className="task-logs-page space-y-6">
      <div className="flex flex-col items-start justify-between gap-4 sm:flex-row sm:items-center">
        <div className="min-w-0">
          <h1 className="text-3xl font-bold">任务日志</h1>
          <p className="mt-1 text-sm text-muted-foreground">异步任务（生图/视频/音乐）记录，点击任务行查看关联的使用日志</p>
        </div>
        <Button variant="outline" size="sm" disabled={busy || UI_PREVIEW_ONLY} onClick={() => updatePage(page)} className="h-9 shrink-0">
          <RefreshCw className={`mr-2 h-4 w-4 ${busy ? 'animate-spin' : ''}`} />刷新
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-4" aria-label="任务统计" aria-busy={busy}>
        <StatCard title="任务总数" value={summary ? numberFormat.format(summary.total) : '—'} icon={ListChecks} color="blue" className="border-l-4 border-l-blue-500" onClick={() => filterStatus('')} />
        <StatCard title="成功" value={summary ? numberFormat.format(summary.succeeded) : '—'} icon={CheckCircle2} color="green" className="border-l-4 border-l-green-500" onClick={() => filterStatus('SUCCESS')} />
        <StatCard title="失败" value={summary ? numberFormat.format(summary.failed) : '—'} icon={XCircle} color="red" className="border-l-4 border-l-red-500" onClick={() => filterStatus('FAILURE')} />
        <StatCard title="消耗额度" value={summary && data ? formattedQuota(summary.quota, data.quota_per_unit) : '—'} icon={Coins} color="yellow" className="border-l-4 border-l-yellow-500" />
      </div>

      <Card aria-labelledby="task-filter-title">
        <CardHeader className="pb-3">
          <CardTitle id="task-filter-title" className="flex items-center gap-2 text-base font-medium"><Filter className="h-4 w-4" />筛选条件</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={event => { event.preventDefault(); applyFilters(draft) }}>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-5">
              <div className="space-y-1">
                <label htmlFor="task-user" className="text-xs font-medium text-muted-foreground">用户搜索</label>
                <div className="relative">
                  <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                  <Input id="task-user" value={draft.user} className="pl-9" maxLength={64} placeholder="用户名或用户 ID" onChange={event => setDraft({ ...draft, user: event.target.value })} />
                </div>
              </div>
              <div className="space-y-1">
                <label htmlFor="task-id" className="text-xs font-medium text-muted-foreground">任务 ID</label>
                <Input id="task-id" value={draft.task_id} className="font-mono" maxLength={191} placeholder="精确查找任务 ID..." spellCheck={false} onChange={event => setDraft({ ...draft, task_id: event.target.value })} />
              </div>
              <div className="space-y-1">
                <label htmlFor="task-platform" className="text-xs font-medium text-muted-foreground">平台</label>
                <Input id="task-platform" value={draft.platform} maxLength={30} placeholder="输入平台代码" onChange={event => setDraft({ ...draft, platform: event.target.value })} />
              </div>
              <div className="space-y-1">
                <label htmlFor="task-status" className="text-xs font-medium text-muted-foreground">状态</label>
                <Select id="task-status" value={draft.status} onChange={event => setDraft({ ...draft, status: event.target.value })}>
                  <option value="">全部状态</option>
                  {statuses.map(status => <option key={status.value} value={status.value}>{status.label}</option>)}
                </Select>
              </div>
              <div className="space-y-1">
                <label htmlFor="task-period" className="text-xs font-medium text-muted-foreground">提交时间</label>
                <Select id="task-period" value={draft.period} onChange={event => setDraft({ ...draft, period: event.target.value as DashboardPeriod })}>
                  {periods.map(period => <option key={period.value} value={period.value}>{period.label}</option>)}
                </Select>
              </div>
            </div>
            <div className="mt-4 flex justify-end gap-2">
              <Button type="button" variant="ghost" size="sm" onClick={reset} className="text-muted-foreground">重置筛选</Button>
              <Button type="submit" size="sm"><Search className="mr-2 h-4 w-4" />查询</Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {error && <div className="flex flex-wrap items-center gap-3 rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive" role="alert">
        <div className="min-w-0 flex-1"><strong className="font-medium">任务日志加载失败</strong><p className="mt-1 break-words">{error}</p></div>
        <Button variant="outline" size="sm" onClick={() => updatePage(query.page)}>重试</Button>
      </div>}

      <Card className="min-w-0" aria-labelledby="task-records-title" aria-busy={busy}>
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle id="task-records-title" className="text-lg">任务记录</CardTitle>
          <span className="text-xs text-muted-foreground">时间按 {data?.timezone ?? 'Asia/Shanghai'} 显示</span>
        </CardHeader>
        <CardContent className="p-0">
          <Table className="min-w-[1380px]">
            <TableHeader>
              <TableRow>
                <TableHead className="w-8"><span className="sr-only">展开</span></TableHead>
                {['任务 ID', '平台 / 操作', '用户', '渠道 ID', '状态 / 进度', '额度', '提交时间', '耗时', '开始时间', '结束时间'].map(label => <TableHead key={label} className="whitespace-nowrap">{label}</TableHead>)}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.items.map(task => {
                const isOpen = expanded.has(task.id)
                return <Fragment key={task.id}>
                  <TableRow className="cursor-pointer hover:bg-muted/50" onClick={() => toggleTask(task.id)}>
                    <TableCell className="pr-0">
                      <button type="button" className="task-expand-button" title={isOpen ? '收起关联日志' : '查看关联使用日志'} aria-expanded={isOpen} aria-controls={`task-usage-${task.id}`} aria-label={`${isOpen ? '收起' : '查看'}任务 ${task.task_id || task.id} 的关联使用日志`} onClick={event => { event.stopPropagation(); toggleTask(task.id) }}>
                        {isOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                      </button>
                    </TableCell>
                    <TableCell><code className="text-xs" title={task.task_id}>{task.task_id.length > 24 ? `${task.task_id.slice(0, 24)}...` : task.task_id || '—'}</code></TableCell>
                    <TableCell className="whitespace-nowrap text-xs"><span className="font-medium">{task.platform || '—'}</span>{task.action && <span className="text-muted-foreground"> / {task.action}</span>}</TableCell>
                    <TableCell className="whitespace-nowrap text-sm"><span>{task.username || `#${task.user_id}`}</span>{task.username && <div className="text-xs text-muted-foreground">ID: {task.user_id}</div>}</TableCell>
                    <TableCell className="text-xs">{task.channel_id || '—'}</TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1.5">
                        <StatusBadge status={task.status} />
                        {task.progress && <span className="text-[10px] text-muted-foreground">{task.progress}</span>}
                      </div>
                    </TableCell>
                    <TableCell className="whitespace-nowrap font-mono text-xs">{formattedQuota(task.quota, data.quota_per_unit)}</TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{formatTime(task.submit_time)}</TableCell>
                    <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">{duration(task)}</TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{formatTime(task.start_time)}</TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{formatTime(task.finish_time)}</TableCell>
                  </TableRow>
                  {isOpen && <TableRow className="bg-muted/20 hover:bg-muted/20">
                    <TableCell colSpan={11} className="px-6 py-3">
                      <TaskUsageDetails task={task} formatTime={formatTime} onCollapse={collapseTask} onUnauthorized={onUnauthorized} />
                    </TableCell>
                  </TableRow>}
                </Fragment>
              })}
            </TableBody>
          </Table>
          {!data?.items.length && <div className="flex min-h-56 flex-col items-center justify-center px-4 py-8 text-center text-muted-foreground" role="status">
            <div className="mb-4 rounded-full bg-muted/50 p-4"><ListChecks className="h-8 w-8" /></div>
            <h3 className="text-lg font-medium text-foreground">{UI_PREVIEW_ONLY ? '数据获取已暂停' : busy ? '正在加载任务日志...' : error ? '任务日志暂不可用' : '暂无任务'}</h3>
            {!UI_PREVIEW_ONLY && !busy && !error && <p className="mt-1 text-sm">{emptyMessage}</p>}
          </div>}
          <div className="task-logs-pagination border-t px-4 py-4" aria-label="任务日志分页">
            <span className="text-sm text-muted-foreground">显示 {data ? numberFormat.format(data.items.length) : '—'} 条，共 {data ? numberFormat.format(data.total) : '—'} 条</span>
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy || page <= 1} onClick={() => updatePage(page - 1)}>上一页</Button>
              <span className="px-2 text-sm font-medium">{page} / {pages}</span>
              <Button variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || busy || page >= pages} onClick={() => updatePage(page + 1)}>下一页</Button>
            </div>
          </div>
        </CardContent>
      </Card>
      <p className="flex items-center gap-1.5 text-xs text-muted-foreground"><Clock className="h-3.5 w-3.5 shrink-0" />提交、开始和结束时间按 {data?.timezone ?? 'Asia/Shanghai'} 显示。</p>
    </div>
  )
}
