import { isValidElement, useEffect } from 'react'
import type { ReactNode } from 'react'
import { Activity, AlertCircle, ChevronLeft, ChevronRight, Database, Info } from 'lucide-react'
import { Button } from './components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from './components/ui/dialog'
import { StatCard } from './components/StatCard'
import { cn } from './lib/utils'
import { useToast } from './components/Toast'
import './module-ui.css'

export function ModuleHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
    <div className="min-w-0"><h1 className="text-2xl font-bold text-foreground sm:text-3xl">{title}</h1>{description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}</div>
    {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
  </div>
}

export function MetricCard({ label, value, detail, tone = 'neutral' }: {
  label: string; value: ReactNode; detail?: ReactNode; tone?: string
}) {
  return <StatCard title={label} value={value} subValue={detail} icon={Activity} color={tone === 'neutral' ? 'blue' : tone} />
}

function noticeText(value: ReactNode): string {
  if (typeof value === 'string' || typeof value === 'number') return String(value)
  if (Array.isArray(value)) return value.map(noticeText).join('')
  if (isValidElement<{ children?: ReactNode }>(value)) return noticeText(value.props.children)
  return ''
}

export function Notice({ kind = 'info', children }: {
  kind?: 'error' | 'warning' | 'info' | 'success'; children: ReactNode
}) {
  const { showToast } = useToast()
  const message = noticeText(children)
  useEffect(() => {
    if (kind === 'success' && message) showToast('success', message)
  }, [kind, message, showToast])
  if (kind === 'success') return null
  const Icon = kind === 'error' || kind === 'warning' ? AlertCircle : Info
  return <div className={cn('flex items-start gap-2 rounded-lg border px-4 py-3 text-sm',
    kind === 'error' ? 'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/20 dark:text-red-300'
      : kind === 'warning' ? 'border-orange-200 bg-orange-50 text-orange-700 dark:border-orange-900 dark:bg-orange-950/20 dark:text-orange-300'
        : 'border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-900 dark:bg-blue-950/20 dark:text-blue-300')}
    role={kind === 'error' ? 'alert' : 'status'}><Icon className="mt-0.5 h-4 w-4 shrink-0" /><div className="min-w-0 break-words leading-relaxed">{children}</div></div>
}

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return <div className="flex min-h-36 flex-col items-center justify-center gap-2 px-4 py-10 text-center text-muted-foreground">
    <Database className="mb-1 h-7 w-7 opacity-30" /><p className="text-sm">{title}</p>
    {description && <p className="max-w-lg text-xs leading-relaxed">{description}</p>}{action}
  </div>
}

export function Pager({ page, pageSize, total, onPage }: {
  page: number; pageSize: number; total: number; onPage: (page: number) => void
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  return <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4 text-sm text-muted-foreground" role="navigation" aria-label="分页">
    <span>共 {total.toLocaleString('zh-CN')} 条记录</span>
    <div className="flex items-center gap-2">
      <Button variant="outline" size="sm" disabled={page <= 1} aria-label="上一页" onClick={() => onPage(page - 1)}><ChevronLeft className="h-4 w-4" /></Button>
      <span className="min-w-16 text-center">第 {page} / {pages} 页</span>
      <Button variant="outline" size="sm" disabled={page >= pages} aria-label="下一页" onClick={() => onPage(page + 1)}><ChevronRight className="h-4 w-4" /></Button>
    </div>
  </div>
}

export function AdminDialog({ title, onClose, children, wide = false }: {
  title: string; onClose: () => void; children: ReactNode; wide?: boolean
}) {
  return <Dialog open onOpenChange={open => { if (!open) onClose() }}>
    <DialogContent className={cn(wide && 'sm:max-w-4xl')} aria-describedby={undefined}>
      <DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>
      <div className="min-w-0">{children}</div>
    </DialogContent>
  </Dialog>
}
