import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { CheckCircle2, Info, X, XCircle } from 'lucide-react'
import { cn } from '../lib/utils'

type ToastType = 'success' | 'error' | 'info'
type ToastMessage = { id: string; type: ToastType; message: string }
type ToastContextValue = { showToast: (type: ToastType, message: string) => void }
const ToastContext = createContext<ToastContextValue | null>(null)

function Toast({ value, onClose }: { value: ToastMessage; onClose: (id: string) => void }) {
  useEffect(() => {
    const timer = window.setTimeout(() => onClose(value.id), 3000)
    return () => window.clearTimeout(timer)
  }, [value.id, onClose])
  const Icon = value.type === 'success' ? CheckCircle2 : value.type === 'error' ? XCircle : Info
  return <div role={value.type === 'error' ? 'alert' : 'status'} className={cn(
    'pointer-events-auto flex w-[min(28rem,calc(100vw-2rem))] items-center gap-3 rounded-lg px-4 py-3 text-white shadow-lg',
    value.type === 'success' ? 'bg-green-500' : value.type === 'error' ? 'bg-red-500' : 'bg-blue-500')}>
    <Icon className="h-5 w-5 shrink-0" /><span className="min-w-0 flex-1 break-words text-sm">{value.message}</span>
    <button type="button" onClick={() => onClose(value.id)} aria-label="关闭提示" className="inline-flex h-6 w-6 shrink-0 items-center justify-center text-white/80 hover:text-white"><X className="h-4 w-4" /></button>
  </div>
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastMessage[]>([])
  const close = useCallback((id: string) => setToasts(values => values.filter(value => value.id !== id)), [])
  const showToast = useCallback((type: ToastType, message: string) => {
    setToasts(values => {
      const last = values[values.length - 1]
      if (last?.type === type && last.message === message) return values
      return [...values.slice(-3), { id: `${Date.now()}-${crypto.getRandomValues(new Uint32Array(1))[0]}`, type, message }]
    })
  }, [])
  return <ToastContext.Provider value={{ showToast }}>{children}<div className="pointer-events-none fixed right-4 top-4 z-[10000] flex flex-col gap-2">{toasts.map(value => <Toast key={value.id} value={value} onClose={close} />)}</div></ToastContext.Provider>
}

export function useToast() {
  const context = useContext(ToastContext)
  if (!context) throw new Error('ToastProvider is required')
  return context
}
