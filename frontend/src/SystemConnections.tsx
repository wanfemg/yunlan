import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, CircleDashed, Database, ExternalLink, KeyRound, Loader2, PlugZap, RefreshCw, Save, Server, ShieldCheck } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import SystemReadinessPanel from './SystemReadinessPanel'

type Check = { id: string; state: string; message: string; checked_at: number }
type Connections = {
  values: Record<string, string>; secrets: Record<string, boolean>; saved_at: number
  restart_required: boolean; active_mode: string; checks: Check[]
  preview_isolated?: boolean
  integrations: { probe_configured: boolean; finance_configured: boolean; newapi_configured: boolean }
}
const labels: Record<string, string> = { main: 'NewAPI 主数据库', logs: '调用日志数据库', management: '站长管理数据库', newapi: 'NewAPI 管理桥接' }
const dbs = [
  { id: 'main', prefix: 'MAIN_DB', title: 'NewAPI 主数据库', description: '用户、令牌、充值、签到和管理操作的数据来源。' },
  { id: 'logs', prefix: 'LOG_DB', title: '调用日志数据库', description: '未单独配置时，调用日志从主数据库读取。' },
  { id: 'management', prefix: 'MANAGEMENT_DB', title: '站长管理数据库', description: '可写的 NewAPI 数据库连接，同时保存站长管理表；留空沿用主库。' },
]
export default function SystemConnections({ tokenVersion, onUnauthorized }: { tokenVersion: number; onUnauthorized: () => void }) {
  const [data, setData] = useState<Connections | null>(null)
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [clear, setClear] = useState<string[]>([])
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const fail = useCallback((cause: unknown) => {
    if (cause instanceof ApiError && cause.status === 401) onUnauthorized()
    else setError(cause instanceof Error ? cause.message : '连接设置暂时不可用。')
  }, [onUnauthorized])
  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    setBusy('load')
    requestJSON<Connections>('/api/system/connections', {}, controller.signal).then(next => {
      if (!controller.signal.aborted) { setData(next); setDraft(next.values); setClear([]) }
    }).catch(cause => { if (!controller.signal.aborted) fail(cause) })
      .finally(() => { if (!controller.signal.aborted) setBusy('') })
    return () => controller.abort()
  }, [tokenVersion, fail])
  function change(key: string, value: string) {
    setDraft(current => ({
      ...current, [key]: value,
      ...(key === 'APP_MODE' && value === 'live' ? { NETWORK_HISTORY_COMPLETE: 'false' } : {}),
    }))
    if (value) setClear(current => current.filter(item => item !== key))
  }
  function inherit(prefix: string) {
    setDraft(current => ({ ...current, [`${prefix}_DRIVER`]: '', [`${prefix}_DSN`]: '' }))
    setClear(current => [...new Set([...current, `${prefix}_DSN`])])
  }
  async function submit(action: 'save' | 'check') {
    if (UI_PREVIEW_ONLY || busy) return
    setBusy(action); setError(''); setMessage('')
    try {
      const body = JSON.stringify({ values: draft, clear_secrets: clear })
      if (action === 'save') {
        const next = await requestJSON<Connections>('/api/system/connections', { method: 'PUT', body })
        setData(next); setDraft(next.values); setClear([])
        setMessage(next.preview_isolated ? '配置已保存；演示预览继续使用样本，使用 Start-Integrated.ps1 启动真实连接。' : next.restart_required ? '配置已保存。重启站长后台后生效；探针和财务同步沿用重启后的连接。' : '配置已保存。')
      } else {
        const next = await requestJSON<{ checks: Check[] }>('/api/system/connections/check', { method: 'POST', body })
        setData(current => current ? { ...current, checks: next.checks } : current)
        setMessage('连接检查已完成，以下显示本次填写配置的实际结果。')
      }
    } catch (cause) { fail(cause) } finally { setBusy('') }
  }
  return <div className="mx-auto max-w-[1440px] space-y-6">
    <div className="flex flex-wrap items-center justify-between gap-4">
      <div><h1 className="flex items-center gap-2 text-3xl font-bold"><PlugZap className="h-7 w-7 text-primary" />系统连接</h1><p className="mt-2 text-sm text-muted-foreground">配置数据来源、验证连接，查看各模块接入状态。</p></div>
      <div className="flex flex-wrap items-center gap-2"><Badge variant={data?.active_mode === 'live' ? 'success' : 'outline'}>{data?.active_mode === 'live' ? '真实数据运行中' : '本地演示模式'}</Badge>
        <Button variant="outline" disabled={!!busy || UI_PREVIEW_ONLY || !data} onClick={() => void submit('check')}>{busy === 'check' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <ShieldCheck className="mr-2 h-4 w-4" />}检查连接</Button>
        <Button disabled={!!busy || UI_PREVIEW_ONLY || !data} onClick={() => void submit('save')}>{busy === 'save' ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}保存配置</Button></div>
    </div>
    {error && <Notice kind="error">{error}</Notice>}{message && <Notice kind="success">{message}</Notice>}
    {data?.preview_isolated ? <Notice kind="info">演示预览始终使用样本。已保存连接供 Start-Integrated.ps1 使用；启动真实连接后读取业务数据。</Notice> : data?.restart_required && <Notice kind="warning">存在已保存但尚未应用的连接配置，需要重启站长后台。当前页面的数据仍来自 {data.active_mode === 'demo' ? '演示数据' : '当前运行连接'}。</Notice>}
    {busy === 'load' && <div className="flex items-center gap-2 p-8 text-muted-foreground"><Loader2 className="h-5 w-5 animate-spin" />正在读取连接状态…</div>}
    <fieldset disabled={!!busy || UI_PREVIEW_ONLY} className="min-w-0 space-y-6" aria-busy={!!busy}>
    <Card><CardContent className="grid gap-5 pt-6 md:grid-cols-2"><label className="space-y-2 text-sm font-medium"><span>数据运行模式</span><Select value={draft.APP_MODE ?? 'demo'} onChange={event => change('APP_MODE', event.target.value)}><option value="demo">演示数据</option><option value="live">NewAPI 真实数据</option></Select></label>
      <label className="flex items-start gap-3 rounded-lg border bg-muted/20 p-4"><input type="checkbox" className="mt-1" checked={draft.NETWORK_HISTORY_COMPLETE === 'true'} onChange={event => change('NETWORK_HISTORY_COMPLETE', String(event.target.checked))} /><span className="text-sm"><strong>调用历史已完整导入</strong><span className="mt-1 block text-xs leading-5 text-muted-foreground">仅在确认源日志保留完整时启用。该声明不会补回已删除的历史，也不会将登录探针历史标记为完整。</span></span></label></CardContent></Card>
    <div className="grid gap-5 xl:grid-cols-3">{dbs.map(item => <Card key={item.id}>
      <CardHeader><CardTitle className="flex items-center gap-2 text-lg"><Database className="h-5 w-5 text-primary" />{item.title}</CardTitle><CardDescription className="min-h-10 leading-5">{item.description}</CardDescription></CardHeader>
      <CardContent className="space-y-4"><label className="block space-y-2 text-sm font-medium"><span>数据库类型</span><Select value={draft[`${item.prefix}_DRIVER`] ?? ''} onChange={event => event.target.value ? change(`${item.prefix}_DRIVER`, event.target.value) : inherit(item.prefix)}>
        <option value="">{item.id === 'main' ? '选择数据库' : '沿用主数据库'}</option><option value="postgres">PostgreSQL</option><option value="mysql">MySQL</option></Select></label>
        <label className="block space-y-2 text-sm font-medium"><span className="flex items-center justify-between">连接字符串<Badge variant="outline">{data?.secrets[`${item.prefix}_DSN`] && !clear.includes(`${item.prefix}_DSN`) ? '已保存' : '未配置'}</Badge></span>
          <Input type="password" autoComplete="new-password" disabled={item.id !== 'main' && !draft[`${item.prefix}_DRIVER`]} value={draft[`${item.prefix}_DSN`] ?? ''} onChange={event => change(`${item.prefix}_DSN`, event.target.value)} placeholder={data?.secrets[`${item.prefix}_DSN`] ? '留空保留原连接' : item.prefix === 'MAIN_DB' ? '输入完整 DSN' : '留空沿用主数据库'} /></label>
        <p className="text-xs leading-5 text-muted-foreground">{draft[`${item.prefix}_DRIVER`] === 'mysql' ? 'MySQL：user:password@tcp(host:3306)/database?parseTime=true' : 'PostgreSQL：postgres://user:password@host:5432/database?sslmode=require'}</p>
      </CardContent></Card>)}</div>
    <div className="grid gap-5 lg:grid-cols-2">
      <Card><CardHeader><CardTitle className="flex items-center gap-2 text-lg"><KeyRound className="h-5 w-5 text-primary" />NewAPI 管理桥接</CardTitle><CardDescription>验证管理员身份，用于需要 NewAPI 官方管理链路的操作。</CardDescription></CardHeader><CardContent className="space-y-4">
        <label className="block space-y-2 text-sm font-medium"><span>NewAPI 地址</span><Input value={draft.NEWAPI_UPSTREAM ?? ''} onChange={event => change('NEWAPI_UPSTREAM', event.target.value)} placeholder="https://newapi.example.com" /></label>
        <div className="grid gap-4 sm:grid-cols-[140px_1fr]"><label className="space-y-2 text-sm font-medium"><span>管理员用户 ID</span><Input inputMode="numeric" value={draft.NEWAPI_ADMIN_USER_ID ?? ''} onChange={event => change('NEWAPI_ADMIN_USER_ID', event.target.value)} /></label>
          <label className="space-y-2 text-sm font-medium"><span>管理员访问令牌 {data?.secrets.NEWAPI_ADMIN_TOKEN && <span className="text-xs text-muted-foreground">已保存</span>}</span><Input type="password" autoComplete="new-password" value={draft.NEWAPI_ADMIN_TOKEN ?? ''} onChange={event => change('NEWAPI_ADMIN_TOKEN', event.target.value)} placeholder="留空保留当前地址的密钥" /></label></div>
      </CardContent></Card>
      <Card><CardHeader><CardTitle className="flex items-center gap-2 text-lg"><Server className="h-5 w-5 text-primary" />模块接入</CardTitle><CardDescription>服务配置和实际数据接通分开显示。</CardDescription></CardHeader><CardContent className="space-y-3">
        {[['登录与注册探针', data?.integrations.probe_configured, '共享机器密钥由部署配置生成；NewAPI 的登录、注册入口需经过探针网关。', ''], ['圈钱中心', data?.integrations.finance_configured, '自动继承主数据库；钉钉连接在圈钱中心中配置和验证。', '/money'], ['AI 辅助审核', null, '模型拉取、连接验证和审核规则在风控中心设置。', '/risk?view=ai']].map(([title, configured, description, href]) => <div key={String(title)} className="rounded-xl border p-4">
          <div className="flex items-center justify-between gap-2"><span className="font-medium">{String(title)}</span>{href ? <a href={String(href)} className="inline-flex items-center gap-1 text-xs text-primary">打开<ExternalLink className="h-3 w-3" /></a> : <Badge variant={configured ? 'success' : 'outline'}>{configured ? '已配置' : '待配置'}</Badge>}</div><p className="mt-2 text-xs leading-5 text-muted-foreground">{String(description)}</p>
        </div>)}
      </CardContent></Card>
    </div>
    <Card><CardHeader><CardTitle className="flex items-center gap-2 text-lg"><RefreshCw className="h-5 w-5 text-primary" />连接检查结果</CardTitle><CardDescription>检查仅验证连接、读取权限与管理员身份；不修改业务数据。</CardDescription></CardHeader><CardContent>
      <div className="grid gap-3 md:grid-cols-2">{Object.entries(labels).map(([id, title]) => {
        const check = data?.checks.find(item => item.id === id)
        const good = check?.state === 'connected'
        return <div key={id} className="flex items-start gap-3 rounded-xl border bg-muted/10 p-4">{good ? <CheckCircle2 className="mt-0.5 h-5 w-5 shrink-0 text-emerald-600" /> : <CircleDashed className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />}<div className="min-w-0"><div className="flex flex-wrap items-center gap-2 font-medium">{title}<Badge variant={good ? 'success' : check?.state === 'failed' ? 'warning' : 'outline'}>{good ? '已连接' : check?.state === 'failed' ? '连接失败' : check?.state === 'missing' ? '缺少配置' : '尚未检查'}</Badge></div><p className="mt-2 text-sm leading-6 text-muted-foreground">{check?.message ?? '填写连接后点击“检查连接”。'}</p>{check?.checked_at && <p className="mt-2 text-xs text-muted-foreground">{new Date(check.checked_at * 1000).toLocaleString('zh-CN')}</p>}</div></div>
      })}</div>
    </CardContent></Card>
    </fieldset>
    <SystemReadinessPanel tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} disabled={!!busy} />
  </div>
}
