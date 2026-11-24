import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowLeft, ArrowRight, BarChart3, Check, Eye, EyeOff, Fingerprint, KeyRound, Loader2, LockKeyhole, ShieldCheck, UserRound, Wallet } from 'lucide-react'
import { ApiError, requestJSON, setCSRFToken, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Button } from './components/ui/button'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import LoginCaptcha from './LoginCaptcha'
import type { OwnerAuthResult, OwnerSession } from './types'
import './owner-login.css'

const methodNames: Record<string, string> = {
  '2fa': '动态验证码 / 备用恢复码', totp: '身份验证器', email: '邮箱验证码', recovery_code: '恢复代码',
  backup_code: '恢复代码', recovery: '恢复代码', legacy_2fa: '身份验证器 / 恢复代码',
}

export default function LoginPage({ session, loading, initialError, message, onRetry, onAuthenticated }: {
  session: OwnerSession | null
  loading: boolean
  initialError?: string
  message?: string
  onRetry: () => void
  onAuthenticated: (result: OwnerAuthResult) => Promise<void>
}) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [visible, setVisible] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [challenge, setChallenge] = useState<OwnerAuthResult | null>(null)
  const [method, setMethod] = useState('')
  const [code, setCode] = useState('')
  const [capsLock, setCapsLock] = useState(false)
  const [captchaToken, setCaptchaToken] = useState('')
  const [captchaRevision, setCaptchaRevision] = useState(0)
  const accountInput = useRef<HTMLInputElement>(null)
  const codeInput = useRef<HTMLInputElement>(null)
  const demo = session?.mode === 'demo' && session.login_source === 'demo'
  const unavailable = !loading && session && !session.login_configured
  const disabled = UI_PREVIEW_ONLY || loading || busy || !session || !session.login_configured || session.login_ready === false
  const captchaNeeded = !challenge && !!session?.captcha_required && session.login_ready !== false
  useEffect(() => {
    const saved = localStorage.getItem('siteadmin-tools-theme')
    const dark = saved === 'dark' || saved === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches
    document.documentElement.classList.toggle('dark', dark)
  }, [])
  useEffect(() => { if (challenge) codeInput.current?.focus() }, [challenge])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (disabled || captchaNeeded && !captchaToken || (!challenge && (!username.trim() || !password)) || (challenge && !code.trim())) return
    setBusy(true)
    setError('')
    try {
      const next = await requestJSON<OwnerAuthResult>(challenge ? '/api/auth/verify' : '/api/auth/login', {
        method: 'POST',
        body: JSON.stringify(challenge
          ? { challenge_id: challenge.challenge_id, method, code: code.trim() }
          : { username: username.trim(), password, ...(captchaNeeded ? { captcha_token: captchaToken } : {}) }),
      })
      if (next.csrf_token) setCSRFToken(next.csrf_token)
      if (next.authenticated) {
        setPassword('')
        setCode('')
        await onAuthenticated(next)
      } else if (next.requires_verification && next.challenge_id && next.methods?.length) {
        setPassword('')
        setCode('')
        setChallenge(next)
        setMethod(next.methods[0])
      } else {
        throw new Error(next.message || '登录未完成，请重新输入账号密码。')
      }
    } catch (cause) {
      setError(cause instanceof ApiError || cause instanceof Error ? cause.message : '登录失败，请稍后重试。')
      if (!challenge) setPassword('')
    } finally {
      setBusy(false)
      if (captchaNeeded) { setCaptchaToken(''); setCaptchaRevision(value => value + 1) }
    }
  }
  function back() {
    if (busy) return
    setChallenge(null); setCode(''); setMethod(''); setError(''); setPassword('')
    accountInput.current?.focus()
  }
  return <div className="owner-login">
    <section className="owner-login-story" aria-label="站长后台介绍">
      <a className="owner-login-brand" href="/login"><img src="/tool.svg" alt="" /><span>站长后台<small>NewAPI SITE ADMIN</small></span></a>
      <div className="owner-login-story-main">
        <span className="owner-login-eyebrow"><span />统一站点管理工作区</span>
        <h1>看见每一次调用，<br /><em>掌握站点的每一面。</em></h1>
        <p>从用户与模型，到风控与财务。用熟悉的站长账号，进入你的管理工作区。</p>
        <div className="owner-login-visual" aria-hidden="true">
          <div className="owner-login-orbit owner-login-orbit--outer" />
          <div className="owner-login-orbit owner-login-orbit--inner" />
          <div className="owner-login-core"><ShieldCheck /><span>站点管理</span><small>统一身份 · 全局掌控</small></div>
          <div className="owner-login-node owner-login-node--users"><UserRound /><span>用户与账号</span><i><Check /></i></div>
          <div className="owner-login-node owner-login-node--risk"><Fingerprint /><span>风险与证据</span><i><Check /></i></div>
          <div className="owner-login-node owner-login-node--usage"><BarChart3 /><span>模型与调用</span><i><Check /></i></div>
          <div className="owner-login-node owner-login-node--finance"><Wallet /><span>订单与财务</span><i><Check /></i></div>
        </div>
      </div>
      <div className="owner-login-story-footer"><span><ShieldCheck />站长身份验证</span><span>同一个工作区，同一套登录</span></div>
    </section>
    <main className="owner-login-main">
      <div className="owner-login-mobile-brand"><img src="/tool.svg" alt="" /><span>站长后台</span></div>
      <div className="owner-login-form-wrap">
        <div className="owner-login-form-icon">{challenge ? <KeyRound /> : <LockKeyhole />}</div>
        <span className="owner-login-form-eyebrow">{challenge ? 'IDENTITY VERIFICATION' : 'WELCOME BACK'}</span>
        <h2>{challenge ? '完成身份验证' : '登录站长后台'}</h2>
        <p className="owner-login-description">{challenge ? challenge.message || '该站长账号已启用二次验证，请输入对应验证码。' : '使用 NewAPI 站长账号和密码登录。'}</p>
        {loading && <div className="owner-login-loading" role="status"><Loader2 className="h-4 w-4 animate-spin" />正在确认登录状态…</div>}
        {message && !challenge && <Notice>{message}</Notice>}
        {initialError && <div className="space-y-3"><Notice kind="error">{initialError}</Notice><Button type="button" variant="outline" onClick={onRetry} disabled={loading}>重新连接</Button></div>}
        {unavailable && !UI_PREVIEW_ONLY && <Notice kind="warning">NewAPI 登录连接尚未配置。请先设置服务端的 NEWAPI_UPSTREAM，再使用站长账号登录。</Notice>}
        {!UI_PREVIEW_ONLY && session?.login_configured && session.login_ready === false && <div className="space-y-3"><Notice kind="warning">{session.message || 'NewAPI 登录连接暂未就绪，请检查上游服务后重新连接。'}</Notice><Button type="button" variant="outline" disabled={loading || busy} onClick={onRetry}>重新检查登录连接</Button></div>}
        {UI_PREVIEW_ONLY && <Notice>当前为界面预览，登录请求暂未启用。</Notice>}
        {demo && !UI_PREVIEW_ONLY && <div className="owner-login-demo"><div><span />本地演示</div><p>测试账号 <code>root</code><span className="owner-login-demo-divider">/</span>密码 <code>123456</code></p><small>正式环境使用现有 NewAPI 站长账号密码。</small></div>}
        <form onSubmit={submit} className="owner-login-form">
          {error && <Notice kind="error">{error}</Notice>}
          {challenge ? <>
            {challenge.methods && challenge.methods.length > 1 && <label className="owner-login-field"><span>验证方式</span><Select aria-label="二次验证方式" value={method} disabled={busy} onChange={event => { setMethod(event.target.value); setCode(''); setError('') }}>{challenge.methods.map(item => <option key={item} value={item}>{methodNames[item] ?? item}</option>)}</Select></label>}
            <label className="owner-login-field"><span>{methodNames[method] ?? '验证码'}</span><div className="owner-login-input"><KeyRound /><Input ref={codeInput} name="verification-code" aria-label="验证码" autoComplete="one-time-code" value={code} onChange={event => setCode(event.target.value)} maxLength={128} disabled={busy} required placeholder={method.includes('recovery') || method === 'backup_code' ? '输入恢复代码' : '输入验证码'} /></div></label>
          </> : <>
            <label className="owner-login-field"><span>站长账号</span><div className="owner-login-input"><UserRound /><Input ref={accountInput} name="username" autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} maxLength={128} disabled={disabled} required placeholder="输入站长账号" /></div></label>
            <label className="owner-login-field"><span>密码</span><div className="owner-login-input owner-login-input--password"><LockKeyhole /><Input name="password" type={visible ? 'text' : 'password'} autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} onKeyUp={event => setCapsLock(event.getModifierState('CapsLock'))} onBlur={() => setCapsLock(false)} maxLength={512} disabled={disabled} required placeholder="输入登录密码" /><button type="button" aria-label={visible ? '隐藏密码' : '显示密码'} aria-pressed={visible} disabled={disabled} onClick={() => setVisible(value => !value)}>{visible ? <EyeOff /> : <Eye />}</button></div>{capsLock && <small className="text-amber-600">大写锁定已开启</small>}</label>
          </>}
          {captchaNeeded && <LoginCaptcha siteKey={session?.captcha_site_key ?? ''} resetKey={captchaRevision} onToken={setCaptchaToken} />}
          <Button type="submit" className="owner-login-submit" disabled={disabled || captchaNeeded && !captchaToken || (challenge ? !code.trim() : !username.trim() || !password)}>{busy || loading ? <Loader2 className="h-4 w-4 animate-spin" /> : null}<span>{busy ? challenge ? '正在验证' : '正在登录' : challenge ? '验证并登录' : '登录工作区'}</span>{!busy && !loading && <ArrowRight className="h-4 w-4" />}</Button>
          {challenge && <Button type="button" variant="ghost" className="w-full" disabled={busy} onClick={back}><ArrowLeft className="mr-2 h-4 w-4" />返回账号登录</Button>}
        </form>
        <div className="owner-login-trust"><ShieldCheck /><span>仅限 NewAPI 站长账号访问</span></div>
      </div>
      <footer className="owner-login-footer"><span>NewAPI 站长后台</span><span>专注运营 · 有据可查</span></footer>
    </main>
  </div>
}
