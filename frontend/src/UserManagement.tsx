import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import {
  Activity, ChevronDown, ChevronLeft, ChevronRight, Clock, Download, Eye, Filter, History, Loader2,
  RefreshCw, Search, ShieldBan, ShieldCheck, Trash2, UserCheck, UserX, Users, X,
} from 'lucide-react'
import { actionKey, ApiError, downloadCSV, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, Notice } from './ModuleUI'
import { StatCard } from './components/StatCard'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import { UserIdentityButton } from './components/UserIdentityButton'
import { cn } from './lib/utils'
import UserLoginObservations from './UserLoginObservations'
import UserRiskAnalysisPanel from './UserRiskAnalysisPanel'
import UserCheckinPanel from './UserCheckinPanel'
import './user-management.css'

type CallActivity = {
  last_attempt_at?: number
  last_success_at?: number
  observed_requests: number
  requests_24h: number
  requests_7d: number
  requests_30d: number
  successes_30d: number
  failures_30d: number
  active_days_7d: number
  active_days_30d?: number
  activity: string
  intensity_7d: string
}

type LoginActivity = {
  last_success_at?: number
  successes_30d: number
  failures_30d: number
  attempted_account_failures: number
  attempted_account_identity: string
}

type UserRecord = {
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
  call_activity: CallActivity
  login_activity: LoginActivity
}

type UserStats = {
  total: number
  enabled: number
  disabled: number
  active_24h: number
  active_7d: number
  active_30d: number
  requests_7d: number
  requests_30d: number
  activity: Record<string, number>
}

type UserPage = {
  items: UserRecord[]
  total: number
  page: number
  page_size: number
  stats: UserStats
  as_of: number
  mode: string
  history_complete: boolean
  activity_window_complete?: boolean
  active_days_timezone: string
}

type UserDetail = { user: UserRecord; as_of: number; mode: string; history_complete: boolean; activity_window_complete?: boolean }
type SoftDeletedData = { count: number; users?: string[]; sample?: boolean }
type LifecycleRecord = {
  id: string; action: string; user_id?: number; user_ids: number[]; usernames: string[]
  reason: string; operator: string; state: string; affected: number
  created_at: number; updated_at: number; error?: string; warning?: string
}
type LifecyclePage = { items: LifecycleRecord[]; total: number; page: number; page_size: number }
type Filters = {
  search: string
  user_id: string
  group: string
  status: string
  activity: string
  intensity: string
  sort: string
  order: string
}
type ActionTarget = { user: UserRecord; action: 'ban' | 'unban' | 'soft-delete' | 'hard-delete' }

const activityNames: Record<string, string> = {
  daily: '日活跃', weekly: '周内调用', monthly: '月内调用', silent: '沉默',
  dormant: '长期未调用', never: '从未调用', unknown: '数据不足',
}
const intensityNames: Record<string, string> = { none: '无调用', low: '低频', regular: '常规', high: '高频', dense: '密集' }
const lifecycleActionNames: Record<string, string> = { soft_delete: '注销用户', hard_delete: '彻底删除用户', purge_deleted: '批量清理注销用户' }
const numberFormat = new Intl.NumberFormat('zh-CN')
const defaults: Filters = { search: '', user_id: '', group: '', status: 'all', activity: 'all', intensity: 'all', sort: 'id', order: 'desc' }
const emptyData = { items: [], total: 0 } as Pick<UserPage, 'items' | 'total'>

function initialFilters(): Filters {
  const params = new URLSearchParams(window.location.search)
  const id = params.get('user_id') ?? ''
  return { ...defaults, user_id: /^\d+$/.test(id) ? id : '' }
}

function queryString(filters: Filters, page: number, pageSize: number) {
  const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) })
  for (const [key, value] of Object.entries(filters)) {
    if (value.trim()) params.set(key, value.trim())
  }
  return params.toString()
}

function count(value: number | undefined) {
  return value == null ? '—' : numberFormat.format(value)
}

function timestamp(value?: number, timezone = 'Asia/Shanghai') {
  if (!value || !Number.isFinite(value)) return '—'
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: timezone,
    }).format(new Date(value * 1000))
  } catch {
    return new Date(value * 1000).toLocaleString('zh-CN')
  }
}

function relativeTime(value?: number, timezone = 'Asia/Shanghai') {
  if (!value || !Number.isFinite(value)) return { text: '—', tone: 'muted' as const }
  const diff = Date.now() - value * 1000
  const minutes = Math.floor(diff / 60000)
  const tone = minutes < 1440 ? 'active' as const : minutes < 43200 ? 'recent' as const : 'muted' as const
  const text = minutes < 1 ? '刚刚' : minutes < 60 ? `${minutes} 分钟前` : minutes < 1440 ? `${Math.floor(minutes / 60)} 小时前` : minutes < 43200 ? `${Math.floor(minutes / 1440)} 天前` : timestamp(value, timezone)
  return { text, tone }
}

function DetailMetric({ label, value, hint, tone = 'default' }: { label: string; value: string; hint?: string; tone?: 'default' | 'active' | 'warning' }) {
  const valueClass = tone === 'active' ? 'text-emerald-600 dark:text-emerald-400' : tone === 'warning' ? 'text-amber-600 dark:text-amber-400' : 'text-foreground'
  return <div className="um-detail-metric">
    <dt>{label}</dt>
    <dd className={valueClass}>{value}</dd>
    {hint && <small>{hint}</small>}
  </div>
}

function message(cause: unknown) {
  const value = cause instanceof Error ? cause.message : '读取用户信息失败'
  const messages: Record<string, string> = {
    'there is no owned risk ban to release': '该账号没有可由本站解除的封禁记录。',
    'NewAPI management connection is not configured': '尚未配置 NewAPI 管理连接。',
    'user state changed after the ban; automatic restoration is blocked': '封禁后账号状态已被修改，请到风控中心核对处置记录。',
    'administrator accounts are excluded from risk actions': '管理员账号受保护，不能执行此操作。',
    'user management is unavailable': '用户数据暂时不可用，请稍后重试。',
    'call activity import is incomplete or unavailable': '调用记录尚未同步完成，请稍后重试。',
  }
  return messages[value] ?? value
}

function UserStatus({ user }: { user: Pick<UserRecord, 'status' | 'role'> }) {
  return <span className="um-badges">
    <Badge variant={user.status === 1 ? 'success' : user.status === 2 ? 'destructive' : 'outline'}>
      {user.status === 1 ? '启用' : user.status === 2 ? '已封禁' : '未知状态'}
    </Badge>
  </span>
}

function ActivityBadge({ activity }: { activity: string }) {
  const tone = activity === 'daily' ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300'
    : activity === 'weekly' || activity === 'monthly' ? 'border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-900 dark:bg-blue-950/40 dark:text-blue-300'
      : activity === 'silent' || activity === 'dormant' ? 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300'
        : 'bg-muted/70 text-muted-foreground'
  return <Badge variant="outline" className={cn('font-normal', tone)}>{activityNames[activity] ?? activity}</Badge>
}

export default function UserManagement({
  tokenVersion = 0, quotaPerUnit = 500000, onUnauthorized,
}: { tokenVersion?: number; quotaPerUnit?: number; onUnauthorized: () => void }) {
  const [draft, setDraft] = useState<Filters>(initialFilters)
  const [filters, setFilters] = useState<Filters>(initialFilters)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [data, setData] = useState<UserPage | null>(null)
  const [loading, setLoading] = useState(!UI_PREVIEW_ONLY)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [actionWarning, setActionWarning] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [exporting, setExporting] = useState(false)
  const [selected, setSelected] = useState<UserRecord | null>(null)
  const [detail, setDetail] = useState<UserDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [detailRefresh, setDetailRefresh] = useState(0)
  const [target, setTarget] = useState<ActionTarget | null>(null)
  const [reason, setReason] = useState('')
  const [banNote, setBanNote] = useState('')
  const [deleteConfirm, setDeleteConfirm] = useState('')
  const [actionError, setActionError] = useState('')
  const [softDeleted, setSoftDeleted] = useState<SoftDeletedData | null>(null)
  const [purgeOpen, setPurgeOpen] = useState(false)
  const [purgeConfirm, setPurgeConfirm] = useState('')
  const [purgeReason, setPurgeReason] = useState('')
  const [purging, setPurging] = useState(false)
  const [purgeError, setPurgeError] = useState('')
  const [acting, setActing] = useState(false)
  const [view, setView] = useState('list')
  const [detailTab, setDetailTab] = useState('overview')
  const [moreFilters, setMoreFilters] = useState(false)
  const [lifecycleOpen, setLifecycleOpen] = useState(false)
  const [lifecyclePage, setLifecyclePage] = useState(1)
  const [lifecycleData, setLifecycleData] = useState<LifecyclePage | null>(null)
  const [lifecycleLoading, setLifecycleLoading] = useState(false)
  const [lifecycleError, setLifecycleError] = useState('')
  const actionAttempt = useRef<{ key: string; reason: string; note: string } | null>(null)
  const purgeAttempt = useRef<{ key: string; reason: string } | null>(null)
  const listRequest = useRef(0)
  const detailRequest = useRef(0)
  const detailContent = useRef<HTMLDivElement>(null)
  const exportAbort = useRef<AbortController | null>(null)
  const currentSession = useRef(tokenVersion)

  const failed = useCallback((cause: unknown, update: (value: string) => void) => {
    if (cause instanceof DOMException && cause.name === 'AbortError') return
    if (cause instanceof ApiError && cause.status === 401) {
      onUnauthorized()
      return
    }
    update(message(cause))
  }, [onUnauthorized])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) {
      listRequest.current++
      setData(null)
      setLoading(false)
      setError('')
      return
    }
    const abort = new AbortController()
    const serial = ++listRequest.current
    setLoading(true)
    setError('')
    setData(null)
    void requestJSON<UserPage>(`/api/users?${queryString(filters, page, pageSize)}`, {}, abort.signal)
      .then(next => {
        if (serial !== listRequest.current || abort.signal.aborted) return
        const pages = Math.max(1, Math.ceil(next.total / pageSize))
        if (page > pages) { setPage(pages); return }
        setData(next)
      })
      .catch(cause => { if (!abort.signal.aborted && serial === listRequest.current) failed(cause, setError) })
      .finally(() => { if (!abort.signal.aborted && serial === listRequest.current) setLoading(false) })
    return () => abort.abort()
  }, [filters, page, pageSize, tokenVersion, refresh, failed])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) {
      setSoftDeleted(null)
      return
    }
    const abort = new AbortController()
    void requestJSON<SoftDeletedData>('/api/users/soft-deleted/count', {}, abort.signal)
      .then(result => { if (!abort.signal.aborted) setSoftDeleted(result) })
      .catch(() => { if (!abort.signal.aborted) setSoftDeleted(null) })
    return () => abort.abort()
  }, [tokenVersion, refresh])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || !lifecycleOpen) return
    const abort = new AbortController()
    setLifecycleLoading(true)
    setLifecycleError('')
    setLifecycleData(null)
    void requestJSON<LifecyclePage>(`/api/users/lifecycle-audit?page=${lifecyclePage}&page_size=20`, {}, abort.signal)
      .then(result => { if (!abort.signal.aborted) setLifecycleData(result) })
      .catch(cause => { if (!abort.signal.aborted) failed(cause, setLifecycleError) })
      .finally(() => { if (!abort.signal.aborted) setLifecycleLoading(false) })
    return () => abort.abort()
  }, [lifecycleOpen, lifecyclePage, tokenVersion, refresh, failed])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) {
      detailRequest.current++
      setSelected(null)
      setDetail(null)
      setDetailLoading(false)
      setDetailError('')
      return
    }
    if (!selected) { setDetail(null); return }
    const abort = new AbortController()
    const serial = ++detailRequest.current
    setDetail(null)
    setDetailError('')
    setDetailLoading(true)
    void requestJSON<UserDetail>(`/api/users/${selected.id}`, {}, abort.signal)
      .then(next => { if (!abort.signal.aborted && serial === detailRequest.current) setDetail(next) })
      .catch(cause => { if (!abort.signal.aborted && serial === detailRequest.current) failed(cause, setDetailError) })
      .finally(() => { if (!abort.signal.aborted && serial === detailRequest.current) setDetailLoading(false) })
    return () => abort.abort()
  }, [selected?.id, tokenVersion, detailRefresh, failed])

  useEffect(() => () => exportAbort.current?.abort(), [])
  useEffect(() => {
    currentSession.current = tokenVersion
    exportAbort.current?.abort()
    setExporting(false)
    setSelected(null)
    setTarget(null)
    setReason('')
    setBanNote('')
    actionAttempt.current = null
    purgeAttempt.current = null
    setPurgeOpen(false)
    setLifecycleOpen(false)
    setLifecycleData(null)
    setActionError('')
    setSuccess('')
    setActionWarning('')
  }, [tokenVersion])

  const visibleData = UI_PREVIEW_ONLY ? null : data
  const result = visibleData ?? emptyData
  const stats = visibleData?.stats
  const timezone = visibleData?.active_days_timezone ?? 'Asia/Shanghai'
  const money = (value: number) => new Intl.NumberFormat('zh-CN', {
    style: 'currency', currency: 'USD', maximumFractionDigits: 2,
  }).format(value / (quotaPerUnit > 0 ? quotaPerUnit : 500000))

  function applyFilters(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY) return
    setPage(1)
    setFilters({ ...draft })
    setSuccess('')
  }

  function resetFilters() {
    setDraft({ ...defaults })
    setFilters({ ...defaults })
    setPage(1)
    setSuccess('')
  }

  async function exportUsers() {
    if (UI_PREVIEW_ONLY || exporting) return
    const abort = new AbortController()
    exportAbort.current = abort
    setExporting(true)
    setError('')
    try {
      await downloadCSV(`/api/users/export?${queryString(filters, 1, pageSize)}`, 'users.csv', abort.signal)
      if (!abort.signal.aborted) setSuccess('用户数据已导出。')
    } catch (cause) {
      if (!abort.signal.aborted) failed(cause, setError)
    } finally {
      if (!abort.signal.aborted) setExporting(false)
    }
  }

  function openAction(user: UserRecord, action: 'ban' | 'unban' | 'soft-delete' | 'hard-delete') {
    if (UI_PREVIEW_ONLY) return
    setTarget({ user, action })
    setReason('')
    setBanNote('')
    setDeleteConfirm('')
    setActionError('')
    actionAttempt.current = null
    setSuccess('')
    setActionWarning('')
  }

  async function confirmAction(event: FormEvent) {
    event.preventDefault()
    const deleteAction = target?.action === 'soft-delete' || target?.action === 'hard-delete'
    const expectedConfirm = target?.action === 'soft-delete' ? '注销用户' : target?.action === 'hard-delete' ? '彻底删除' : ''
    if (UI_PREVIEW_ONLY || !target || acting) return
    if (deleteAction && deleteConfirm.trim() !== expectedConfirm) return
    if (!reason.trim()) return
    const submitted = reason.trim()
    const note = target.action === 'ban' ? banNote.trim() : ''
    if ([...note].length > 255) {
      setActionError('追加备注连同原内容与风控标记合计最多 255 字。')
      return
    }
    const session = currentSession.current
    if (!actionAttempt.current || actionAttempt.current.reason !== submitted || actionAttempt.current.note !== note) {
      actionAttempt.current = { key: actionKey(), reason: submitted, note }
    }
    setActing(true)
    setActionError('')
    try {
      const deleteResult = deleteAction
        ? await requestJSON<LifecycleRecord>(`/api/users/${target.user.id}?hard_delete=${target.action === 'hard-delete'}`, {
          method: 'DELETE',
          headers: { 'Idempotency-Key': actionAttempt.current.key },
          body: JSON.stringify({ confirm_text: deleteConfirm.trim(), reason: submitted }),
        })
        : null
      const actionResult = !deleteAction
        ? await requestJSON<{ warning?: string; action?: { remark_sync?: { state: string; error?: string } } }>(`/api/users/${target.user.id}/${target.action}`, {
          method: 'POST',
          headers: { 'Idempotency-Key': actionAttempt.current.key },
          body: JSON.stringify(target.action === 'ban' ? { reason: submitted, note } : { reason: submitted }),
        })
        : null
      if (session !== currentSession.current) return
      if (!deleteAction && actionResult && (actionResult.warning || actionResult.action?.remark_sync && actionResult.action.remark_sync.state !== 'synced')) {
        setActionWarning(actionResult.warning || `账号处置已完成，但用户备注未确认同步：${actionResult.action?.remark_sync?.error || '请在风控中心的封禁列表检查用户备注。'}`)
      } else if (deleteAction) {
        setSuccess(`${target.user.username} ${target.action === 'hard-delete' ? '已彻底删除' : '已注销'}。`)
        setActionWarning(deleteResult?.warning ?? '')
      } else setSuccess(`${target.user.username} ${target.action === 'ban' ? '已封禁' : '已解封'}。`)
      setTarget(null)
      setSelected(null)
      setBanNote('')
      actionAttempt.current = null
      setRefresh(value => value + 1)
    } catch (cause) {
      if (session === currentSession.current) failed(cause, setActionError)
    } finally {
      setActing(false)
    }
  }

  async function previewPurge() {
    if (UI_PREVIEW_ONLY || purging) return
    setPurgeOpen(true)
    setPurgeConfirm('')
    setPurgeReason('')
    purgeAttempt.current = null
    setPurgeError('')
    try {
      const result = await requestJSON<SoftDeletedData>('/api/users/soft-deleted/purge', {
        method: 'POST',
        body: JSON.stringify({ dry_run: true }),
      })
      setSoftDeleted(result)
      if (!result.count) setPurgeOpen(false)
    } catch (cause) {
      setPurgeOpen(false)
      failed(cause, setPurgeError)
    }
  }

  async function confirmPurge(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY || purging || purgeConfirm.trim() !== '彻底删除' || !purgeReason.trim()) return
    const submittedReason = purgeReason.trim()
    const session = currentSession.current
    if (!purgeAttempt.current || purgeAttempt.current.reason !== submittedReason) {
      purgeAttempt.current = { key: actionKey(), reason: submittedReason }
    }
    setPurging(true)
    setPurgeError('')
    try {
      const result = await requestJSON<LifecycleRecord>('/api/users/soft-deleted/purge', {
        method: 'POST',
        headers: { 'Idempotency-Key': purgeAttempt.current.key },
        body: JSON.stringify({ dry_run: false, confirm_text: purgeConfirm.trim(), reason: submittedReason }),
      })
      if (session !== currentSession.current) return
      setSuccess(`已彻底清理 ${count(result.affected)} 个注销用户，操作记录已保存。`)
      setActionWarning(result.warning ?? '')
      purgeAttempt.current = null
      setPurgeOpen(false)
      setRefresh(value => value + 1)
    } catch (cause) {
      if (session === currentSession.current) failed(cause, setPurgeError)
    } finally {
      setPurging(false)
    }
  }

  function openDetails(user: UserRecord) {
    if (UI_PREVIEW_ONLY) return
    setDetailTab('overview')
    setSelected(user)
  }

  function openRiskDetails() {
    setDetailTab('risk')
    window.requestAnimationFrame(() => {
      detailContent.current?.closest('[role="dialog"]')?.scrollTo({ top: 0 })
    })
  }

  function renderActions(user: UserRecord) {
    return <div className="um-row-actions">
      <Button variant="ghost" size="sm" type="button" className="h-8 px-2" disabled={UI_PREVIEW_ONLY}
        onClick={() => openDetails(user)}
        aria-label={`查看 ${user.username} 的详情`}><Eye className="h-4 w-4 mr-1" />详情</Button>
      <Button variant="ghost" size="sm" className={cn('h-8 px-2', user.status === 1 ? 'text-destructive hover:text-destructive hover:bg-destructive/10' : 'text-emerald-600 hover:text-emerald-700 hover:bg-emerald-50')} type="button"
        disabled={UI_PREVIEW_ONLY || user.role >= 10 || (user.status !== 1 && user.status !== 2) || acting}
        title={user.role >= 10 ? '管理员账号受保护' : user.status === 1 ? '封禁账号' : '解除本站记录的封禁'}
        onClick={() => openAction(user, user.status === 1 ? 'ban' : 'unban')}>
        {user.status === 1 ? <ShieldBan className="h-4 w-4 mr-1" /> : <ShieldCheck className="h-4 w-4 mr-1" />}{user.status === 1 ? '封禁' : '解封'}
      </Button>
      <Button variant="ghost" size="sm" className="h-8 px-2 text-destructive hover:text-destructive hover:bg-destructive/10" type="button"
        disabled={UI_PREVIEW_ONLY || user.role >= 10 || acting}
        title={user.role >= 10 ? '管理员账号受保护' : '注销用户（软删除）'}
        onClick={() => openAction(user, 'soft-delete')}>
        <Trash2 className="h-4 w-4 mr-1" />注销
      </Button>
      <Button variant="ghost" size="sm" className="h-8 px-2 text-destructive hover:text-destructive hover:bg-destructive/10" type="button"
        disabled={UI_PREVIEW_ONLY || user.role >= 10 || acting}
        title={user.role >= 10 ? '管理员账号受保护' : '彻底删除用户及令牌'}
        onClick={() => openAction(user, 'hard-delete')}>
        <UserX className="h-4 w-4 mr-1" />彻底删除
      </Button>
    </div>
  }

  const shownUser = detail?.user
  const pages = visibleData ? Math.max(1, Math.ceil(visibleData.total / pageSize)) : 1
  const filterCount = (Object.keys(defaults) as (keyof Filters)[]).filter(key => filters[key] !== defaults[key]).length
  const activityRanges: Record<string, string> = {
    daily: '最近 24 小时内有调用', weekly: '最近调用距今超过 24 小时、不超过 7 天',
    monthly: '最近调用距今超过 7 天、不超过 30 天', silent: '最近调用距今超过 30 天、不超过 90 天',
    dormant: '最近调用距今超过 90 天', never: '历史覆盖完整且确认没有调用',
    unknown: '历史记录不足，无法确定最后调用情况',
  }
  function chooseActivity(value: string) {
    setDraft(current => ({ ...current, activity: value }))
    setFilters(current => ({ ...current, activity: value }))
    setPage(1)
  }
  function chooseStatus(value: string) {
    setDraft(current => ({ ...current, status: value }))
    setFilters(current => ({ ...current, status: value }))
    setPage(1)
  }

  const statsCards = <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
    <StatCard title="活跃用户" value={count(stats?.active_7d)} subValue="7 天内有业务请求" icon={UserCheck} color="green"
      onClick={() => chooseActivity('active_7d')} className={cn(filters.activity === 'active_7d' && 'ring-2 ring-primary ring-offset-2')} />
    <StatCard title="近期调用" value={count(stats?.activity.monthly)} subValue="7–30 天内有请求" icon={Clock} color="yellow"
      onClick={() => chooseActivity('monthly')} className={cn(filters.activity === 'monthly' && 'ring-2 ring-primary ring-offset-2')} />
    <StatCard title="长期未调用" value={count(stats?.activity.dormant)} subValue="超过 90 天无请求" icon={UserX} color="red"
      onClick={() => chooseActivity('dormant')} className={cn(filters.activity === 'dormant' && 'ring-2 ring-primary ring-offset-2')} />
    <StatCard title="从未调用" value={count(stats?.activity.never)} subValue="已确认注册后未使用" icon={Users} color="gray"
      onClick={() => chooseActivity('never')} className={cn(filters.activity === 'never' && 'ring-2 ring-primary ring-offset-2')} />
  </div>

  return <div className="um-page space-y-6 animate-in fade-in duration-500">
    <div className="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
      <div><h1 className="text-3xl font-bold">用户管理</h1><p className="text-muted-foreground mt-1">查看和管理用户账号、调用活跃与登录记录</p></div>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" className="h-9" type="button" disabled={UI_PREVIEW_ONLY}
          onClick={() => { setLifecyclePage(1); setLifecycleOpen(true) }}><History className="h-4 w-4 mr-2" />操作记录</Button>
        <Button variant="outline" size="sm" className="h-9" type="button" disabled={UI_PREVIEW_ONLY || exporting || loading || !data} onClick={() => void exportUsers()}>
          {exporting ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <Download className="h-4 w-4 mr-2" />}导出 CSV
        </Button>
        <Button variant="outline" size="sm" className="h-9" type="button" disabled={UI_PREVIEW_ONLY || loading} onClick={() => { if (!UI_PREVIEW_ONLY) setRefresh(value => value + 1) }}>
          <RefreshCw className={cn('h-4 w-4 mr-2', loading && 'animate-spin')} />刷新
        </Button>
      </div>
    </div>
    {!UI_PREVIEW_ONLY && error && <Notice kind="error">{error}</Notice>}
    {!UI_PREVIEW_ONLY && success && <Notice kind="success">{success}</Notice>}
    {!UI_PREVIEW_ONLY && softDeleted && softDeleted.count > 0 && <Card className="border-purple-200 bg-purple-50/60 dark:border-purple-900 dark:bg-purple-950/25">
      <CardContent className="flex flex-col gap-4 p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-purple-100 text-purple-700 dark:bg-purple-900 dark:text-purple-300"><Trash2 className="h-5 w-5" /></span>
          <div className="min-w-0">
            <p className="font-semibold text-purple-900 dark:text-purple-100">清理已注销用户</p>
            <p className="mt-1 text-sm text-purple-700 dark:text-purple-300">当前有 {count(softDeleted.count)} 个已注销账号，彻底清理会删除账号及其令牌，不可恢复。</p>
          </div>
        </div>
        <Button variant="outline" size="sm" className="border-purple-300 text-purple-800 hover:bg-purple-100 hover:text-purple-900 dark:border-purple-800 dark:text-purple-200 dark:hover:bg-purple-900" type="button" disabled={purging} onClick={() => void previewPurge()}>
          {purging ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <Trash2 className="h-4 w-4 mr-2" />}彻底清理注销用户
        </Button>
      </CardContent>
    </Card>}
    {!UI_PREVIEW_ONLY && actionWarning && <Notice kind="warning">{actionWarning}</Notice>}
    {visibleData && !visibleData.history_complete && <Notice kind="warning">历史调用记录尚未确认完整，未观察到请求的账号按“数据不足”展示。</Notice>}
    {visibleData?.activity_window_complete === false && <Notice kind="warning">近 30 日事件未完整覆盖，近期调用数量仅代表已保留的记录。</Notice>}
    <Tabs value={view} onValueChange={setView} className="w-full">
      <TabsList className="grid w-full max-w-sm grid-cols-2">
        <TabsTrigger value="list" className="gap-2"><Users className="h-4 w-4" />用户列表</TabsTrigger>
        <TabsTrigger value="activity" className="gap-2"><Activity className="h-4 w-4" />活跃分析</TabsTrigger>
      </TabsList>
      <TabsContent value="list" forceMount className="data-[state=inactive]:hidden mt-6 space-y-6">
        {statsCards}
        <Card className="um-list-card">
          <CardHeader className="pb-3">
            <CardTitle className="text-base font-medium flex items-center justify-between gap-3 flex-wrap">
              <span className="flex items-center gap-2"><Search className="w-4 h-4" />用户列表<span className="ml-2 text-sm font-normal text-muted-foreground">共 {count(visibleData?.total)} 个</span></span>
              {filterCount > 0 && <Button variant="ghost" size="sm" type="button" className="h-8 text-xs" onClick={resetFilters}><X className="h-3.5 w-3.5 mr-1" />清除筛选 {filterCount}</Button>}
            </CardTitle>
          </CardHeader>
          <CardContent className="min-w-0">
            <form onSubmit={applyFilters}>
              <div className="flex flex-col lg:flex-row gap-4 mb-4">
                <div className="flex-1 flex gap-2 min-w-0">
                  <div className="relative flex-1 max-w-sm">
                    <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground pointer-events-none" />
                    <Input aria-label="搜索账号" value={draft.search} onChange={event => setDraft(current => ({ ...current, search: event.target.value }))} placeholder="搜索用户名、昵称或邮箱..." className="pl-9" maxLength={128} />
                  </div>
                  <Button type="submit" disabled={UI_PREVIEW_ONLY || loading}>搜索</Button>
                </div>
                <div className="um-primary-selects">
                  <Select aria-label="账号状态" value={draft.status} onChange={event => chooseStatus(event.target.value)}>
                    <option value="all">所有账号状态</option><option value="enabled">启用账号</option><option value="disabled">已封禁账号</option>
                  </Select>
                  <Select aria-label="调用活跃" value={draft.activity} onChange={event => chooseActivity(event.target.value)}>
                    <option value="all">所有活跃状态</option><option value="active_7d">7 日内活跃</option><option value="active_30d">30 日内活跃</option>
                    {Object.entries(activityNames).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
                  </Select>
                  <Button variant={moreFilters ? 'secondary' : 'outline'} type="button" onClick={() => setMoreFilters(value => !value)} aria-expanded={moreFilters} aria-controls="um-more-filters">
                    <Filter className="h-4 w-4 mr-2" />更多筛选<ChevronDown className={cn('h-3.5 w-3.5 ml-2 transition-transform', moreFilters && 'rotate-180')} />
                  </Button>
                </div>
              </div>
              {moreFilters && <div id="um-more-filters" className="um-more-filters">
                <label className="um-field"><span>用户 ID</span><Input aria-label="用户 ID" inputMode="numeric" value={draft.user_id} onChange={event => setDraft(current => ({ ...current, user_id: event.target.value.replace(/\D/g, '') }))} placeholder="全部用户" maxLength={18} /></label>
                <label className="um-field"><span>用户分组</span><Input aria-label="用户分组" value={draft.group} onChange={event => setDraft(current => ({ ...current, group: event.target.value }))} placeholder="全部分组" maxLength={64} /></label>
                <label className="um-field"><span>7 日调用强度</span><Select aria-label="7 日调用强度" value={draft.intensity} onChange={event => setDraft(current => ({ ...current, intensity: event.target.value }))}><option value="all">全部调用强度</option>{Object.entries(intensityNames).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</Select></label>
                <label className="um-field"><span>排序方式</span><Select aria-label="排序方式" value={draft.sort} onChange={event => setDraft(current => ({ ...current, sort: event.target.value }))}><option value="id">用户 ID</option><option value="last_attempt_at">最近调用</option><option value="requests_7d">7 日请求数</option><option value="requests_30d">30 日请求数</option><option value="quota">余额</option></Select></label>
                <label className="um-field"><span>排列顺序</span><Select aria-label="排列顺序" value={draft.order} onChange={event => setDraft(current => ({ ...current, order: event.target.value }))}><option value="desc">从高到低</option><option value="asc">从低到高</option></Select></label>
                <div className="flex items-end gap-2">
                  <Button variant="outline" type="button" className="flex-1" onClick={resetFilters}>重置</Button>
                  <Button type="submit" className="flex-1" disabled={UI_PREVIEW_ONLY || loading}>应用筛选</Button>
                </div>
              </div>}
            </form>
            <div className="um-table-region">
              <Table className="um-table" containerClassName="scroll-region um-user-table-scroll" containerLabel="用户列表，可上下和左右滚动" aria-label="用户列表">
                <TableHeader><TableRow>
                  <TableHead className="w-16">ID</TableHead><TableHead>用户</TableHead><TableHead className="hidden lg:table-cell">角色</TableHead>
                  <TableHead>状态</TableHead><TableHead className="hidden md:table-cell">分组</TableHead><TableHead>调用活跃</TableHead>
                  <TableHead className="text-right">近 7 日请求</TableHead><TableHead className="text-right">余额</TableHead>
                  <TableHead className="hidden xl:table-cell text-right">已用额度</TableHead><TableHead className="hidden lg:table-cell">最近调用</TableHead><TableHead className="text-right">操作</TableHead>
                </TableRow></TableHeader>
                <TableBody>{!result.items.length ? <TableRow><TableCell colSpan={11} className="h-48 text-center">
                  <div className="um-table-empty" role="status">
                    {loading ? <><Loader2 className="h-8 w-8 animate-spin text-primary" /><span>正在读取用户数据…</span></>
                      : <><Users className="h-8 w-8 text-muted-foreground/50" /><span>{UI_PREVIEW_ONLY ? '数据获取已暂停' : error ? '用户数据暂时不可用' : '暂无符合条件的用户'}</span>
                        {!UI_PREVIEW_ONLY && <Button variant="ghost" size="sm" type="button" onClick={error ? () => setRefresh(value => value + 1) : resetFilters}>{error ? '重新读取' : '清空筛选'}</Button>}</>}
                  </div>
                </TableCell></TableRow> : result.items.map(user => <TableRow key={user.id}>
                  <TableCell className="font-mono text-xs text-muted-foreground">{user.id}</TableCell>
                  <TableCell><UserIdentityButton userId={user.id} username={user.username} displayName={user.display_name}
                    group={user.group} email={user.email} disabled={UI_PREVIEW_ONLY} className="um-list-identity"
                    onClick={() => openDetails(user)} /></TableCell>
                  <TableCell className="hidden lg:table-cell"><Badge variant={user.role >= 10 ? 'default' : 'secondary'} className="font-normal">{user.role >= 100 ? '超级管理员' : user.role >= 10 ? '管理员' : '普通用户'}</Badge></TableCell>
                  <TableCell><UserStatus user={user} /></TableCell>
                  <TableCell className="hidden md:table-cell"><Badge variant="outline" className="font-normal">{user.group || '—'}</Badge></TableCell>
                  <TableCell><ActivityBadge activity={user.call_activity.activity} /><span className="um-secondary">{intensityNames[user.call_activity.intensity_7d] ?? '—'}</span></TableCell>
                  <TableCell className="text-right tabular-nums"><span className="font-medium">{count(user.call_activity.requests_7d)}</span><span className="um-secondary">活跃 {count(user.call_activity.active_days_7d)} 天</span></TableCell>
                  <TableCell className="text-right font-mono text-xs whitespace-nowrap">{money(user.quota)}</TableCell>
                  <TableCell className="hidden xl:table-cell text-right font-mono text-xs whitespace-nowrap">{money(user.used_quota)}</TableCell>
                  <TableCell className="hidden lg:table-cell text-xs text-muted-foreground whitespace-nowrap">{timestamp(user.call_activity.last_attempt_at, timezone)}</TableCell>
                  <TableCell className="text-right">{renderActions(user)}</TableCell>
                </TableRow>)}</TableBody>
              </Table>
            </div>
            <div className="um-list-footer">
              <div className="flex items-center flex-wrap gap-3 text-sm text-muted-foreground">
                <span>共 {count(visibleData?.total)} 个用户</span>
                <div className="flex items-center gap-2"><span>每页</span><div className="w-20"><Select aria-label="每页用户数量" value={String(pageSize)} onChange={event => { setPageSize(Number(event.target.value)); setPage(1) }}><option value="10">10</option><option value="20">20</option><option value="50">50</option><option value="100">100</option></Select></div><span>条</span></div>
              </div>
              <div className="flex items-center gap-3">
                <span className="text-sm text-muted-foreground">第 {visibleData ? page : '—'} / {visibleData ? pages : '—'} 页</span>
                <div className="flex gap-2">
                  <Button variant="outline" size="sm" type="button" className="h-8 w-8 p-0" aria-label="上一页" disabled={!visibleData || loading || page <= 1} onClick={() => setPage(value => value - 1)}><ChevronLeft className="h-4 w-4" /></Button>
                  <Button variant="outline" size="sm" type="button" className="h-8 w-8 p-0" aria-label="下一页" disabled={!visibleData || loading || page >= pages} onClick={() => setPage(value => value + 1)}><ChevronRight className="h-4 w-4" /></Button>
                </div>
              </div>
            </div>
            <p className="mt-3 text-xs text-muted-foreground">{visibleData ? `统计于 ${timestamp(visibleData.as_of, timezone)} · 启用 ${count(stats?.enabled)} 个，已封禁 ${count(stats?.disabled)} 个` : '尚未读取用户记录'}</p>
          </CardContent>
        </Card>
      </TabsContent>
      <TabsContent value="activity" forceMount className="data-[state=inactive]:hidden mt-6 space-y-6">
        {statsCards}
        <Card><CardHeader className="pb-3"><CardTitle className="text-base font-medium flex items-center gap-2"><Activity className="h-4 w-4" />调用活跃分布</CardTitle><p className="text-sm text-muted-foreground">按最近一次业务调用划分，登录行为独立统计。</p></CardHeader>
          <CardContent><Table><TableHeader><TableRow><TableHead>活跃状态</TableHead><TableHead>划分条件</TableHead><TableHead className="text-right">用户数</TableHead><TableHead className="text-right">占比</TableHead></TableRow></TableHeader>
            <TableBody>{Object.entries(activityNames).map(([key, label]) => {
              const value = stats?.activity[key]
              const proportion = value != null && stats && stats.total > 0 ? value / stats.total * 100 : null
              return <TableRow key={key}><TableCell><button type="button" className="um-activity-link" onClick={() => { chooseActivity(key); setView('list') }}><ActivityBadge activity={key} /><span className="sr-only">筛选{label}</span></button></TableCell><TableCell className="text-muted-foreground text-sm">{activityRanges[key]}</TableCell><TableCell className="text-right tabular-nums">{count(value)}</TableCell><TableCell className="text-right tabular-nums">{proportion == null ? '—' : `${proportion.toFixed(1)}%`}</TableCell></TableRow>
            })}</TableBody></Table>
            <p className="mt-4 text-xs text-muted-foreground leading-relaxed">7 日活跃包含日活跃与周内调用，30 日活跃另包含月内调用。历史不完整时不将缺失记录当作从未调用。</p>
          </CardContent>
        </Card>
      </TabsContent>
    </Tabs>

    {!UI_PREVIEW_ONLY && selected && <AdminDialog title={`用户详情 · ${selected.username}`} onClose={() => setSelected(null)} wide>
      {detailLoading ? <div className="um-table-empty min-h-52" role="status"><Loader2 className="h-8 w-8 animate-spin text-primary" />正在读取详情…</div>
        : detailError ? <><Notice kind="error">{detailError}</Notice><Button variant="outline" type="button" onClick={() => setDetailRefresh(value => value + 1)}><RefreshCw className="h-4 w-4 mr-2" />重新读取</Button></>
          : shownUser && <div className="um-detail" ref={detailContent}>
            <div className="um-profile-hero">
              <div className="um-profile-hero-main">
                <span className="um-profile-avatar">{(shownUser.display_name || shownUser.username).slice(0, 1).toUpperCase()}</span>
                <div className="min-w-0">
                  <div className="um-profile-name-line">
                    <strong>{shownUser.display_name || shownUser.username}</strong>
                    <UserStatus user={shownUser} />
                  </div>
                  <small>{shownUser.username} · #{shownUser.id}</small>
                  <div className="um-profile-tags"><Badge variant="outline">{shownUser.group || 'default'}</Badge><ActivityBadge activity={shownUser.call_activity.activity} /><Badge variant="secondary">{intensityNames[shownUser.call_activity.intensity_7d] ?? '强度未知'}</Badge></div>
                </div>
              </div>
              <div className="um-profile-metrics" aria-label="用户关键状态">
                <DetailMetric label="余额" value={money(shownUser.quota)} />
                <DetailMetric label="24h 调用" value={count(shownUser.call_activity.requests_24h)} tone={shownUser.call_activity.requests_24h > 0 ? 'active' : 'default'} />
                <DetailMetric label="30d 登录" value={count(shownUser.login_activity.successes_30d)} />
                <DetailMetric label="最近调用" value={relativeTime(shownUser.call_activity.last_attempt_at, timezone).text} hint={timestamp(shownUser.call_activity.last_attempt_at, timezone)} />
              </div>
            </div>
            <Tabs value={detailTab} onValueChange={setDetailTab} className="um-detail-tabs">
              <TabsList className="um-detail-tab-list"><TabsTrigger value="overview" className="flex-1">账号概览</TabsTrigger><TabsTrigger value="usage" className="flex-1">调用活跃</TabsTrigger><TabsTrigger value="login" className="flex-1">登录与设备</TabsTrigger><TabsTrigger value="risk" className="flex-1">风控分析</TabsTrigger><TabsTrigger value="checkins" className="flex-1">签到分析</TabsTrigger></TabsList>
              <TabsContent value="overview" className="mt-5">
                <section className="um-detail-section um-detail-section--overview">
                  <header><h4>账号资产</h4><p>NewAPI 原始账号信息与额度状态</p></header>
                  <dl className="um-detail-grid">
                    <DetailMetric label="邮箱" value={shownUser.email || '—'} />
                    <DetailMetric label="用户角色" value={shownUser.role >= 100 ? '超级管理员' : shownUser.role >= 10 ? '管理员' : '普通用户'} />
                    <DetailMetric label="分组" value={shownUser.group || '—'} />
                    <DetailMetric label="余额" value={money(shownUser.quota)} />
                    <DetailMetric label="已用额度" value={money(shownUser.used_quota)} />
                    <DetailMetric label="注册时间" value={timestamp(shownUser.created_at, timezone)} />
                    <DetailMetric label="上游累计请求" value={count(shownUser.upstream_request_count)} />
                  </dl>
                </section>
                <section className="um-detail-note">
                  <h4>NewAPI 用户备注 · 仅管理员可见</h4>
                  <p>{shownUser.remark_supported === false ? '当前 NewAPI 不支持用户备注' : shownUser.remark || '暂无备注'}</p>
                </section>
              </TabsContent>
              <TabsContent value="usage" className="mt-5">
                <section className="um-detail-section um-detail-section--usage">
                  <header><h4>调用活跃</h4><p>登录与调用分开统计，调用口径来自使用日志</p></header>
                  <div className="um-detail-inline-badges"><ActivityBadge activity={shownUser.call_activity.activity} /><Badge variant="secondary">7 日强度：{intensityNames[shownUser.call_activity.intensity_7d] ?? '—'}</Badge></div>
                  <dl className="um-detail-grid">
                    <DetailMetric label="24 小时请求" value={count(shownUser.call_activity.requests_24h)} tone={shownUser.call_activity.requests_24h > 0 ? 'active' : 'default'} />
                    <DetailMetric label="7 日请求" value={count(shownUser.call_activity.requests_7d)} />
                    <DetailMetric label="30 日请求" value={count(shownUser.call_activity.requests_30d)} />
                    <DetailMetric label="7 日活跃天数" value={count(shownUser.call_activity.active_days_7d)} />
                    <DetailMetric label="30 日活跃天数" value={count(shownUser.call_activity.active_days_30d)} />
                    <DetailMetric label="30 日成功 / 失败" value={`${count(shownUser.call_activity.successes_30d)} / ${count(shownUser.call_activity.failures_30d)}`} tone={shownUser.call_activity.failures_30d > 0 ? 'warning' : 'default'} />
                    <DetailMetric label="最近调用" value={relativeTime(shownUser.call_activity.last_attempt_at, timezone).text} hint={timestamp(shownUser.call_activity.last_attempt_at, timezone)} />
                    <DetailMetric label="最近成功调用" value={relativeTime(shownUser.call_activity.last_success_at, timezone).text} hint={timestamp(shownUser.call_activity.last_success_at, timezone)} />
                  </dl>
                  <p className="um-detail-note-line">活跃天数按实例时区自然日计算，调用数量按滚动窗口计算。</p>
                </section>
              </TabsContent>
              <TabsContent value="login" className="mt-5 space-y-5">
                <section className="um-detail-section um-detail-section--login">
                  <header><h4>登录观测</h4><p>登录 IP 与调用 IP 分离统计，不互相替代</p></header>
                  <dl className="um-detail-grid">
                    <DetailMetric label="30 日成功登录" value={count(shownUser.login_activity.successes_30d)} tone={shownUser.login_activity.successes_30d > 0 ? 'active' : 'default'} />
                    <DetailMetric label="30 日已关联失败登录" value={count(shownUser.login_activity.failures_30d)} tone={shownUser.login_activity.failures_30d > 0 ? 'warning' : 'default'} />
                    <DetailMetric label="最近观测成功登录" value={relativeTime(shownUser.login_activity.last_success_at, timezone).text} hint={timestamp(shownUser.login_activity.last_success_at, timezone)} />
                    <DetailMetric label="上游最近登录" value={timestamp(shownUser.upstream_last_login_at, timezone)} />
                  </dl>
                </section>
                <UserLoginObservations userId={shownUser.id} username={shownUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
              </TabsContent>
              <TabsContent value="risk" className="mt-5">
                <UserRiskAnalysisPanel userId={shownUser.id} username={shownUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
              </TabsContent>
              <TabsContent value="checkins" className="mt-5">
                <UserCheckinPanel userId={shownUser.id} username={shownUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} />
              </TabsContent>
            </Tabs>
            {!detail?.history_complete && <Notice kind="warning">历史覆盖尚未确认完整，缺失记录不能证明账号从未调用或从未登录。</Notice>}
            {detail?.activity_window_complete === false && <Notice kind="warning">近 30 日事件覆盖不足，调用次数和活跃天数仅代表已保留的记录。</Notice>}
            <div className="um-dialog-actions"><Button variant="outline" size="sm" type="button" onClick={openRiskDetails} aria-pressed={detailTab === 'risk'}><ShieldCheck className="h-4 w-4 mr-2" />风控分析</Button><Button size="sm" type="button" onClick={() => setSelected(null)}>关闭</Button></div>
          </div>}
    </AdminDialog>}

    {!UI_PREVIEW_ONLY && target && <AdminDialog title={target.action === 'ban' ? '封禁账号' : target.action === 'unban' ? '解除封禁' : target.action === 'soft-delete' ? '注销用户' : '彻底删除用户'} onClose={() => { if (!acting) setTarget(null) }}>
      <form className="um-action-form" onSubmit={event => void confirmAction(event)}>
        <div className={cn('flex items-center gap-3 p-3 rounded-lg border', target.action === 'ban' || target.action !== 'unban' ? 'bg-destructive/5 border-destructive/20' : 'bg-emerald-50 border-emerald-200 dark:bg-emerald-950/20 dark:border-emerald-900')}>
          {target.action === 'ban' ? <ShieldBan className="h-6 w-6 text-destructive" /> : target.action === 'unban' ? <ShieldCheck className="h-6 w-6 text-emerald-600" /> : <Trash2 className="h-6 w-6 text-destructive" />}<div className="min-w-0"><p className="font-medium break-all">{target.user.username}</p><p className="text-xs text-muted-foreground mt-1">用户 #{target.user.id}</p></div>
        </div>
        <p className="text-sm text-muted-foreground leading-relaxed">{target.action === 'ban' ? '封禁后该账号将无法继续使用服务，现有会话将由 NewAPI 撤销。' : target.action === 'unban' ? '仅解除本站持有且状态未发生冲突的封禁，不恢复原本禁用或已过期的令牌。' : target.action === 'soft-delete' ? '注销会停用账号和有效令牌，并写入注销时间。操作原因与结果会持久保存；恢复需人工处理。' : '彻底删除会移除用户和关联令牌，操作后不可恢复。历史调用、财务与风控证据保留，操作原因与结果会持久保存。'}</p>
        {actionError && <Notice kind="error">{actionError}</Notice>}
        {target.action !== 'unban' && target.action !== 'ban' && <label className="um-field"><span>确认文本 <span className="text-destructive">*</span></span><Input aria-label="确认文本" value={deleteConfirm} disabled={acting} onChange={event => setDeleteConfirm(event.target.value)} placeholder={target.action === 'soft-delete' ? '注销用户' : '彻底删除'} autoFocus maxLength={16} /><small className="text-xs text-muted-foreground">请输入「{target.action === 'soft-delete' ? '注销用户' : '彻底删除'}」确认操作。</small></label>}
        <label className="um-field"><span>操作原因 <span className="text-destructive">*</span></span><textarea aria-label="操作原因" className="um-textarea" rows={3} maxLength={500} required disabled={acting} value={reason} onChange={event => setReason(event.target.value)} placeholder={target.action === 'ban' ? '填写封禁原因' : target.action === 'unban' ? '填写解除封禁的原因' : '填写删除原因，记录将保留在用户操作记录与风控审计日志'} /></label>
        {target.action === 'ban' && <label className="um-field"><span>追加用户备注（选填）</span><textarea aria-label="追加用户备注" className="um-textarea" rows={2} disabled={acting} value={banNote} onChange={event => setBanNote(event.target.value)} placeholder="补充复核细节或后续跟进事项" /><small className={`text-xs ${[...banNote.trim()].length > 255 ? 'text-destructive' : 'text-muted-foreground'}`}>已填写 {[...banNote.trim()].length} 字。保留 NewAPI 原备注并追加风控说明，仅管理员可见；原内容、标记与本次追加合计最多 255 字，超长会提示修改。</small></label>}
        <div className="um-dialog-actions"><Button variant="outline" type="button" disabled={acting} onClick={() => setTarget(null)}>取消</Button><Button variant={target.action === 'unban' ? 'default' : 'destructive'} type="submit" disabled={acting || !reason.trim() || (target.action !== 'unban' && target.action !== 'ban' && deleteConfirm.trim() !== (target.action === 'soft-delete' ? '注销用户' : '彻底删除'))}>{acting ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : target.action === 'unban' ? <ShieldCheck className="h-4 w-4 mr-2" /> : <ShieldBan className="h-4 w-4 mr-2" />}{acting ? '正在处理…' : target.action === 'ban' ? '确认封禁' : target.action === 'unban' ? '确认解封' : target.action === 'soft-delete' ? '确认注销' : '确认彻底删除'}</Button></div>
      </form>
    </AdminDialog>}

    {!UI_PREVIEW_ONLY && purgeOpen && <AdminDialog title="清理已注销用户" onClose={() => { if (!purging) setPurgeOpen(false) }}>
      <form className="um-action-form" onSubmit={event => void confirmPurge(event)}>
        <div className="flex items-center gap-3 rounded-lg border border-destructive/20 bg-destructive/5 p-3">
          <Trash2 className="h-6 w-6 text-destructive" />
          <div className="min-w-0">
            <p className="font-medium">将彻底清理 {count(softDeleted?.count)} 个已注销用户</p>
            <p className="mt-1 text-xs text-muted-foreground">账号及其令牌会被永久删除，操作后不可恢复。</p>
          </div>
        </div>
        {softDeleted?.users?.length ? <div className="rounded-lg border bg-muted/30 p-3">
          <p className="text-xs font-medium text-muted-foreground">示例用户（最多 20 个）</p>
          <p className="mt-2 break-words text-sm">{softDeleted.users.join('、')}</p>
          {softDeleted.sample && <p className="mt-2 text-xs text-muted-foreground">仅显示前 20 个，实际清理数量以上方统计为准。</p>}
        </div> : null}
        {purgeError && <Notice kind="error">{purgeError}</Notice>}
        <label className="um-field"><span>清理原因 <span className="text-destructive">*</span></span><textarea aria-label="清理原因" className="um-textarea" rows={3} maxLength={500} required disabled={purging} value={purgeReason} onChange={event => setPurgeReason(event.target.value)} placeholder="填写本次批量清理原因，系统会记录实际删除账号与结果" /></label>
        <label className="um-field"><span>确认文本 <span className="text-destructive">*</span></span><Input aria-label="清理确认文本" value={purgeConfirm} disabled={purging} onChange={event => setPurgeConfirm(event.target.value)} placeholder="彻底删除" autoFocus maxLength={16} /><small className="text-xs text-muted-foreground">请输入「彻底删除」确认本次批量清理。</small></label>
        <div className="um-dialog-actions"><Button variant="outline" type="button" disabled={purging} onClick={() => setPurgeOpen(false)}>取消</Button><Button variant="destructive" type="submit" disabled={purging || !purgeReason.trim() || purgeConfirm.trim() !== '彻底删除'}>{purging ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <Trash2 className="h-4 w-4 mr-2" />}{purging ? '正在清理…' : '确认清理'}</Button></div>
      </form>
    </AdminDialog>}

    {!UI_PREVIEW_ONLY && lifecycleOpen && <AdminDialog title="用户操作记录" onClose={() => setLifecycleOpen(false)}>
      <div className="space-y-4">
        <p className="text-sm leading-relaxed text-muted-foreground">注销、彻底删除与批量清理均保存原因、账号快照和执行结果；记录也同步进入风控审计日志。共享管理令牌下操作者记为 admin。</p>
        {lifecycleError && <Notice kind="error">{lifecycleError}</Notice>}
        {lifecycleLoading ? <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />读取操作记录…</div>
          : lifecycleData?.items.length ? <div className="scroll-region scroll-region--lg space-y-3">
            {lifecycleData.items.map(record => <article key={record.id} className="rounded-xl border bg-card p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2"><History className="h-4 w-4 text-muted-foreground" /><strong className="text-sm">{lifecycleActionNames[record.action] ?? record.action}</strong></div>
                <Badge variant={record.state === 'completed' ? 'success' : record.state === 'pending' ? 'secondary' : 'destructive'}>{record.state === 'completed' ? '已完成' : record.state === 'pending' ? '处理中' : '待核实'}</Badge>
              </div>
              <p className="mt-2 text-xs text-muted-foreground">{timestamp(record.updated_at, timezone)} · {record.operator} · 实际影响 {count(record.affected)} 个账号</p>
              <p className="mt-3 break-words text-sm">{record.reason}</p>
              <div className="mt-3 flex flex-wrap gap-1.5">
                {record.usernames.slice(0, 20).map((name, index) => <Badge key={`${record.user_ids[index]}-${index}`} variant="outline" className="font-normal">{name} #{record.user_ids[index]}</Badge>)}
                {record.usernames.length > 20 && <Badge variant="secondary">另 {count(record.usernames.length - 20)} 个账号</Badge>}
                {!record.usernames.length && record.user_id ? <Badge variant="outline">用户 #{record.user_id}</Badge> : null}
              </div>
              {record.error && <p className="mt-3 break-words text-sm text-destructive">{record.error}</p>}
              {record.warning && <p className="mt-3 break-words text-xs leading-relaxed text-amber-700 dark:text-amber-300">{record.warning}</p>}
            </article>)}
          </div> : !lifecycleError && <div className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">暂无注销或删除记录</div>}
        <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
          <span className="text-xs text-muted-foreground">共 {count(lifecycleData?.total)} 条 · 第 {lifecyclePage} 页</span>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" disabled={lifecycleLoading || lifecyclePage <= 1} onClick={() => setLifecyclePage(value => value - 1)}><ChevronLeft className="mr-1 h-4 w-4" />上一页</Button>
            <Button variant="outline" size="sm" disabled={lifecycleLoading || !lifecycleData || lifecyclePage * 20 >= lifecycleData.total} onClick={() => setLifecyclePage(value => value + 1)}>下一页<ChevronRight className="ml-1 h-4 w-4" /></Button>
          </div>
        </div>
      </div>
    </AdminDialog>}
  </div>
}
