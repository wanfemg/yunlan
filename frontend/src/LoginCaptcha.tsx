import { useEffect, useRef, useState } from 'react'
import { Loader2, RefreshCw } from 'lucide-react'
import { Button } from './components/ui/button'

type Turnstile = {
  render: (container: HTMLElement, options: Record<string, unknown>) => string
  remove: (widget: string) => void
}
let turnstileLoad: Promise<Turnstile> | null = null

function loadTurnstile(): Promise<Turnstile> {
  const available = (window as unknown as { turnstile?: Turnstile }).turnstile
  if (available) return Promise.resolve(available)
  if (turnstileLoad) return turnstileLoad
  turnstileLoad = new Promise<Turnstile>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    script.async = true
    script.defer = true
    const fail = (message: string) => {
      window.clearTimeout(timeout)
      script.remove()
      turnstileLoad = null
      reject(new Error(message))
    }
    const timeout = window.setTimeout(() => fail('人机验证加载超时，请检查网络后重试。'), 15000)
    script.onload = () => {
      window.clearTimeout(timeout)
      const api = (window as unknown as { turnstile?: Turnstile }).turnstile
      if (api) resolve(api)
      else fail('人机验证未能加载，请重试。')
    }
    script.onerror = () => fail('人机验证暂时不可用，请检查网络后重试。')
    document.head.append(script)
  })
  return turnstileLoad
}

export default function LoginCaptcha({ siteKey, resetKey, onToken }: {
  siteKey: string; resetKey: number; onToken: (token: string) => void
}) {
  const container = useRef<HTMLDivElement>(null)
  const tokenCallback = useRef(onToken)
  tokenCallback.current = onToken
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [retry, setRetry] = useState(0)
  useEffect(() => {
    let active = true
    let widget: string | null = null
    let api: Turnstile | null = null
    setError('')
    setLoading(true)
    tokenCallback.current('')
    if (!siteKey) {
      setError('NewAPI 已启用人机验证，但未返回有效站点密钥。')
      setLoading(false)
      return
    }
    void loadTurnstile().then(loaded => {
      if (!active || !container.current) return
      api = loaded
      widget = loaded.render(container.current, {
        sitekey: siteKey,
        theme: 'auto',
        size: 'flexible',
        retry: 'never',
        callback: (token: string) => {
          if (!active) return
          setLoading(false); setError(''); tokenCallback.current(token)
        },
        'expired-callback': () => {
          if (!active) return
          tokenCallback.current(''); setError('验证已过期，请重新完成验证。')
        },
        'error-callback': () => {
          if (!active) return true
          setLoading(false); tokenCallback.current('')
          setError('人机验证失败。请重试，并确认当前后台域名已加入 Turnstile 允许域名。')
          return true
        },
      })
      setLoading(false)
    }).catch(cause => {
      if (active) {
        setLoading(false)
        setError(cause instanceof Error ? cause.message : '人机验证加载失败。')
      }
    })
    return () => { active = false; if (api && widget) api.remove(widget) }
  }, [siteKey, resetKey, retry])
  return <div className="owner-login-captcha">
    <span>人机验证</span>
    {loading && <p role="status"><Loader2 className="h-3.5 w-3.5 animate-spin" />正在加载验证…</p>}
    <div ref={container} />
    {error && <div role="alert"><p>{error}</p><Button type="button" variant="outline" size="sm" onClick={() => setRetry(value => value + 1)}><RefreshCw className="mr-1.5 h-3 w-3" />重新验证</Button></div>}
  </div>
}
