import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ClipboardCheck, Loader2, Save } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import type { CheckinReview, CheckinReviewInput } from './checkin-types'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Select } from './components/ui/select'

export type CheckinReviewFormProps = {
  userId: number
  username?: string
  review: CheckinReview | null
  tokenVersion?: number
  observedPaid?: boolean
  onUnauthorized: () => void
  onSaved: (review: CheckinReview | null) => void
}

function initialReview(review: CheckinReview | null): CheckinReviewInput {
  return {
    paid_status: review?.paid_status ?? 'unknown',
    funding: review?.funding ?? 'unknown',
    history_verified: review?.history_verified ?? false,
    reason: review?.reason ?? '',
  }
}

export default function CheckinReviewForm({
  userId, username, review, tokenVersion = 0, observedPaid = false, onUnauthorized, onSaved,
}: CheckinReviewFormProps) {
  const [draft, setDraft] = useState<CheckinReviewInput>(() => initialReview(review))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const serial = useRef(0)
  const abort = useRef<AbortController | null>(null)
  const callbacks = useRef({ onUnauthorized, onSaved })
  callbacks.current = { onUnauthorized, onSaved }
  const sessionToken = sessionStorage.getItem('siteadmin-token')
  const scope = `${userId}:${tokenVersion}:${sessionToken ?? ''}`
  const currentScope = useRef(scope)
  currentScope.current = scope

  useEffect(() => {
    serial.current++
    abort.current?.abort()
    setDraft(initialReview(review))
    setBusy(false)
    setError('')
    setSaved(false)
    return () => { serial.current++; abort.current?.abort() }
  }, [scope, review?.updated_at, review?.paid_status, review?.funding, review?.history_verified, review?.reason])

  function change<K extends keyof CheckinReviewInput>(key: K, value: CheckinReviewInput[K]) {
    setDraft(current => ({ ...current, [key]: value }))
    setSaved(false)
    setError('')
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY || busy || !draft.reason.trim()) return
    const requestScope = scope
    const requestSerial = ++serial.current
    const controller = new AbortController()
    abort.current?.abort()
    abort.current = controller
    const isCurrent = () => !controller.signal.aborted && serial.current === requestSerial &&
      currentScope.current === requestScope && sessionStorage.getItem('siteadmin-token') === sessionToken
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      const result = await requestJSON<{ review: CheckinReview | null }>(`/api/checkins/users/${userId}/review`, {
        method: 'POST',
        body: JSON.stringify({
          paid_status: observedPaid ? 'paid' : draft.paid_status,
          funding: draft.funding,
          history_verified: draft.history_verified,
          reason: draft.reason.trim(),
        } satisfies CheckinReviewInput),
      }, controller.signal)
      if (!isCurrent()) return
      setSaved(true)
      callbacks.current.onSaved(result.review)
    } catch (cause) {
      if (!isCurrent()) return
      if (cause instanceof ApiError && cause.status === 401) {
        setError('管理员会话已失效，请重新连接。')
        callbacks.current.onUnauthorized()
      } else {
        setError(cause instanceof Error ? cause.message : '保存来源核实失败。')
      }
    } finally {
      if (isCurrent()) setBusy(false)
    }
  }

  return <details className="min-w-0 rounded-lg border bg-muted/10 p-4">
    <summary className="flex cursor-pointer list-none flex-wrap items-center gap-2 text-sm font-medium">
      <ClipboardCheck className="h-4 w-4 text-primary" />来源核实
      <Badge variant="outline" className="font-normal">{review ? '已记录人工核实' : '尚未人工核实'}</Badge>
    </summary>
    <form className="mt-4 min-w-0 space-y-4" onSubmit={event => void save(event)}>
      <p className="text-xs leading-relaxed text-muted-foreground">
        仅保存 {username || `用户 #${userId}`} 的本站分析记录，不修改 NewAPI 账号、余额或订单。
        已记录的成功外部付款始终优先，人工标记不能将已付费用户覆盖为未付费。
      </p>
      <div className="grid min-w-0 gap-4 sm:grid-cols-2">
        <label className="min-w-0 space-y-2 text-sm">
          <span className="block text-xs font-medium text-muted-foreground">外部付费核实</span>
          <Select aria-label="外部付费核实" value={observedPaid ? 'paid' : draft.paid_status} disabled={busy || UI_PREVIEW_ONLY || observedPaid}
            onChange={event => change('paid_status', event.target.value as CheckinReviewInput['paid_status'])}>
            <option value="unknown">尚未确认</option>
            <option value="paid">已确认有外部付费</option>
            <option value="none">已核实无外部付费</option>
          </Select>
          {observedPaid && <span className="block text-xs text-muted-foreground">已有付费事实，按已付费处理。</span>}
        </label>
        <label className="min-w-0 space-y-2 text-sm">
          <span className="block text-xs font-medium text-muted-foreground">非支付额度来源</span>
          <Select aria-label="非支付额度来源" value={draft.funding} disabled={busy || UI_PREVIEW_ONLY}
            onChange={event => change('funding', event.target.value as CheckinReviewInput['funding'])}>
            <option value="unknown">来源尚未确认</option>
            <option value="free_only">已核实仅免费福利额度</option>
          </Select>
        </label>
      </div>
      <label className="flex items-start gap-2 rounded-md bg-muted/30 p-3 text-sm">
        <input className="mt-0.5 h-4 w-4 shrink-0 accent-primary" type="checkbox" checked={draft.history_verified}
          disabled={busy || UI_PREVIEW_ONLY} onChange={event => change('history_verified', event.target.checked)} />
        <span>已人工核对该账号的完整历史
          <span className="mt-1 block text-xs leading-relaxed text-muted-foreground">包括历史付款、卡密购买和其他额度来源；数据缺失时保持未确认。</span>
        </span>
      </label>
      <label className="block space-y-2 text-sm">
        <span className="text-xs font-medium text-muted-foreground">核实依据与原因 <span className="text-destructive">*</span></span>
        <textarea className="min-h-24 w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
          aria-label="核实依据与原因" maxLength={1000} required disabled={busy || UI_PREVIEW_ONLY} value={draft.reason}
          onChange={event => change('reason', event.target.value)} placeholder="记录已核对的来源和依据，不能只依据当前余额或无付款记录判断。" />
      </label>
      {error && <p className="break-words text-sm text-destructive" role="alert">{error}</p>}
      {saved && <p className="text-sm text-emerald-600 dark:text-emerald-400" role="status">来源核实已保存。</p>}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="text-xs text-muted-foreground">不触发封禁、额度回收或补签。</span>
        <Button type="submit" size="sm" disabled={busy || UI_PREVIEW_ONLY || !draft.reason.trim()}>
          {busy ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}保存核实
        </Button>
      </div>
    </form>
  </details>
}
