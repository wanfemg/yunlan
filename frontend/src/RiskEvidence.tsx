import { useEffect, useRef, useState } from 'react'
import { LoaderCircle, Save, UsersRound } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Select } from './components/ui/select'
import { UserIdentityButton } from './components/UserIdentityButton'

export type RiskReviewRules = {
  related_accounts_enabled: boolean
  related_account_min_users: number
  call_failures_enabled: boolean
  failure_rate_threshold: number
  failure_rate_increase: number
  quota_spike_enabled: boolean
  quota_burst_ratio: number
  quota_min_previous: number
  model_failures_enabled: boolean
  model_min_calls: number
  model_failure_rate_threshold: number
}

export const defaultReviewRules: RiskReviewRules = {
  related_accounts_enabled: true, related_account_min_users: 2,
  call_failures_enabled: true, failure_rate_threshold: .5, failure_rate_increase: .25,
  quota_spike_enabled: true, quota_burst_ratio: 3, quota_min_previous: 500000,
  model_failures_enabled: true, model_min_calls: 20, model_failure_rate_threshold: .6,
}

export type RelatedRiskEvidence = {
  type: string
  source: string
  strength: string
  key: string
  user_ids: number[]
  total_users: number
  truncated: boolean
  first_seen: number
  last_seen: number
  description: string
}

export type ManualRiskReview = {
  id: string
  user_id: number
  assessment_id: string
  status: 'pending' | 'confirmed' | 'false_positive' | 'watching'
  reason: string
  operator: string
  rule_version: number
  detection_revision: number
  created_at: number
}

export const reviewLabels: Record<ManualRiskReview['status'], string> = {
  pending: '待复核', confirmed: '已核实', false_positive: '误报', watching: '待观察',
}

export function RiskRelatedEvidence({ evidence, truncated, onViewUser }: {
  evidence?: RelatedRiskEvidence[]
  truncated?: boolean
  onViewUser?: (id: number) => void
}) {
  return <section className="rc-related-evidence space-y-3">
    <div className="flex items-center gap-2 text-sm font-semibold"><UsersRound className="h-4 w-4 text-primary" />关联账号证据<Badge variant="secondary">{evidence?.length ?? 0}</Badge></div>
    {evidence?.length ? <div className="grid gap-3">{evidence.map(item => <article key={`${item.type}:${item.key}`} className="rounded-xl border bg-muted/10 p-4">
      <div className="flex flex-wrap items-center gap-2"><strong className="text-sm">{item.type === 'shared_verified_browser_id' ? '同浏览器成功认证' : '共享调用令牌'}</strong><Badge variant="outline">{item.strength === 'high' ? '强关联线索' : '辅助线索'}</Badge><span className="ml-auto text-xs text-muted-foreground">{item.total_users} 个账号</span></div>
      <p className="mt-2 text-xs leading-relaxed text-muted-foreground">{item.description}</p>
      <div className="mt-3 flex flex-wrap gap-1.5">{item.user_ids.map(id => onViewUser
        ? <UserIdentityButton key={id} userId={id} compact onClick={() => onViewUser(id)} />
        : <Badge key={id} variant="secondary">账号 #{id}</Badge>)}</div>
      {item.truncated && <p className="mt-2 text-xs text-amber-600">显示其中 {item.user_ids.length} 个账号，总人数为完整统计。</p>}
    </article>)}</div> : <p className="text-xs leading-relaxed text-muted-foreground">当前窗口未观测到同浏览器成功认证或共享令牌关联；缺少探针和历史记录时不能据此认定安全。</p>}
    {truncated && <Notice kind="warning">关联组达到展示上限，规则计算使用全部关联组。</Notice>}
  </section>
}

export function RiskReviewPanel({ userId, assessmentId, review, latestReview, disabled, onSaved, onUnauthorized }: {
  userId: number
  assessmentId?: string
  review?: ManualRiskReview
  latestReview?: ManualRiskReview
  disabled?: boolean
  onSaved?: (value: ManualRiskReview) => void
  onUnauthorized?: () => void
}) {
  const current = review ?? (latestReview?.assessment_id === assessmentId ? latestReview : undefined)
  const [saved, setSaved] = useState(current)
  const [status, setStatus] = useState<ManualRiskReview['status']>(current?.status ?? 'watching')
  const [reason, setReason] = useState(current?.reason ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const operation = useRef<AbortController | null>(null)
  useEffect(() => {
    setSaved(current)
    setStatus(current?.status ?? 'watching')
    setReason(current?.reason ?? '')
    setError('')
    setBusy(false)
    return () => {
      operation.current?.abort()
      operation.current = null
    }
  }, [userId, assessmentId, current?.id])

  async function save() {
    if (!assessmentId || operation.current || UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    operation.current = controller
    setBusy(true)
    setError('')
    try {
      const value = await requestJSON<ManualRiskReview>(`/api/risk/users/${userId}/review`, {
        method: 'PUT', body: JSON.stringify({ assessment_id: assessmentId, status, reason: reason.trim() }),
      }, controller.signal)
      if (controller.signal.aborted) return
      setSaved(value)
      onSaved?.(value)
    } catch (cause) {
      if (controller.signal.aborted) return
      setError(cause instanceof Error ? cause.message : '复核结果保存失败。')
      if (cause instanceof ApiError && cause.status === 401) onUnauthorized?.()
    } finally {
      if (operation.current === controller) operation.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return <section className="rc-manual-review space-y-3 rounded-xl border bg-muted/10 p-4">
    <div className="flex flex-wrap items-center gap-2"><h4 className="text-sm font-semibold">人工复核与误报反馈</h4>{saved && <Badge variant={saved.status === 'false_positive' ? 'success' : 'secondary'}>{reviewLabels[saved.status]}</Badge>}</div>
    {saved && <p className="text-xs text-muted-foreground">最近保存于 {new Date(saved.created_at * 1000).toLocaleString('zh-CN')} · {saved.operator}</p>}
    {latestReview && latestReview.assessment_id !== assessmentId && !saved && <div className="rounded-lg border bg-background/60 p-3 text-xs leading-relaxed"><strong>上一评估：{reviewLabels[latestReview.status]}</strong><p className="mt-1 text-muted-foreground">{latestReview.reason}</p><p className="mt-1 text-muted-foreground">该结论对应另一条评估，不会自动覆盖当前证据。</p></div>}
    {assessmentId ? <>
      <div className="grid gap-3 sm:grid-cols-[160px_minmax(0,1fr)]"><label className="rc-field"><span>复核结论</span><Select disabled={busy || disabled} value={status} onChange={event => setStatus(event.target.value as ManualRiskReview['status'])}>{Object.entries(reviewLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</Select></label><label className="rc-field"><span>判断理由</span><textarea className="rc-textarea" rows={3} maxLength={1000} disabled={busy || disabled} placeholder="记录核实证据、正常业务解释或继续观察的原因" value={reason} onChange={event => setReason(event.target.value)} /></label></div>
      {error && <Notice kind="error">{error}</Notice>}
      <div className="flex flex-wrap items-center justify-between gap-3"><p className="max-w-xl text-xs leading-relaxed text-muted-foreground">结果保存到管理数据库和审计日志。误报仅移出本次评估的可疑队列；后续新证据仍会重新检测，账号状态不变。</p><Button size="sm" type="button" disabled={busy || disabled || UI_PREVIEW_ONLY || reason.trim().length < 2} onClick={() => void save()}>{busy ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}保存复核</Button></div>
    </> : <p className="text-xs leading-relaxed text-muted-foreground">当前为即时规则分析。到 AI 审核中发起并保存账号评估后，记录核实、误报或待观察结论。</p>}
  </section>
}
