import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Activity, FileSearch, KeyRound, Loader2, RefreshCw, ShieldCheck, UserPlus } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { ScrollRegion } from './components/ScrollRegion'
import { RiskRelatedEvidence, RiskReviewPanel } from './RiskEvidence'
import type { ManualRiskReview, RelatedRiskEvidence } from './RiskEvidence'

type RiskFeatures = {
  calls: number
  previous_calls: number
  current_quota: number
  previous_quota: number
  burst_ratio: number
  baseline_available: boolean
  successful_calls: number
  failed_calls: number
  peak_rpm: number
  max_token_minute_ips: number
  max_token_ips_at_high_rpm?: number
  login_successes: number
  login_failures: number
  attempted_account_failures: number
  pending_logins: number
  new_browser_logins: number
  registration_successes: number
  call_ips: string[]
  login_ips: string[]
  registration_ips: string[]
  failure_rate?: number
  previous_failure_rate?: number
  failure_baseline_available?: boolean
  quota_burst_ratio?: number
  model_failures?: { model: string; calls: number; failures: number; previous_calls: number; previous_failures: number; failure_rate: number; previous_failure_rate: number }[]
}

type RiskFinding = { id: string; source: string; score: number; description: string }
type RiskAI = {
  status: string
  risk_level?: string
  confidence?: number
  evidence_ids?: string[]
  reason?: string
  recommendation?: string
}
type UserRiskAnalysis = {
  id?: string
  user_id: number
  window_start: number
  window_end: number
  created_at: number
  rule_version: number
  features: RiskFeatures
  findings: RiskFinding[]
  score: number
  level: string
  proposed_action: string
  auto_eligible: boolean
  whitelisted: boolean
  dry_run: boolean
  ai: RiskAI
  action_id?: string
  action_error?: string
  data_notes: string[]
  history_complete: boolean
  history_coverage_start?: number
  related_evidence?: RelatedRiskEvidence[]
  related_evidence_truncated?: boolean
  model_failures_truncated?: boolean
  review?: ManualRiskReview
  latest_review?: ManualRiskReview
  sync_state?: string
}

type RequestScope = {
  userId: number
  tokenVersion: number
  sessionToken: string
  revision: number
}
type QueryResult = RequestScope & (
  | { phase: 'loading' }
  | { phase: 'success'; data: UserRiskAnalysis }
  | { phase: 'error'; error: string; unauthorized?: boolean }
)

export type UserRiskAnalysisPanelProps = {
  userId: number
  username: string
  tokenVersion?: number
  onUnauthorized: () => void
}

const numbers = new Intl.NumberFormat('zh-CN')
const timestamps = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})
const actionLabels: Record<string, string> = {
  observe: '持续观察', review: '人工复核', ban: '建议封禁（规则）', unban: '建议解除封禁',
}
const levelLabels: Record<string, string> = {
  high: '重点关注', medium: '需要复核', low: '未命中高分规则', unknown: '待确认',
}
const aiLabels: Record<string, string> = {
  not_requested: '未调用', disabled: '未启用', unconfigured: '未配置',
  complete: '已完成', assessed: '已完成', ok: '已完成', success: '已完成',
  unknown: '待确认', failed: '评估失败', error: '评估失败', unavailable: '暂不可用',
  invalid: '响应格式无效',
}
const sourceLabels: Record<string, string> = {
  api_call: 'API 调用', login: '用户登录', registration: '用户注册', login_registration: '成功登录 / 注册',
}
const findingLabels: Record<string, { title: string; description: string }> = {
  token_fanout_with_burst: {
    title: '令牌多出口与调用突增',
    description: '同一个自然分钟内，账号达到配置的 RPM 阈值，且同一令牌使用多个公共出口；不会拼接不同分钟的峰值。',
  },
  repeated_login_failures: {
    title: '连续登录失败',
    description: '已确认关联该账号的登录失败次数达到复核阈值。',
  },
  claimed_account_login_failures: {
    title: '未认证的账号登录尝试',
    description: '失败尝试填写了该账号，但账号关联未经认证；仅作为复核线索。',
  },
  login_failures_and_new_browser: {
    title: '失败尝试与新浏览器登录',
    description: '窗口内同时出现登录失败和可用历史中未见过的浏览器，需要人工复核。',
  },
  shared_verified_browser_accounts: {
    title: '同浏览器多账号认证',
    description: '同一 BrowserID 成功登录或注册的账号数达到阈值；共享浏览器等正常解释仍需人工核实。',
  },
  shared_token_accounts: {
    title: '多账号共享调用令牌',
    description: '调用日志确认同一令牌关联多个账号；共享网关或转发服务需要人工排除。',
  },
  call_failure_rate_spike: {
    title: '调用失败率突增',
    description: '两窗口样本充足，失败率和增幅均达到复核阈值；上游、模型或客户端故障均可能造成该现象。',
  },
  quota_consumption_spike: {
    title: '额度消耗突增',
    description: '上一窗口调用和额度样本充足，消耗增幅达到阈值；价格变化和业务增长需要人工复核。',
  },
  model_failure_concentration: {
    title: '模型集中失败',
    description: '同一模型两窗口样本充足，当前失败率与增幅达到阈值；仅代表调用质量复核线索。',
  },
}
const noteLabels: Record<string, string> = {
  'Findings describe observed events, not proof that the account is safe.': '规则命中仅描述已观测事件，不代表账号安全结论。',
  'Login-failure counts include verified account associations only; attempts naming an account are excluded.': '登录失败只统计已确认关联该账号的认证事件；未认证账号尝试单独列示。',
  'New browser means unseen in available prior events; missing probe history is not proof of a first-ever device.': '新浏览器指可用历史中未见过的浏览器，不代表该账号首次使用此设备。',
  'The previous equal-length window has insufficient calls for automatic-action baselining.': '上一等长窗口调用样本不足，未建立自动处置基线。',
  'The observed traffic does not exceed the configured growth ratio over its baseline.': '调用增幅未达到配置的基线增长阈值。',
  'Historical call coverage is not declared complete; automatic actions are disabled.': '调用历史覆盖尚未确认完整，自动处置资格已抑制。',
  'Historical call coverage does not cover both comparison windows; automatic actions are disabled.': '调用历史声明或保留范围未覆盖当前窗口和上一基线窗口，自动处置资格已抑制。',
  'AI did not return a usable review; automatic execution is blocked for this assessment.': 'AI 未返回有效审核意见，本次自动处置已暂停。',
}

function sameScope(left: RequestScope, right: RequestScope) {
  return left.userId === right.userId && left.tokenVersion === right.tokenVersion &&
    left.sessionToken === right.sessionToken && left.revision === right.revision
}

function count(value?: number | null) {
  return value == null || !Number.isFinite(value) ? '—' : numbers.format(value)
}

function timestamp(value: number) {
  return value > 0 && Number.isFinite(value) ? timestamps.format(value * 1000) : '—'
}

function errorMessage(cause: unknown) {
  const message = cause instanceof Error ? cause.message : '无法读取当前账号的风控分析。'
  const messages: Record<string, string> = {
    'network events are not initialized': '网络事件采集尚未初始化。',
    'user was not found': '当前账号不存在或已被移除。',
    'risk operation is temporarily unavailable': '风控分析暂不可用，请稍后重试。',
    'call activity import is incomplete or unavailable': '调用记录尚未同步完成，请稍后重试。',
  }
  return messages[message] ?? message
}

function Metric({ label, children }: { label: string; children: ReactNode }) {
  return <div className="min-w-0 rounded-lg bg-muted/20 p-4 text-center shadow-sm">
    <div className="mb-1 text-xs text-muted-foreground">{label}</div>
    <div className="break-words text-xl font-bold tabular-nums">{children}</div>
  </div>
}

function SourceDetails({ title, icon, rows, ips }: {
  title: string
  icon: ReactNode
  rows: { label: string; value: ReactNode; detail: string }[]
  ips: string[]
}) {
  return <section className="min-w-0 space-y-3">
    <h4 className="flex items-center gap-2 text-sm font-semibold">{icon}{title}</h4>
    <Table className="min-w-[460px]">
      <TableHeader><TableRow>
        <TableHead className="h-9">指标</TableHead>
        <TableHead className="h-9 whitespace-nowrap text-right">当前窗口</TableHead>
        <TableHead className="h-9">说明</TableHead>
      </TableRow></TableHeader>
      <TableBody>{rows.map(row => <TableRow key={row.label}>
        <TableCell className="py-2.5 text-xs">{row.label}</TableCell>
        <TableCell className="whitespace-nowrap py-2.5 text-right font-mono text-sm">{row.value}</TableCell>
        <TableCell className="py-2.5 text-xs text-muted-foreground">{row.detail}</TableCell>
      </TableRow>)}</TableBody>
    </Table>
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">{title} IP <Badge variant="secondary" className="px-1.5 py-0 font-normal">{count(ips.length)}</Badge></div>
      {ips.length ? <ScrollRegion label={`${title} IP 列表`} size="sm" className="flex flex-wrap gap-1.5 rounded-xl border bg-muted/10 p-3">
        {ips.map(ip => <code key={ip} className="max-w-full break-all rounded border bg-muted/30 px-2 py-1 text-xs">{ip}</code>)}
      </ScrollRegion> : <p className="text-xs text-muted-foreground">当前窗口暂无该来源的已观测 IP。</p>}
    </div>
  </section>
}

export default function UserRiskAnalysisPanel({
  userId, username, tokenVersion = 0, onUnauthorized,
}: UserRiskAnalysisPanelProps) {
  const [revision, setRevision] = useState(0)
  const [result, setResult] = useState<QueryResult | null>(null)
  const serialRef = useRef(0)
  const unauthorizedRef = useRef(onUnauthorized)
  unauthorizedRef.current = onUnauthorized
  const sessionToken = sessionStorage.getItem('siteadmin-token') ?? ''
  const scope = { userId, tokenVersion, sessionToken, revision }
  const latestScope = useRef<RequestScope>(scope)
  latestScope.current = scope

  useEffect(() => {
    const serial = ++serialRef.current
    const controller = new AbortController()
    const requestScope: RequestScope = { userId, tokenVersion, sessionToken, revision }
    // Account and session changes invalidate callbacks even before effect cleanup.
    const isCurrent = () => !controller.signal.aborted && serialRef.current === serial &&
      sameScope(latestScope.current, requestScope) &&
      (sessionStorage.getItem('siteadmin-token') ?? '') === sessionToken
    const cleanup = () => {
      controller.abort()
      if (serialRef.current === serial) serialRef.current++
    }
    if (UI_PREVIEW_ONLY) {
      setResult(null)
      return cleanup
    }
    if (!Number.isSafeInteger(userId) || userId <= 0) {
      setResult({ ...requestScope, phase: 'error', error: '账号 ID 无效。' })
      return cleanup
    }
    setResult({ ...requestScope, phase: 'loading' })
    void requestJSON<UserRiskAnalysis>(`/api/risk/users/${userId}/analysis`, {}, controller.signal)
      .then(data => {
        if (!isCurrent()) return
        if (data.user_id !== userId || !data.features || !Array.isArray(data.findings)) {
          throw new Error('返回的分析与当前账号不匹配，请刷新重试。')
        }
        setResult({ ...requestScope, phase: 'success', data })
      })
      .catch(cause => {
        if (!isCurrent() || cause instanceof DOMException && cause.name === 'AbortError') return
        const unauthorized = cause instanceof ApiError && cause.status === 401
        setResult({
          ...requestScope, phase: 'error', unauthorized,
          error: unauthorized ? '管理员会话已失效，请重新连接。' : errorMessage(cause),
        })
        if (unauthorized) unauthorizedRef.current()
      })
    return cleanup
  }, [userId, tokenVersion, sessionToken, revision])

  const current = result && sameScope(result, scope) ? result : null
  const analysis = current?.phase === 'success' ? current.data : null
  const loading = !UI_PREVIEW_ONLY && (!current || current.phase === 'loading')
  const features = analysis?.features
  const notes = [...new Set((analysis?.data_notes ?? []).map(note => noteLabels[note] ?? note))]

  return <div className="um-detail um-detail--risk min-w-0 space-y-6" aria-busy={loading}>
    <div className="um-detail-section um-detail-section--risk">
      <header>
        <h4>风控分析</h4>
        <p>规则评分、证据分层与处理建议</p>
      </header>
      <div className="um-detail-section-toolbar">
        <span className="um-detail-section-user">{username || `账号 #${userId}`}</span>
        <Button variant="outline" size="sm" type="button" disabled={loading || UI_PREVIEW_ONLY} onClick={() => setRevision(value => value + 1)}>
          <RefreshCw className={`mr-2 h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />刷新
        </Button>
      </div>
    </div>

    {UI_PREVIEW_ONLY ? <p className="py-6 text-center text-sm text-muted-foreground" role="status">数据获取已暂停</p>
      : current?.phase === 'error' ? <Notice kind="error">
        <div className="flex flex-wrap items-center justify-between gap-3"><span>{current.error}</span>
          {!current.unauthorized && <Button variant="outline" size="sm" type="button" onClick={() => setRevision(value => value + 1)}>重试</Button>}
        </div>
      </Notice> : loading ? <div className="flex min-h-32 items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="h-5 w-5 animate-spin text-primary" />正在读取当前账号的规则分析...</div> : null}

    <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
      <Metric label="规则评分">{analysis ? <>{count(analysis.score)}<span className="text-xs font-normal text-muted-foreground"> / 100</span></> : '—'}</Metric>
      <Metric label="规则风险等级">{analysis ? levelLabels[analysis.level] ?? analysis.level : '—'}</Metric>
      <Metric label="处理意见">{analysis ? actionLabels[analysis.proposed_action] ?? analysis.proposed_action : '—'}</Metric>
      <Metric label="规则版本">{analysis ? `v${analysis.rule_version}` : '—'}</Metric>
    </div>

    {analysis && features && <>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={analysis.level === 'high' ? 'destructive' : analysis.level === 'medium' ? 'warning' : 'secondary'}>{levelLabels[analysis.level] ?? analysis.level}</Badge>
          <Badge variant={analysis.history_complete ? 'outline' : 'warning'}>{analysis.history_complete ? '调用与基线窗口已覆盖' : '历史覆盖不足或待确认'}</Badge>
          <Badge variant="secondary">{features.baseline_available ? '调用基线充足' : '调用基线不足'}</Badge>
          {analysis.whitelisted && <Badge variant="success">已排除自动处置</Badge>}
          <Badge variant="outline">{analysis.dry_run ? '试运行' : '正式策略'}</Badge>
          {analysis.sync_state && <Badge variant={analysis.sync_state === 'completed' ? 'outline' : 'warning'}>{analysis.sync_state === 'completed' ? '调用同步完成' : analysis.sync_state === 'queued' ? '调用同步排队中' : '调用后台同步中'}</Badge>}
        </div>
        <dl className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
          <div><dt className="text-muted-foreground">分析窗口</dt><dd className="mt-1 break-words leading-relaxed">{timestamp(analysis.window_start)} 至 {timestamp(analysis.window_end)}</dd></div>
          <div><dt className="text-muted-foreground">分析时间（浏览器时区）</dt><dd className="mt-1">{timestamp(analysis.created_at)}</dd></div>
          {!!analysis.history_coverage_start && <div className="sm:col-span-2"><dt className="text-muted-foreground">调用历史需覆盖（包含上一基线窗口）</dt><dd className="mt-1">{timestamp(analysis.history_coverage_start)} 至 {timestamp(analysis.window_end)}</dd></div>}
        </dl>
        {!analysis.history_complete && <Notice kind="warning">调用历史覆盖尚未确认完整，当前分数仅反映已观测事件，自动处置资格已抑制。</Notice>}
      </div>

      <SourceDetails title="API 调用" icon={<Activity className="h-4 w-4 text-primary" />} ips={features.call_ips ?? []} rows={[
        { label: '调用次数', value: count(features.calls), detail: '仅统计 API 调用事件' },
        { label: '成功 / 失败', value: `${count(features.successful_calls)} / ${count(features.failed_calls)}`, detail: '当前分析窗口' },
        { label: '峰值 RPM', value: count(features.peak_rpm), detail: '单分钟最高调用次数' },
        { label: '令牌多出口', value: count(features.max_token_minute_ips), detail: '同一令牌在同一分钟的最多出口数' },
        { label: '达到 RPM 时的令牌出口', value: count(features.max_token_ips_at_high_rpm), detail: '仅取账号达到 RPM 阈值的相同自然分钟' },
        { label: '上一窗口调用', value: count(features.previous_calls), detail: '前一个等长分析窗口' },
        { label: '调用增幅', value: features.baseline_available ? `${features.burst_ratio.toFixed(2)} 倍` : '基线不足', detail: '当前调用数 / 上一窗口调用数' },
        { label: '当前消耗', value: count(features.current_quota), detail: '原始配额单位' },
        { label: '上一窗口消耗', value: count(features.previous_quota), detail: '原始配额单位' },
        { label: '失败率 / 上一窗口', value: `${((features.failure_rate ?? 0) * 100).toFixed(1)}% / ${((features.previous_failure_rate ?? 0) * 100).toFixed(1)}%`, detail: features.failure_baseline_available ? '仅计算已知成功或失败结果' : '上一窗口基线不足' },
        { label: '额度消耗增幅', value: features.previous_quota > 0 ? `${(features.quota_burst_ratio ?? 0).toFixed(2)} 倍` : '基线不足', detail: '当前消耗 / 上一等长窗口消耗' },
      ]} />

      <SourceDetails title="用户登录" icon={<KeyRound className="h-4 w-4 text-primary" />} ips={features.login_ips ?? []} rows={[
        { label: '成功登录', value: count(features.login_successes), detail: '已确认关联该账号' },
        { label: '认证失败', value: count(features.login_failures), detail: '已确认关联该账号' },
        { label: '未认证账号尝试', value: count(features.attempted_account_failures), detail: '填写了该账号，关联未经认证' },
        { label: '待二次验证', value: count(features.pending_logins), detail: '尚未完成登录' },
        { label: '新浏览器登录', value: count(features.new_browser_logins), detail: '可用历史中未见过的浏览器' },
      ]} />

      <SourceDetails title="用户注册" icon={<UserPlus className="h-4 w-4 text-primary" />} ips={features.registration_ips ?? []} rows={[
        { label: '注册成功', value: count(features.registration_successes), detail: '仅统计注册事件' },
        { label: '已观测出口', value: count(features.registration_ips?.length ?? 0), detail: '仅来自当前窗口内的注册事件' },
      ]} />
      <p className="text-xs leading-relaxed text-muted-foreground">调用、登录和注册分别统计。IP 仅反映已观测出口，代理或 VPN 属性不直接构成恶意结论。</p>
      <RiskRelatedEvidence evidence={analysis.related_evidence} truncated={analysis.related_evidence_truncated} />
      {!!features.model_failures?.length && <section className="space-y-3"><h4 className="text-sm font-semibold">模型质量复核</h4><Table className="min-w-[460px]" containerClassName="scroll-region scroll-region--sm" containerLabel="模型调用质量"><TableHeader><TableRow><TableHead>模型</TableHead><TableHead>调用 / 失败</TableHead><TableHead>当前失败率</TableHead><TableHead>上一窗口失败率</TableHead></TableRow></TableHeader><TableBody>{features.model_failures.map(model => <TableRow key={model.model}><TableCell className="max-w-64 break-all text-xs">{model.model}</TableCell><TableCell className="text-xs">{count(model.calls)} / {count(model.failures)}</TableCell><TableCell>{(model.failure_rate * 100).toFixed(1)}%</TableCell><TableCell>{model.previous_calls ? `${(model.previous_failure_rate * 100).toFixed(1)}%` : '样本不足'}</TableCell></TableRow>)}</TableBody></Table>{analysis.model_failures_truncated && <p className="text-xs text-amber-600">显示失败次数最多的前 20 个模型，规则计算覆盖全部模型。</p>}</section>}

      <section className="min-w-0 space-y-3">
        <h4 className="flex items-center gap-2 text-sm font-semibold"><FileSearch className="h-4 w-4 text-primary" />命中规则证据<Badge variant="secondary">{count(analysis.findings.length)}</Badge></h4>
        <Table className="min-w-[560px]">
          <TableHeader><TableRow><TableHead className="h-9">规则</TableHead><TableHead className="h-9">来源</TableHead><TableHead className="h-9 text-right">分值</TableHead><TableHead className="h-9">证据说明</TableHead></TableRow></TableHeader>
          <TableBody>{analysis.findings.map(finding => <TableRow key={finding.id}>
            <TableCell className="py-3 text-xs font-medium">{findingLabels[finding.id]?.title ?? finding.id}</TableCell>
            <TableCell className="whitespace-nowrap py-3 text-xs">{sourceLabels[finding.source] ?? finding.source}</TableCell>
            <TableCell className="py-3 text-right font-mono text-xs">+{count(finding.score)}</TableCell>
            <TableCell className="py-3 text-xs leading-relaxed text-muted-foreground">{findingLabels[finding.id]?.description ?? finding.description}</TableCell>
          </TableRow>)}</TableBody>
        </Table>
        {analysis.findings.length === 0 && <p className="py-3 text-center text-xs text-muted-foreground">当前窗口没有命中已配置规则。</p>}
        <p className="text-xs text-muted-foreground">自动处置资格：{analysis.auto_eligible ? '组合规则条件已满足' : '未满足自动处置条件'}。AI 意见不单独触发封禁。</p>
      </section>

      <section className="space-y-3 rounded-lg border bg-muted/20 p-4">
        <div className="flex flex-wrap items-center gap-2"><h4 className="text-sm font-semibold">AI 辅助状态</h4><Badge variant="secondary">{aiLabels[analysis.ai?.status] ?? analysis.ai?.status ?? '—'}</Badge></div>
        {analysis.ai?.reason && <p className="text-sm leading-relaxed">{analysis.ai.reason}</p>}
        {analysis.ai?.risk_level && <dl className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-3">
          <div><dt className="text-muted-foreground">模型判断</dt><dd className="mt-1">{levelLabels[analysis.ai.risk_level] ?? analysis.ai.risk_level}</dd></div>
          <div><dt className="text-muted-foreground">模型置信度</dt><dd className="mt-1">{analysis.ai.confidence == null ? '—' : `${Math.round(analysis.ai.confidence * 100)}%`}</dd></div>
          <div><dt className="text-muted-foreground">模型意见</dt><dd className="mt-1">{actionLabels[analysis.ai.recommendation ?? ''] ?? analysis.ai.recommendation ?? '—'}</dd></div>
        </dl>}
      </section>
      <RiskReviewPanel userId={userId} assessmentId={analysis.id} review={analysis.review} latestReview={analysis.latest_review} onUnauthorized={onUnauthorized} onSaved={() => setRevision(value => value + 1)} />
      {notes.length > 0 && <details className="border-t pt-3">
        <summary className="cursor-pointer text-xs text-muted-foreground">数据说明</summary>
        <ul className="mt-3 list-disc space-y-1 pl-5 text-xs leading-relaxed text-muted-foreground">{notes.map(note => <li key={note}>{note}</li>)}</ul>
      </details>}
      {analysis.action_error && <Notice kind="error">{analysis.action_error}</Notice>}
    </>}
  </div>
}
