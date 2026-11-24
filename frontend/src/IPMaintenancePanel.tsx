import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { CheckCircle2, ChevronDown, Database, Download, FileUp, Loader2, RefreshCw, Save, Settings2, ShieldCheck, Trash2 } from 'lucide-react'
import { ApiError, authenticatedHeaders, notifyUnauthorized, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { cn } from './lib/utils'

type Settings = { auto_update: boolean; interval_hours: number; proxy_url: string; retention_days: number }
type Maintenance = {
  settings: Settings; state: string; stage: string; started_at: number; updated_at: number
  last_success: number; last_error: string; version: string; next_update_at: number
}
type Intelligence = {
  configured: boolean; available: boolean; loaded_at: number; version: string
  files: { name: string; status: string; prefix_count: number; error?: string }[]
  warnings: string[]
  geo?: { city_available: boolean; asn_available: boolean; isp_available: boolean; anonymous_available: boolean }
}
type Snapshot = { maintenance: Maintenance; intelligence: Intelligence }
type RetentionPreview = {
  days: number; cutoff: number; events: number; source_counts: Record<string, number>; history_complete_after: false
}

const defaultSettings: Settings = { auto_update: false, interval_hours: 24, proxy_url: '', retention_days: 0 }
const stageNames: Record<string, string> = {
  queued: '等待执行', downloading: '下载数据', download: '下载数据', validating: '校验数据',
  validate: '校验数据', installing: '安装数据', install: '安装数据', reloading: '重载数据',
  reload: '重载数据', complete: '更新完成', completed: '更新完成', done: '更新完成', idle: '等待任务',
}
const sourceNames: Record<string, string> = { api_call: '调用事件', login: '登录事件', registration: '注册事件' }
const counts = new Intl.NumberFormat('zh-CN')
const stamp = (value?: number) => value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '暂无记录'
const isRunning = (state?: string) => state === 'running' || state === 'queued' || state === 'importing'

export default function IPMaintenancePanel({ onUnauthorized, onChanged }: {
  onUnauthorized: () => void; onChanged?: () => void
}) {
  const [open, setOpen] = useState(false)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [draft, setDraft] = useState<Settings>(defaultSettings)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [importKind, setImportKind] = useState('isp')
  const [importFile, setImportFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<RetentionPreview | null>(null)
  const [cleanupOpen, setCleanupOpen] = useState(false)
  const [cleanupReason, setCleanupReason] = useState('')
  const [cleanupConfirm, setCleanupConfirm] = useState('')
  const uploadInput = useRef<HTMLInputElement>(null)
  const settingsEdited = useRef(false)
  const callbacks = useRef({ onUnauthorized, onChanged })
  callbacks.current = { onUnauthorized, onChanged }
  const running = isRunning(snapshot?.maintenance.state)
  const locked = Boolean(busy) || loading || running || UI_PREVIEW_ONLY
  const unsaved = Boolean(snapshot && JSON.stringify(draft) !== JSON.stringify(snapshot.maintenance.settings))

  function failure(cause: unknown) {
    if (cause instanceof ApiError && cause.status === 401) callbacks.current.onUnauthorized()
    else setError(cause instanceof Error ? cause.message : 'IP 数据维护失败')
  }

  async function reloadSnapshot(resetDraft = false) {
    const next = await requestJSON<Snapshot>('/api/ip/maintenance')
    setSnapshot(next)
    if (resetDraft) {
      settingsEdited.current = false
      setDraft(next.maintenance.settings)
    }
  }

  useEffect(() => {
    if (!open || UI_PREVIEW_ONLY) return
    const abort = new AbortController()
    setLoading(true)
    void requestJSON<Snapshot>('/api/ip/maintenance', {}, abort.signal)
      .then(next => {
        if (abort.signal.aborted) return
        setSnapshot(next)
        // Polling preserves settings that the administrator is editing.
        if (!settingsEdited.current) setDraft(next.maintenance.settings)
      })
      .catch(cause => {
        if (abort.signal.aborted) return
        if (cause instanceof ApiError && cause.status === 401) callbacks.current.onUnauthorized()
        else setError(cause instanceof Error ? cause.message : '读取维护状态失败')
      })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [open, refresh])

  function editSettings(change: (current: Settings) => Settings) {
    settingsEdited.current = true
    setDraft(change)
  }

  useEffect(() => {
    if (!open || !running || UI_PREVIEW_ONLY) return
    const timer = window.setTimeout(() => setRefresh(value => value + 1), 3000)
    return () => window.clearTimeout(timer)
  }, [open, running, snapshot?.maintenance.updated_at, refresh])

  async function run(operation: string, work: () => Promise<void>, text: string, resetDraft = false) {
    if (locked) return
    setBusy(operation)
    setError('')
    setSuccess('')
    try {
      await work()
      if (text) setSuccess(text)
      await reloadSnapshot(resetDraft)
      callbacks.current.onChanged?.()
    } catch (cause) {
      failure(cause)
    } finally {
      setBusy('')
    }
  }

  async function saveSettings(event: FormEvent) {
    event.preventDefault()
    if (!Number.isInteger(draft.interval_hours) || draft.interval_hours < 6 || draft.interval_hours > 720 ||
      !Number.isInteger(draft.retention_days) || draft.retention_days < 0 || draft.retention_days > 3650 ||
      (draft.retention_days > 0 && draft.retention_days < 30)) {
      setError('更新间隔使用 6–720 小时整数；事件保留时间设为 0（永久）或 30–3650 天。')
      return
    }
    const settings = { ...draft, proxy_url: draft.proxy_url.trim() }
    await run('settings', async () => {
      await requestJSON('/api/ip/maintenance', { method: 'PUT', body: JSON.stringify(settings) })
      setPreview(null)
    }, 'IP 数据维护配置已保存。', true)
  }

  async function importDatabase(event: FormEvent) {
    event.preventDefault()
    if (!importFile || locked) return
    await run('import', async () => {
      const form = new FormData()
      form.append('file', importFile)
      const headers = authenticatedHeaders(undefined, 'POST')
      const response = await fetch(`${import.meta.env.VITE_API_BASE ?? ''}/api/ip/intelligence/import?kind=${encodeURIComponent(importKind)}`, {
        method: 'POST', headers, body: form, credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      })
      notifyUnauthorized(response.status, '/api/ip/intelligence/import')
      const payload = await response.json().catch(() => null) as { success?: boolean; error?: string; message?: string } | null
      if (!response.ok || payload?.success === false) throw new ApiError(payload?.error || payload?.message || `导入失败 (${response.status})`, response.status)
      if (payload?.success !== true) throw new Error('导入结果无法确认，请刷新数据库状态。')
      setImportFile(null)
      if (uploadInput.current) uploadInput.current.value = ''
    }, '数据库已导入并重载，相关 IP 查询会使用新数据。')
  }

  async function previewCleanup() {
    if (locked || unsaved || !snapshot?.maintenance.settings.retention_days) return
    setBusy('preview')
    setError('')
    try {
      const result = await requestJSON<RetentionPreview>('/api/ip/retention/preview')
      setPreview(result)
      setCleanupReason('')
      setCleanupConfirm('')
      setCleanupOpen(true)
    } catch (cause) {
      failure(cause)
    } finally {
      setBusy('')
    }
  }

  async function cleanup(event: FormEvent) {
    event.preventDefault()
    if (!preview || locked || cleanupConfirm !== '清理过期事件' || !cleanupReason.trim()) return
    await run('cleanup', async () => {
      const result = await requestJSON<{ deleted: number; cutoff: number }>('/api/ip/retention/cleanup', {
        method: 'POST',
        body: JSON.stringify({ confirm: cleanupConfirm, cutoff: preview.cutoff, reason: cleanupReason.trim() }),
      })
      setCleanupOpen(false)
      setPreview(null)
      setSuccess(`已清理 ${counts.format(result.deleted)} 条过期事件，截止 ${stamp(result.cutoff)}。`)
    }, '')
  }

  const maintenance = snapshot?.maintenance
  const intelligence = snapshot?.intelligence
  const stateName = running ? '更新中' : maintenance?.state === 'failed' || maintenance?.last_error ? '需检查' : maintenance?.last_success ? '已就绪' : '待配置'

  return <section className="overflow-hidden rounded-xl border bg-card">
    <button type="button" className="flex w-full flex-wrap items-center justify-between gap-3 px-5 py-4 text-left transition-colors hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      aria-expanded={open} onClick={() => setOpen(value => { if (!value) settingsEdited.current = false; return !value })}>
      <span className="flex min-w-0 items-center gap-3">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary"><Database className="h-5 w-5" /></span>
        <span><span className="block text-sm font-semibold">IP 数据库与维护</span><span className="mt-1 block text-xs text-muted-foreground">情报更新 · ISP / 匿名网络库 · 事件保留</span></span>
      </span>
      <span className="ml-auto flex items-center gap-3"><Badge variant={running ? 'secondary' : stateName === '需检查' ? 'destructive' : 'outline'}>{running && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}{stateName}</Badge><ChevronDown className={cn('h-4 w-4 text-muted-foreground transition-transform', open && 'rotate-180')} /></span>
    </button>
    {open && <div className="space-y-5 border-t p-5">
      {error && <Notice kind="error">{error}</Notice>}
      {success && <div role="status" className="flex gap-2 rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/25 dark:text-emerald-300"><CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0" />{success}</div>}
      {!snapshot && loading ? <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />读取维护状态…</div> : snapshot && <>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {([
            ['城市归属地', intelligence?.geo?.city_available], ['ASN', intelligence?.geo?.asn_available],
            ['运营商 ISP', intelligence?.geo?.isp_available], ['匿名网络库', intelligence?.geo?.anonymous_available],
          ] as const).map(([name, available]) => <div key={name} className="flex items-center justify-between gap-2 rounded-lg border bg-muted/20 p-3"><span className="text-xs font-medium">{name}</span><Badge variant={available ? 'success' : 'outline'}>{available ? '已加载' : '未配置'}</Badge></div>)}
        </div>
        {running && <div role="status" className="flex items-center gap-3 rounded-lg border border-primary/20 bg-primary/5 p-3"><Loader2 className="h-4 w-4 animate-spin text-primary" /><div className="text-sm"><p>{stageNames[maintenance?.stage ?? ''] ?? maintenance?.stage ?? '执行更新'}</p><p className="mt-1 text-xs text-muted-foreground">开始：{stamp(maintenance?.started_at)} · 任务完成前自动刷新进度</p></div></div>}
        {maintenance?.last_error && <Notice kind="warning">{maintenance.last_error}</Notice>}
        <form onSubmit={event => void saveSettings(event)} className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="flex items-center gap-2 text-sm font-semibold"><Settings2 className="h-4 w-4 text-muted-foreground" />更新与保留配置</h3><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={draft.auto_update} disabled={locked} onChange={event => editSettings(current => ({ ...current, auto_update: event.target.checked }))} className="h-4 w-4 accent-primary" />自动更新情报</label></div>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <label className="grid gap-1.5 text-xs text-muted-foreground"><span>更新间隔（6–720 小时）</span><Input type="number" min={6} max={720} step={1} value={draft.interval_hours} disabled={locked} onChange={event => editSettings(current => ({ ...current, interval_hours: Number(event.target.value) }))} /></label>
            <label className="grid gap-1.5 text-xs text-muted-foreground"><span>事件保留天数（0 永久，至少 30 天）</span><Input type="number" min={0} max={3650} step={1} value={draft.retention_days} disabled={locked} onChange={event => { editSettings(current => ({ ...current, retention_days: Number(event.target.value) })); setPreview(null) }} /></label>
            <label className="grid gap-1.5 text-xs text-muted-foreground sm:col-span-2 lg:col-span-1"><span>更新下载代理（留空直连）</span><Input value={draft.proxy_url} maxLength={2048} disabled={locked} placeholder="http://127.0.0.1:7890" onChange={event => editSettings(current => ({ ...current, proxy_url: event.target.value }))} autoComplete="off" /></label>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-xs text-muted-foreground">最近更新：{stamp(maintenance?.last_success)}<br className="sm:hidden" /><span className="hidden sm:inline"> · </span>下次更新：{draft.auto_update ? stamp(maintenance?.next_update_at) : '已关闭'}</p>
            <Button size="sm" variant="outline" type="submit" disabled={locked || !unsaved}>{busy === 'settings' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}保存配置</Button>
          </div>
        </form>
        <div className="space-y-3 border-t pt-4">
          <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="text-sm font-semibold">在线情报与本地数据库</h3><div className="flex flex-wrap gap-2">
            <Button size="sm" type="button" disabled={locked || unsaved} onClick={() => void run('update', async () => { await requestJSON('/api/ip/intelligence/update', { method: 'POST' }) }, '情报更新任务已提交。')}>{busy === 'update' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Download className="mr-2 h-4 w-4" />}立即更新</Button>
            <Button size="sm" type="button" variant="outline" disabled={locked} onClick={() => void run('reload', async () => { await requestJSON('/api/ip/intelligence/reload', { method: 'POST' }) }, '本地 IP 数据库已重载。')}>{busy === 'reload' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <RefreshCw className="mr-2 h-4 w-4" />}重载本地文件</Button>
          </div></div>
          <p className="text-xs leading-relaxed text-muted-foreground">在线更新用于代理、非住宅网络与 Tor 出口情报。ISP 和匿名网络 MMDB 文件通过下方导入；城市与 ASN 数据库状态在上方显示。</p>
          {intelligence?.files?.length ? <div className="grid gap-2 sm:grid-cols-2">{intelligence.files.map(file => <div key={file.name} className="rounded-lg border bg-muted/15 px-3 py-2"><div className="flex min-w-0 items-center justify-between gap-2"><span className="truncate font-mono text-xs" title={file.name}>{file.name}</span><Badge variant={file.status === 'loaded' ? 'success' : file.status === 'invalid' ? 'destructive' : 'outline'}>{file.status === 'loaded' ? counts.format(file.prefix_count) + ' 条' : file.status === 'invalid' ? '文件无效' : '未导入'}</Badge></div>{file.error && <p className="mt-1 break-words text-xs text-destructive">{file.error}</p>}</div>)}</div> : null}
          <form onSubmit={event => void importDatabase(event)} className="flex flex-col gap-2 rounded-lg border border-dashed p-3 sm:flex-row sm:items-center">
            <Select aria-label="导入数据库类型" className="sm:w-40" value={importKind} disabled={locked} onChange={event => setImportKind(event.target.value)}><option value="isp">ISP 运营商库</option><option value="anonymous">匿名网络库</option></Select>
            <Input ref={uploadInput} aria-label="MMDB 数据库文件" type="file" accept=".mmdb,application/octet-stream" className="min-w-0 flex-1 text-xs" disabled={locked} onChange={event => setImportFile(event.target.files?.[0] ?? null)} />
            <Button size="sm" variant="outline" type="submit" disabled={locked || !importFile}>{busy === 'import' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <FileUp className="mr-2 h-4 w-4" />}导入并加载</Button>
          </form>
          {!!intelligence?.warnings?.length && <p className="break-words text-xs leading-relaxed text-amber-700 dark:text-amber-300">{intelligence.warnings.join('；')}</p>}
        </div>
        <div className="flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs leading-relaxed text-muted-foreground"><p className="mb-1 flex items-center gap-1.5 font-medium text-foreground"><ShieldCheck className="h-3.5 w-3.5" />保留策略与人工清理</p>保留时间不会触发自动删除。手动清理只删除过期事件，清理后历史覆盖标记为不完整。</div>
          <Button size="sm" variant="outline" className="shrink-0 text-destructive hover:text-destructive" type="button" disabled={locked || unsaved || !maintenance?.settings.retention_days} onClick={() => void previewCleanup()}>{busy === 'preview' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Trash2 className="mr-2 h-4 w-4" />}预览过期事件</Button>
        </div>
      </>}
      {!loading && !snapshot && <Button variant="outline" size="sm" type="button" disabled={UI_PREVIEW_ONLY} onClick={() => setRefresh(value => value + 1)}><RefreshCw className="mr-2 h-4 w-4" />重新读取维护状态</Button>}
    </div>}
    {cleanupOpen && preview && <AdminDialog title="清理过期 IP 事件" onClose={() => { if (!busy) setCleanupOpen(false) }}>
      <form onSubmit={event => void cleanup(event)} className="space-y-4">
        <Notice kind="warning">将删除 {stamp(preview.cutoff)} 之前的事件，共 {counts.format(preview.events)} 条。清理后历史覆盖不再完整，删除的事件无法恢复。</Notice>
        <div className="grid grid-cols-3 gap-2">{Object.entries(sourceNames).map(([source, name]) => <div key={source} className="rounded-lg border bg-muted/20 p-3 text-center"><p className="text-xs text-muted-foreground">{name}</p><p className="mt-1 text-lg font-semibold tabular-nums">{counts.format(preview.source_counts[source] ?? 0)}</p></div>)}</div>
        <label className="grid gap-1.5 text-sm"><span>清理原因 <span className="text-destructive">*</span></span><textarea required rows={3} maxLength={500} disabled={Boolean(busy)} value={cleanupReason} onChange={event => setCleanupReason(event.target.value)} className="rounded-lg border bg-background px-3 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring" placeholder="填写清理原因" /></label>
        <label className="grid gap-1.5 text-sm"><span>输入「清理过期事件」确认</span><Input value={cleanupConfirm} disabled={Boolean(busy)} maxLength={20} onChange={event => setCleanupConfirm(event.target.value)} placeholder="清理过期事件" /></label>
        {error && <Notice kind="error">{error}</Notice>}
        <div className="flex justify-end gap-2 border-t pt-4"><Button variant="outline" type="button" disabled={Boolean(busy)} onClick={() => setCleanupOpen(false)}>取消</Button><Button variant="destructive" type="submit" disabled={locked || preview.events < 1 || !cleanupReason.trim() || cleanupConfirm !== '清理过期事件'}>{busy === 'cleanup' && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}确认清理</Button></div>
      </form>
    </AdminDialog>}
  </section>
}
