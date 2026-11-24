import { useEffect, useRef, useState } from 'react'
import { Bot, CheckCircle2, CircleDashed, Globe2, Loader2, PlugZap, RadioTower, Server, Wallet } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './components/ui/card'

type ReadinessState = 'missing' | 'configured' | 'unreachable' | 'ready' | 'demo'
type ModuleID = 'finance' | 'probe' | 'ai' | 'newapi' | 'ip'
type ReadinessModule = {
  id: ModuleID
  state: ReadinessState
  message: string
  checked_at: number
  checks: { label: string; value: string; state?: ReadinessState }[]
  warnings: string[]
}
type ReadinessResult = {
  active_mode: string
  checked_at: number
  read_only: true
  modules: ReadinessModule[]
}
type RequestScope = { tokenVersion: number; sessionToken: string }
type ScopedResult = RequestScope & { data: ReadinessResult }
type ScopedError = RequestScope & { message: string }

const modules = [
  { id: 'finance', title: '圈钱中心', icon: Wallet },
  { id: 'probe', title: '登录注册探针', icon: RadioTower },
  { id: 'ai', title: 'AI 辅助审核', icon: Bot },
  { id: 'newapi', title: 'NewAPI 管理桥接', icon: PlugZap },
  { id: 'ip', title: 'IP 情报', icon: Globe2 },
] as const
const stateLabels: Record<ReadinessState, string> = {
  missing: '未配置', configured: '已配置待验证', unreachable: '暂不可达',
  ready: '已就绪', demo: '演示',
}
const stateNames = new Set<ReadinessState>(['missing', 'configured', 'unreachable', 'ready', 'demo'])

function sameScope(left: RequestScope, right: RequestScope) {
  return left.tokenVersion === right.tokenVersion && left.sessionToken === right.sessionToken
}
function timestamp(value?: number) {
  return value && Number.isFinite(value) && value > 0
    ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—'
}
function StatusBadge({ state }: { state?: ReadinessState }) {
  return <Badge variant={state === 'ready' ? 'success' : state === 'unreachable' ? 'warning' : 'outline'}>
    {state ? stateLabels[state] : '未检查'}
  </Badge>
}
function validResult(value: ReadinessResult) {
  return value && value.read_only === true && Array.isArray(value.modules) &&
    value.modules.every(module => modules.some(item => item.id === module.id) && stateNames.has(module.state) &&
      typeof module.message === 'string' && Array.isArray(module.checks) && Array.isArray(module.warnings) &&
      module.warnings.every(warning => typeof warning === 'string') &&
      module.checks.every(check => typeof check.label === 'string' && typeof check.value === 'string' &&
        (!check.state || stateNames.has(check.state))))
}

export default function SystemReadinessPanel({ tokenVersion, onUnauthorized, disabled = false }: {
  tokenVersion: number
  onUnauthorized: () => void
  disabled?: boolean
}) {
  const sessionToken = sessionStorage.getItem('siteadmin-token') ?? ''
  const scope = { tokenVersion, sessionToken }
  const scopeRef = useRef<RequestScope>(scope)
  scopeRef.current = scope
  const unauthorizedRef = useRef(onUnauthorized)
  unauthorizedRef.current = onUnauthorized
  const operation = useRef<AbortController | null>(null)
  const serial = useRef(0)
  const [result, setResult] = useState<ScopedResult | null>(null)
  const [error, setError] = useState<ScopedError | null>(null)
  const [busy, setBusy] = useState(false)

  // Session changes invalidate results and pending work without initiating a
  // module check. Only the explicit button below performs a request.
  useEffect(() => {
    serial.current++
    operation.current?.abort()
    operation.current = null
    setResult(null)
    setError(null)
    setBusy(false)
    return () => {
      serial.current++
      operation.current?.abort()
      operation.current = null
    }
  }, [tokenVersion, sessionToken])

  async function checkReadiness() {
    if (disabled || UI_PREVIEW_ONLY || operation.current) return
    const controller = new AbortController()
    operation.current = controller
    const requestScope = { tokenVersion, sessionToken }
    const requestSerial = ++serial.current
    const isCurrent = () => !controller.signal.aborted && requestSerial === serial.current &&
      sameScope(scopeRef.current, requestScope) &&
      (sessionStorage.getItem('siteadmin-token') ?? '') === requestScope.sessionToken
    setBusy(true)
    setResult(null)
    setError(null)
    try {
      const data = await requestJSON<ReadinessResult>('/api/system/readiness', {}, controller.signal)
      if (!isCurrent()) return
      if (!validResult(data)) throw new Error('invalid readiness response')
      setResult({ ...requestScope, data })
    } catch (cause) {
      if (!isCurrent()) return
      const unauthorized = cause instanceof ApiError && cause.status === 401
      setError({ ...requestScope, message: unauthorized ? '管理员会话已失效，请重新连接后检查。' : '模块就绪检查暂时不可用，请稍后重试。' })
      if (unauthorized) unauthorizedRef.current()
    } finally {
      if (operation.current === controller) operation.current = null
      if (isCurrent()) setBusy(false)
    }
  }

  const data = result && sameScope(result, scope) ? result.data : null
  const currentError = error && sameScope(error, scope) ? error.message : ''
  return <section className="min-w-0 space-y-4" aria-label="模块就绪检查" aria-busy={busy}>
    <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0"><h2 className="flex items-center gap-2 text-xl font-semibold"><Server className="h-5 w-5 text-primary" />模块就绪</h2>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">主动检查机器服务与当前运行配置。检查读取服务状态，不发送通知、不发起 AI 审核。</p>
        {data && <p className="mt-1 text-xs text-muted-foreground">检查时间：{timestamp(data.checked_at)} · {data.active_mode === 'live' ? '真实数据运行配置' : '演示运行配置'}</p>}
      </div>
      <Button variant="outline" type="button" className="shrink-0 self-start" disabled={disabled || busy || UI_PREVIEW_ONLY} onClick={() => void checkReadiness()}>
        {busy ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <CheckCircle2 className="mr-2 h-4 w-4" />}{busy ? '正在检查模块…' : '检查模块就绪'}
      </Button>
    </div>
    {currentError && <Notice kind="error">{currentError}</Notice>}
    {UI_PREVIEW_ONLY && <p className="text-sm text-muted-foreground">数据获取已暂停，模块状态尚未检查。</p>}
    <div className="grid min-w-0 gap-4 md:grid-cols-2 xl:grid-cols-3">
      {modules.map(item => {
        const value = data?.modules.find(module => module.id === item.id)
        // A live-looking status returned while running a finance preview must
        // never be presented as a verified production connection.
        const financeDemo = item.id === 'finance' && !!value && (data?.active_mode !== 'live' || value.state === 'demo')
        const state = financeDemo && value?.state === 'ready' ? 'demo' : value?.state
        const warnings = [...new Set([
          ...(value?.warnings ?? []),
          ...(item.id === 'probe' && value ? ['探针服务就绪不代表登录流量已接入；仍需确认网关路由和真实登录、注册事件。'] : []),
          ...(financeDemo ? ['当前使用演示运行配置，财务检查结果不代表真实数据来源或通知投递已接通。'] : []),
        ])]
        const Icon = item.icon
        return <Card key={item.id} className="min-w-0 overflow-hidden">
          <CardHeader className="space-y-3 p-5 pb-4">
            <div className="flex items-center justify-between gap-2"><div className="flex h-9 w-9 items-center justify-center rounded-xl border bg-primary/5 text-primary"><Icon className="h-4 w-4" /></div><StatusBadge state={state} /></div>
            <CardTitle className="text-base">{item.title}</CardTitle>
            <CardDescription className="break-words text-xs leading-5">{value?.message ?? (busy ? '正在读取该模块的实际状态。' : data ? '本次未返回该模块的就绪结果。' : '尚未检查。点击上方按钮读取模块状态。')}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3 px-5 pb-5">
            {value?.checks.length ? <dl className="divide-y rounded-lg border bg-muted/10 px-3">{value.checks.map((check, index) => <div key={`${check.label}:${index}`} className="flex flex-wrap items-start justify-between gap-x-3 gap-y-1 py-2.5 text-xs"><dt className="text-muted-foreground">{check.label}</dt><dd className="flex min-w-0 flex-wrap items-center justify-end gap-1.5 text-right font-medium"><span className="min-w-0 break-words">{check.value}</span>{check.state && <StatusBadge state={financeDemo && check.state === 'ready' ? 'demo' : check.state} />}</dd></div>)}</dl> : !value && <div className="flex items-center gap-2 text-xs text-muted-foreground"><CircleDashed className="h-4 w-4" />{busy ? '检查中' : '等待主动检查'}</div>}
            {warnings.length > 0 && <ul className="space-y-2 rounded-lg border border-amber-200/70 bg-amber-50/60 p-3 text-xs leading-5 text-amber-800 dark:border-amber-900/70 dark:bg-amber-950/20 dark:text-amber-300">{warnings.map(warning => <li key={warning}>{warning}</li>)}</ul>}
            {value && <p className="text-[11px] text-muted-foreground">状态时间：{timestamp(value.checked_at)}</p>}
          </CardContent>
        </Card>
      })}
    </div>
  </section>
}
