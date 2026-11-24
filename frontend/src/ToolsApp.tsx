import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { Activity, BarChart3, CalendarCheck, DollarSign, Globe, Key, LayoutDashboard, ListChecks, Loader2, PlugZap, Ticket, UserPlus, Users } from 'lucide-react'
import { ApiError, AUTH_EXPIRED_EVENT, getMeta, requestJSON, setCSRFToken, UI_PREVIEW_ONLY } from './api'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import ToolsLayout from './ToolsLayout'
import LoginPage from './LoginPage'
import { IPDetailsProvider } from './IPDetails'
import { EmptyState, Notice } from './ModuleUI'
import type { OwnerAuthResult, OwnerSession, SystemMeta } from './types'

const Overview = lazy(() => import('./Overview'))
const UserManagement = lazy(() => import('./UserManagement'))
const IPAnalysis = lazy(() => import('./IPAnalysis'))
const RiskControl = lazy(() => import('./RiskControl'))
const LogAnalysis = lazy(() => import('./LogAnalysis'))
const TaskLogs = lazy(() => import('./TaskLogs'))
const TokenManagement = lazy(() => import('./TokenManagement'))
const AutoGroup = lazy(() => import('./AutoGroup').then(module => ({ default: module.AutoGroup })))
const Redemptions = lazy(() => import('./Redemptions'))
const CheckinAnalysis = lazy(() => import('./CheckinAnalysis'))
const SystemConnections = lazy(() => import('./SystemConnections'))

const tabs = [
  { path: '/', title: '仪表板', icon: LayoutDashboard },
  { path: '/money', title: '圈钱中心', icon: DollarSign },
  { path: '/risk', title: '风控中心', icon: Activity },
  { path: '/ip-analysis', title: 'IP分析', icon: Globe },
  { path: '/log-analysis', title: '日志分析', icon: BarChart3 },
  { path: '/task-logs', title: '任务日志', icon: ListChecks },
  { path: '/users', title: '用户管理', icon: Users },
  { path: '/checkin-analysis', title: '签到分析', icon: CalendarCheck },
  { path: '/tokens', title: '令牌管理', icon: Key },
  { path: '/auto-group', title: '自动分组', icon: UserPlus },
  { path: '/redemptions', title: '兑换码生成', icon: Ticket },
  { path: '/system', title: '系统连接', icon: PlugZap },
]

function validSession(value: OwnerSession) {
  if (typeof value.authenticated !== 'boolean') throw new Error('登录服务返回了无法识别的状态。')
  if (value.authenticated && (!value.user || value.user.role < 100 || !value.csrf_token)) {
    throw new Error('站长身份验证尚未完成，请重新登录。')
  }
  return value
}
function afterLoginPath() {
  const candidate = new URLSearchParams(window.location.search).get('redirect') || '/'
  return candidate.startsWith('/') && !candidate.startsWith('//') && !/[\\\r\n]/.test(candidate) &&
    tabs.some(tab => tab.path === candidate.split('?')[0]) ? candidate : '/'
}

export default function ToolsApp() {
  const [location, setLocation] = useState(() => window.location.pathname + window.location.search)
  const currentPath = location.split('?')[0].replace(/\/$/, '') || '/'
  const [meta, setMeta] = useState<SystemMeta | null>(null)
  const [tokenVersion, setTokenVersion] = useState(0)
  const [session, setSession] = useState<OwnerSession | null>(null)
  const [authLoading, setAuthLoading] = useState(!UI_PREVIEW_ONLY)
  const [authError, setAuthError] = useState('')
  const [authMessage, setAuthMessage] = useState('')
  const [logoutBusy, setLogoutBusy] = useState(false)
  const [logoutError, setLogoutError] = useState('')
  const sessionRequest = useRef<AbortController | null>(null)
  const sessionSequence = useRef(0)
  const invalidating = useRef(false)
  const navigate = useCallback((path: string) => {
    if (window.location.pathname + window.location.search !== path) window.history.pushState(null, '', path)
    setLocation(window.location.pathname + window.location.search)
    window.scrollTo({ top: 0, behavior: 'instant' })
  }, [])
  const refreshSession = useCallback(async () => {
    if (UI_PREVIEW_ONLY) return
    sessionRequest.current?.abort()
    const controller = new AbortController()
    sessionRequest.current = controller
    const sequence = ++sessionSequence.current
    setAuthLoading(true)
    setAuthError('')
    try {
      const value = validSession(await requestJSON<OwnerSession>('/api/auth/session', {}, controller.signal))
      if (controller.signal.aborted || sequence !== sessionSequence.current) return
      setCSRFToken(value.csrf_token)
      setSession(value)
    } catch (error) {
      if (controller.signal.aborted || sequence !== sessionSequence.current) return
      setCSRFToken('')
      setSession(null)
      setAuthError(error instanceof Error ? error.message : '暂时无法连接登录服务。')
    } finally {
      if (!controller.signal.aborted && sequence === sessionSequence.current) setAuthLoading(false)
    }
  }, [])
  const unauthorized = useCallback(() => {
    if (invalidating.current || UI_PREVIEW_ONLY) return
    invalidating.current = true
    sessionStorage.removeItem('siteadmin-token')
    setCSRFToken('')
    setSession(null)
    setMeta(null)
    setTokenVersion(value => value + 1)
    setAuthMessage('登录状态已失效，请重新登录。')
    setLogoutError('')
    void refreshSession().finally(() => { invalidating.current = false })
  }, [refreshSession])
  useEffect(() => {
    sessionStorage.removeItem('siteadmin-token')
    if (!UI_PREVIEW_ONLY) void refreshSession()
    return () => { sessionRequest.current?.abort() }
  }, [refreshSession])
  useEffect(() => {
    window.addEventListener(AUTH_EXPIRED_EVENT, unauthorized)
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, unauthorized)
  }, [unauthorized])
  useEffect(() => {
    const update = () => setLocation(window.location.pathname + window.location.search)
    window.addEventListener('popstate', update)
    return () => window.removeEventListener('popstate', update)
  }, [])
  useEffect(() => {
    document.title = session?.authenticated
      ? `${tabs.find(tab => tab.path === currentPath)?.title || '站长后台'} · 站长后台`
      : '登录 · 站长后台'
  }, [currentPath, session?.authenticated])
  useEffect(() => {
    if (authLoading || UI_PREVIEW_ONLY) return
    if (!session?.authenticated && currentPath !== '/login') {
      const path = tabs.some(tab => tab.path === currentPath) ? location : '/'
      const loginPath = path === '/' ? '/login' : `/login?redirect=${encodeURIComponent(path)}`
      window.history.replaceState(null, '', loginPath)
      setLocation(loginPath)
    } else if (session?.authenticated && currentPath === '/login') navigate(afterLoginPath())
  }, [authLoading, session?.authenticated, currentPath, location, navigate])
  useEffect(() => {
    if (UI_PREVIEW_ONLY || !session?.authenticated) return
    let cancelled = false
    void getMeta().then(value => {
      if (cancelled) return
      setMeta(value)
    }).catch(error => { if (!cancelled && error instanceof ApiError && error.status === 401) unauthorized() })
    return () => { cancelled = true }
  }, [session?.authenticated, tokenVersion, unauthorized])

  async function completeLogin(result: OwnerAuthResult) {
    const next = result.user && result.csrf_token
      ? validSession({ ...session, ...result } as OwnerSession)
      : validSession(await requestJSON<OwnerSession>('/api/auth/session'))
    if (!next.authenticated) throw new Error('登录状态尚未建立，请重新登录。')
    sessionRequest.current?.abort()
    sessionSequence.current++
    setCSRFToken(next.csrf_token)
    setSession(next)
    setAuthLoading(false)
    setAuthError('')
    setAuthMessage('')
    setLogoutError('')
    setMeta(null)
    setTokenVersion(value => value + 1)
  }
  async function logout() {
    if (logoutBusy || UI_PREVIEW_ONLY) return
    setLogoutBusy(true)
    setLogoutError('')
    try {
      await requestJSON('/api/auth/logout', { method: 'POST', body: '{}' })
      setCSRFToken('')
      setSession(null)
      setMeta(null)
      setTokenVersion(value => value + 1)
      setAuthMessage('已退出登录。')
      window.history.replaceState(null, '', '/login')
      setLocation('/login')
      await refreshSession()
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) unauthorized()
      else setLogoutError(error instanceof Error ? `退出登录未完成：${error.message}` : '退出登录未完成，请重试。')
    } finally { setLogoutBusy(false) }
  }

  if (authLoading || !session?.authenticated || UI_PREVIEW_ONLY) {
    const previewSession: OwnerSession = { authenticated: false, mode: 'demo', login_configured: false, login_source: 'demo' }
    return <LoginPage session={UI_PREVIEW_ONLY ? previewSession : session} loading={authLoading}
      initialError={authError} message={authMessage} onRetry={() => void refreshSession()} onAuthenticated={completeLogin} />
  }
  const unit = meta?.quota_per_unit ?? 500000
  return <IPDetailsProvider tokenVersion={tokenVersion} quotaPerUnit={unit} onUnauthorized={unauthorized}>
    <ToolsLayout tabs={tabs} currentPath={currentPath} onNavigate={navigate}
    owner={session.user} logoutBusy={logoutBusy} onLogout={() => void logout()}>
    {logoutError && <div className="mb-6"><Notice kind="error">{logoutError}</Notice></div>}
    <Suspense fallback={<div className="flex min-h-64 items-center justify-center text-muted-foreground"><Loader2 className="mr-2 h-5 w-5 animate-spin" />加载中...</div>}>
      <div key={`${tokenVersion}:${location}`}>
        {currentPath === '/' && <Overview tokenVersion={tokenVersion} onUnauthorized={unauthorized} />}
        {currentPath === '/users' && <UserManagement tokenVersion={tokenVersion} quotaPerUnit={unit} onUnauthorized={unauthorized} />}
        {currentPath === '/checkin-analysis' && <CheckinAnalysis tokenVersion={tokenVersion} quotaPerUnit={unit} onUnauthorized={unauthorized} />}
        {currentPath === '/ip-analysis' && <IPAnalysis tokenVersion={tokenVersion} onUnauthorized={unauthorized} />}
        {currentPath === '/risk' && <RiskControl tokenVersion={tokenVersion} quotaPerUnit={unit} onUnauthorized={unauthorized} />}
        {currentPath === '/log-analysis' && <LogAnalysis tokenVersion={tokenVersion} onUnauthorized={unauthorized} />}
        {currentPath === '/task-logs' && <TaskLogs tokenVersion={tokenVersion} onUnauthorized={unauthorized} />}
        {currentPath === '/tokens' && <TokenManagement tokenVersion={tokenVersion} quotaPerUnit={unit} onUnauthorized={unauthorized} />}
        {currentPath === '/auto-group' && <AutoGroup key={tokenVersion} onUnauthorized={unauthorized} />}
        {currentPath === '/redemptions' && <Redemptions tokenVersion={tokenVersion} quotaPerUnit={unit} timezone={meta?.timezone} demoMode={meta?.mode === 'demo'} onUnauthorized={unauthorized} />}
        {currentPath === '/system' && <SystemConnections tokenVersion={tokenVersion} onUnauthorized={unauthorized} />}
        {currentPath === '/money' && <div className="space-y-6"><h1 className="flex items-center gap-2 text-3xl font-bold"><DollarSign className="h-8 w-8 text-primary" />圈钱中心</h1><Card><CardHeader><CardTitle className="text-lg">财务工作区</CardTitle></CardHeader><CardContent>{!UI_PREVIEW_ONLY && meta?.finance_configured
          ? <iframe title="圈钱中心" src="/finance/" className="min-h-[640px] w-full rounded-lg border bg-card" />
          : <EmptyState title={UI_PREVIEW_ONLY ? '数据获取已暂停' : '财务连接尚未就绪'} description="订单、收款和财务统计将在接入后显示。" />}</CardContent></Card></div>}
        {!tabs.some(tab => tab.path === currentPath) && <EmptyState title="页面不存在" action={<Button onClick={() => navigate('/')}>返回仪表板</Button>} />}
      </div>
    </Suspense>
    </ToolsLayout>
  </IPDetailsProvider>
}
