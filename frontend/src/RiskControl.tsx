import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import {
  Activity, ArrowRight, Check, ChevronDown, ClipboardList, Eye, EyeOff, FileSearch, Globe,
  History, Info, KeyRound, LoaderCircle, PencilLine, RefreshCw, RotateCcw, Rows3,
  Save, Search, Settings2, ShieldAlert, ShieldBan, ShieldCheck, Square, X,
} from 'lucide-react'
import { actionKey, ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, EmptyState, Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './components/ui/dialog'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import { ScrollRegion } from './components/ScrollRegion'
import { UserIdentityButton } from './components/UserIdentityButton'
import RiskIPMonitor from './RiskIPMonitor'
import RiskRealtimeRanking from './RiskRealtimeRanking'
import RiskUserCard from './RiskUserCard'
import { defaultReviewRules, reviewLabels, RiskRelatedEvidence, RiskReviewPanel } from './RiskEvidence'
import type { ManualRiskReview, RelatedRiskEvidence, RiskReviewRules } from './RiskEvidence'
import './risk-control.css'

type AIConfig = {
  enabled: boolean
  base_url: string
  api_key?: string
  api_key_configured?: boolean
  model: string
  timeout_seconds: number
}
type RiskConfig = {
  version: number
  enabled: boolean
  auto_execute: boolean
  dry_run: boolean
  scan_interval_seconds: number
  window_seconds: number
  min_requests: number
  max_requests_per_minute: number
  concurrent_token_ips: number
  min_burst_ratio: number
  login_failures_threshold: number
  review_rules: RiskReviewRules
  whitelist: number[]
  ai: AIConfig
}
type Features = {
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
  model_failures?: { model: string; calls: number; failures: number; failure_rate: number; previous_calls: number; previous_failures: number; previous_failure_rate: number }[]
}
type Finding = { id: string; source: string; score: number; description: string }
type AIResult = {
  status: string
  verified?: boolean
  endpoint?: string
  model?: string
  risk_level?: string
  confidence?: number
  evidence_ids?: string[]
  reason?: string
  recommendation?: string
}
type AIModel = { id: string; owned_by?: string }
type AIModels = { models: AIModel[]; total: number; endpoint: string }
type Assessment = {
  id?: string
  user_id: number
  username?: string
  window_start: number
  window_end: number
  created_at: number
  rule_version: number
  detection_revision?: number | string
  features: Features
  findings: Finding[]
  score: number
  level: string
  proposed_action: string
  auto_eligible: boolean
  whitelisted: boolean
  dry_run: boolean
  ai: AIResult
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
type ScanJob = {
  id: string
  state: string
  config: RiskConfig
  user_ids: number[]
  cursor: number
  scanned: number
  assessed: number
  banned: number
  errors: string[]
  created_at: number
  updated_at: number
  window_end: number
}
type UserSnapshot = { id: number; username?: string; status: number; role: number; auth_version: number; auth_version_known: boolean }
type RemarkSync = { state: 'pending' | 'synced' | 'failed'; before: string; after: string; error?: string; synced_at?: number }
type UserRemark = { user_id: number; remark: string; remark_supported: boolean; limit: number }
type NoteTarget = { user_id: number; action_id?: string }
type RiskAction = {
  id: string
  user_id: number
  username?: string
  display_name?: string
  action: string
  reason: string
  note?: string
  note_updated_at?: number
  remark_sync?: RemarkSync
  operator: string
  state: string
  owns_transition: boolean
  before: UserSnapshot
  after: UserSnapshot
  releases?: string
  released_by?: string
  created_at: number
  updated_at: number
  error?: string
  assessment_id?: string
  rule_version?: number
  evidence_ids?: string[]
}
type AuditRecord = {
  id: string
  event: string
  user_id?: number
  username?: string
  display_name?: string
  action_id?: string
  action?: string
  operator?: string
  reason?: string
  note?: string
  previous_note?: string
  remark_sync?: RemarkSync
  error?: string
  state?: string
  previous_state?: string
  resolution?: string
  created_at: number
  before?: UserSnapshot
  after?: UserSnapshot
  observed_user_state?: UserSnapshot
  user_ids?: number[]
  usernames?: string[]
  affected?: number
  warning?: string
  [key: string]: unknown
}
type RecordType = Assessment | ScanJob | RiskAction | AuditRecord
type HistoryTab = 'assessments' | 'jobs' | 'actions' | 'audit'
type ReviewTab = 'assessments' | 'jobs'
type Tab = 'ranking' | 'matrix' | 'ip' | 'actions' | 'audit' | 'ai'
type HistoryPage<T> = { items: T[]; limit: number; offset: number; total: number; has_more: boolean; next_offset: number | null }
type CandidatePage = HistoryPage<Assessment> & { window_start: number; as_of: number; rule_version: number; detection_revision?: number | string; truncated: boolean }
type RecordDetail = { kind: HistoryTab; record: RecordType }
type ActionDialog = { kind: 'ban' | 'release' | 'reconcile'; key: string; record?: RiskAction }
type BannedUser = {
  user: { id: number; username: string; display_name: string; email: string; role: number; status: number; group: string; quota: number; used_quota: number; upstream_request_count: number; remark?: string; remark_supported?: boolean }
  ban: RiskAction | null
  owned: boolean
  last_action: RiskAction | null
}
type AccountMatrixMember = {
  user: { id: number; username: string; display_name?: string; email?: string; group?: string }
  browser_ids?: string[]; login_ips?: string[]; call_ips?: string[]; token_ids?: string[]; inviter_id?: number
}
type AccountMatrixGroup = {
  id: string; strength: 'high' | 'medium'; level: string; reason: string
  members: AccountMatrixMember[]; evidence: { type: string; strength: string; description: string }[]
  total_users?: number; truncated?: boolean
}
type AccountMatrixData = {
  as_of: number; window_start: number; history_complete: boolean
  groups: AccountMatrixGroup[]; warnings: string[]
  total_groups?: number; truncated?: boolean
}

const PAGE_SIZE = 15
const NOTE_LIMIT = 255
const scanSchedules = [
  { seconds: 0, label: '已禁用' },
  { seconds: 60, label: '每 1 分钟' },
  { seconds: 300, label: '每 5 分钟' },
  { seconds: 900, label: '每 15 分钟' },
  { seconds: 1800, label: '每 30 分钟' },
  { seconds: 3600, label: '每 1 小时' },
  { seconds: 7200, label: '每 2 小时' },
  { seconds: 21600, label: '每 6 小时' },
  { seconds: 43200, label: '每 12 小时' },
  { seconds: 86400, label: '每 24 小时' },
]
const ruleFields = [
  { key: 'window_seconds', label: '分析窗口（秒）', min: 60, max: 2592000, step: 1 },
  { key: 'min_requests', label: '最小调用样本', min: 2, max: 10000000, step: 1 },
  { key: 'max_requests_per_minute', label: '峰值 RPM 阈值', min: 2, max: 10000000, step: 1 },
  { key: 'concurrent_token_ips', label: '令牌每分钟出口阈值', min: 2, max: 10000, step: 1 },
  { key: 'min_burst_ratio', label: '较基线最小增幅（倍）', min: 1.5, max: 1000, step: 0.1 },
  { key: 'login_failures_threshold', label: '登录失败复核阈值', min: 2, max: 100000, step: 1 },
] as const
const reviewRuleFields = [
  { key: 'related_account_min_users', label: '同标识关联账号数', min: 2, max: 1000, step: 1 },
  { key: 'failure_rate_threshold', label: '调用失败率阈值（0～1）', min: .01, max: 1, step: .01 },
  { key: 'failure_rate_increase', label: '较基线失败率增幅（0～1）', min: .01, max: 1, step: .01 },
  { key: 'quota_burst_ratio', label: '额度消耗增幅（倍）', min: 1.5, max: 1000, step: .1 },
  { key: 'quota_min_previous', label: '上一窗口最小额度（原始单位）', min: 1, max: 1000000000000000, step: 1 },
  { key: 'model_min_calls', label: '模型两窗口最小样本', min: 2, max: 10000000, step: 1 },
  { key: 'model_failure_rate_threshold', label: '模型失败率阈值（0～1）', min: .01, max: 1, step: .01 },
] as const
const reviewRuleSwitches = [
  { key: 'related_accounts_enabled', label: '多账号关联复核' },
  { key: 'call_failures_enabled', label: '调用失败率突增复核' },
  { key: 'quota_spike_enabled', label: '额度消耗突增复核' },
  { key: 'model_failures_enabled', label: '模型集中失败复核' },
] as const
const workspaceTabs: { id: Tab; label: string; icon: typeof Activity }[] = [
  { id: 'ranking', label: '实时排行', icon: Activity },
  { id: 'matrix', label: '账号矩阵', icon: Rows3 },
  { id: 'ip', label: 'IP 监控', icon: Globe },
  { id: 'actions', label: '封禁列表', icon: ShieldBan },
  { id: 'audit', label: '审计日志', icon: History },
  { id: 'ai', label: 'AI 审核', icon: ShieldAlert },
]
const historyLabels: Record<HistoryTab, string> = { assessments: '账号评估', jobs: '扫描批次', actions: '处置记录', audit: '审计日志' }
const sourceLabels: Record<string, string> = { api_call: 'API 调用', login: '用户登录', registration: '用户注册', login_registration: '成功登录 / 注册' }
const actionLabels: Record<string, string> = { observe: '持续观察', review: '人工复核', ban: '封禁账号', unban: '解除封禁', soft_delete: '注销用户', hard_delete: '彻底删除用户', purge_deleted: '清理已注销账号' }
const operatorLabels: Record<string, string> = { admin: '管理员', 'risk-scan': '风控自动扫描' }
const stateLabels: Record<string, string> = {
  pending: '等待执行', running: '扫描中', complete: '已完成',
  prepared: '已准备', executing: '执行中', verified: '已验证', released: '已解除',
  noop: '状态未变更', failed: '失败', uncertain: '待核对', conflict: '状态冲突',
  confirmed: '已核实', false_positive: '误报', watching: '待观察',
  completed: '已完成', needs_review: '待人工核对',
}
const aiLabels: Record<string, string> = {
  disabled: '未启用', unconfigured: '未配置', not_requested: '未调用',
  complete: '已完成', assessed: '已完成', ok: '已完成', success: '已完成', unknown: '待确认',
  failed: '评估失败', error: '评估失败', unavailable: '暂不可用',
  invalid: '响应格式无效',
}
const eventLabels: Record<string, string> = {
  action_prepared: '处置准备', action_executing: '处置开始', action_verified: '处置验证',
  action_failed: '处置失败', action_uncertain: '处置待核对', action_released: '处置解除',
  action_reconciled: '状态核对', action_noop: '无需变更', action_note_updated: '更新用户备注',
  user_remark_updated: '更新用户备注', user_remark_failed: '用户备注更新失败', user_remark_pending: '用户备注更新准备',
  action_remark_synced: '用户备注已同步', action_remark_failed: '用户备注同步失败',
  manual_review_saved: '人工复核与误报反馈',
  user_lifecycle_pending: '用户删除操作准备',
  user_lifecycle_completed: '用户删除操作完成',
  user_lifecycle_needs_review: '用户删除操作待核对',
}
const findingLabels: Record<string, string> = {
  token_fanout_with_burst: '令牌多出口与调用突增',
  repeated_login_failures: '连续登录失败',
  login_failures_and_new_browser: '失败尝试与新浏览器登录',
  attempted_account_failures: '账号被尝试登录',
  claimed_account_login_failures: '未认证的账号登录尝试',
  shared_verified_browser_accounts: '同浏览器多账号认证',
  shared_token_accounts: '多账号共享调用令牌',
  call_failure_rate_spike: '调用失败率突增',
  quota_consumption_spike: '额度消耗突增',
  model_failure_concentration: '模型集中失败',
}

function count(value?: number | null) {
  return value == null ? '—' : new Intl.NumberFormat('zh-CN').format(value)
}
function time(value?: number) {
  return value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—'
}
function briefID(value?: string) {
  return value ? value.length > 18 ? `${value.slice(0, 10)}…${value.slice(-5)}` : value : '—'
}
function sourceLabel(value: string) {
  return sourceLabels[value] ?? value
}
function stateTone(state?: string) {
  if (state === 'verified' || state === 'released' || state === 'complete' || state === 'completed') return 'green'
  if (state === 'failed' || state === 'conflict') return 'red'
  if (state === 'running' || state === 'executing') return 'blue'
  if (state === 'uncertain' || state === 'pending' || state === 'prepared' || state === 'needs_review') return 'amber'
  return 'neutral'
}
function StateBadge({ state }: { state?: string }) {
  const tone = stateTone(state)
  return <Badge variant={tone === 'green' ? 'success' : tone === 'red' ? 'destructive' : tone === 'amber' ? 'warning' : 'secondary'}
    className={tone === 'blue' ? 'bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300' : undefined}>{state ? stateLabels[state] ?? state : '—'}</Badge>
}
function RemarkStatus({ sync }: { sync?: RemarkSync }) {
  if (!sync) return null
  return <Badge variant={sync.state === 'synced' ? 'success' : 'warning'} title={sync.error}>
    {sync.state === 'synced' ? '用户备注已同步' : sync.state === 'failed' ? '用户备注未同步' : '用户备注待同步'}
  </Badge>
}
function RiskBadge({ assessment }: { assessment: Assessment }) {
  const level = assessment.level
  return <Badge variant={level === 'high' ? 'destructive' : level === 'medium' ? 'warning' : 'secondary'}>
    {level === 'high' ? '重点关注' : level === 'medium' ? '需要复核' : level === 'low' ? '未命中高分规则' : '待确认'}
  </Badge>
}
function ReviewMetric({ label, value, icon: Icon, tone }: {
  label: string; value: string; icon: typeof Activity; tone: 'neutral' | 'green' | 'blue' | 'red' | 'amber'
}) {
  return <Card className={`rc-review-metric rc-review-metric--${tone}`}>
    <div className="rc-review-metric-icon"><Icon size={23} aria-hidden="true" /></div>
    <div className="min-w-0"><span>{label}</span><strong title={value}>{value}</strong></div>
  </Card>
}
function ReviewEmpty({ icon: Icon, title, description }: { icon: typeof Activity; title: string; description: string }) {
  return <div className="rc-review-empty"><Icon aria-hidden="true" /><strong>{title}</strong><p>{description}</p></div>
}
function dataNote(note: string) {
  if (note.includes('Historical call coverage')) return '调用历史覆盖未确认，自动处置已抑制。'
  if (note.includes('baseline') || note.includes('Baseline')) return '历史基线不足，本次结果仅供复核。'
  if (note.includes('unverified') || note.includes('Unverified')) return '尝试账号关联未经认证，仅作为复核线索。'
  return note
}
function parseIDs(raw: string) {
  const values = raw.trim() ? raw.split(/[\s,，]+/).filter(Boolean).map(Number) : []
  if (values.length > 10000 || values.some(id => !Number.isSafeInteger(id) || id <= 0)) {
    throw new Error('账号 ID 必须为正整数，最多填写 10000 个。')
  }
  return [...new Set(values)]
}
function recordID(record: RecordType, index: number) {
  return record.id || `unpersisted-${index}`
}
function isAborted(cause: unknown) {
  return cause instanceof DOMException && cause.name === 'AbortError'
}
function cloneConfig(config: RiskConfig): RiskConfig {
  return { ...config, whitelist: [...config.whitelist], review_rules: { ...defaultReviewRules, ...config.review_rules }, ai: { ...config.ai, api_key: '' } }
}
function previewConfigDraft(): RiskConfig {
  return {
    version: 0, enabled: false, auto_execute: false, dry_run: true,
    scan_interval_seconds: 300, window_seconds: 3600, min_requests: 100,
    max_requests_per_minute: 600, concurrent_token_ips: 3, min_burst_ratio: 3,
    login_failures_threshold: 10, whitelist: [],
    review_rules: { ...defaultReviewRules },
    ai: { enabled: false, base_url: '', model: '', timeout_seconds: 20, api_key: '' },
  }
}
function payloadConfig(config: RiskConfig, whitelist: string): RiskConfig {
  return {
    version: config.version, enabled: config.enabled, auto_execute: config.auto_execute, dry_run: config.dry_run,
    scan_interval_seconds: config.scan_interval_seconds, window_seconds: config.window_seconds,
    min_requests: config.min_requests, max_requests_per_minute: config.max_requests_per_minute,
    concurrent_token_ips: config.concurrent_token_ips, min_burst_ratio: config.min_burst_ratio,
    login_failures_threshold: config.login_failures_threshold, whitelist: parseIDs(whitelist),
    review_rules: { ...defaultReviewRules, ...config.review_rules },
    ai: {
      enabled: config.ai.enabled, base_url: config.ai.base_url.trim(), api_key: config.ai.api_key?.trim() || '',
      model: config.ai.model.trim(), timeout_seconds: config.ai.timeout_seconds,
    },
  }
}

function aiCredentialScope(raw: string) {
  const value = raw.trim()
  try {
    const parsed = new URL(value)
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.search || parsed.hash) return ''
    // Keep explicit ports and escaped proxy mount paths, matching the server's
    // conservative credential scope rather than broadening it to an origin.
    const parts = /^(https?):\/\/([^/?#]+)(\/[^?#]*)?$/.exec(value)
    if (!parts) return ''
    let path = (parts[3] || '').replace(/\/+$/, '')
    for (const suffix of ['/chat/completions', '/models']) {
      if (path.endsWith(suffix)) { path = path.slice(0, -suffix.length); break }
    }
    if (!path.endsWith('/v1')) path += '/v1'
    return `${parts[1]}://${parts[2].toLowerCase()}${path}`
  } catch {
    return ''
  }
}

function FeatureList({ title, icon, rows, exits }: {
  title: string; icon: ReactNode; rows: [string, ReactNode][]; exits?: string[]
}) {
  return <section className="min-w-0 space-y-3">
    <h3 className="flex items-center gap-2 text-sm font-semibold">{icon}{title}</h3>
    <dl className="rc-feature-list">{rows.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    <ScrollRegion label={`${title}已观测 IP`} size="sm" fixed className="rc-feature-exits">
      <div className="flex flex-wrap gap-1.5 text-xs text-muted-foreground">{exits?.length ? exits.map(ip => <code key={ip} className="max-w-full break-all rounded bg-muted px-2 py-1">{ip}</code>) : <span>暂无已观测出口</span>}</div>
    </ScrollRegion>
  </section>
}

function AssessmentView({ value, onBan, showAI = true, onViewUser, onReviewSaved, onUnauthorized }: { value: Assessment; onBan?: () => void; showAI?: boolean; onViewUser?: (id: number, username?: string) => void; onReviewSaved?: (value: ManualRiskReview) => void; onUnauthorized?: () => void }) {
  const f = value.features
  return <div className="space-y-6">
    <div className="flex flex-wrap items-center justify-between gap-4">
      <div><UserIdentityButton userId={value.user_id} username={value.username} compact onClick={() => onViewUser?.(value.user_id, value.username)} disabled={!onViewUser} /><h3 className="mt-3 flex items-baseline gap-2 text-sm">规则评分 <strong className="text-3xl font-bold">{value.score}</strong><small className="text-xs text-muted-foreground">/ 100</small></h3></div>
      <div className="flex flex-wrap items-center gap-2"><RiskBadge assessment={value} />{value.whitelisted && <Badge variant="success">排除处置</Badge>}
        {value.sync_state && <Badge variant={value.sync_state === 'completed' ? 'outline' : 'warning'}>{value.sync_state === 'completed' ? '调用同步完成' : '调用同步中'}</Badge>}
        <Badge variant="secondary">{actionLabels[value.proposed_action] ?? value.proposed_action}</Badge>
        {onBan && !value.whitelisted && <Button variant="destructive" size="sm" type="button" onClick={onBan}><ShieldBan className="mr-2 h-4 w-4" />人工处置</Button>}
      </div>
    </div>
    <p className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">{time(value.window_start)} — {time(value.window_end)}<span>规则版本 v{value.rule_version}</span></p>
    {!!value.history_coverage_start && <p className="text-xs leading-relaxed text-muted-foreground">完整性检查包含上一基线窗口：需覆盖 {time(value.history_coverage_start)} 至 {time(value.window_end)}。保留清理截断该范围时，仅提供人工复核。</p>}
    {!value.history_complete && <Notice kind="warning">调用历史尚未确认完整；当前分数不代表账号安全结论，自动处置已抑制。</Notice>}
    <div className="grid grid-cols-1 gap-6 md:grid-cols-3">
      <FeatureList title="API 调用" icon={<Activity size={16} />} exits={f.call_ips} rows={[
        ['调用次数', count(f.calls)], ['上一窗口', count(f.previous_calls)],
        ['成功 / 失败', `${count(f.successful_calls)} / ${count(f.failed_calls)}`],
        ['峰值 RPM', count(f.peak_rpm)], ['令牌每分钟最多出口', count(f.max_token_minute_ips)],
        ['达 RPM 阈值时令牌最多出口', count(f.max_token_ips_at_high_rpm)],
        ['请求增幅', f.baseline_available ? `${Number(f.burst_ratio || 0).toFixed(2)} 倍` : '基线不足'],
        ['失败率 / 上一窗口', `${((f.failure_rate ?? 0) * 100).toFixed(1)}% / ${((f.previous_failure_rate ?? 0) * 100).toFixed(1)}%`],
        ['额度消耗增幅', f.previous_quota > 0 ? `${Number(f.quota_burst_ratio ?? 0).toFixed(2)} 倍` : '基线不足'],
      ]} />
      <FeatureList title="用户登录" icon={<KeyRound size={16} />} exits={f.login_ips} rows={[
        ['成功登录', count(f.login_successes)], ['认证失败', count(f.login_failures)],
        ['未认证账号尝试', count(f.attempted_account_failures)], ['待二次验证', count(f.pending_logins)],
        ['未见过的浏览器', count(f.new_browser_logins)],
      ]} />
      <FeatureList title="用户注册" icon={<ShieldCheck size={16} />} exits={f.registration_ips} rows={[
        ['注册成功', count(f.registration_successes)], ['观测出口', count(f.registration_ips?.length ?? 0)],
        ['历史基线', f.baseline_available ? '已有调用基线' : '尚未建立'],
        ['自动处置资格', value.auto_eligible ? '规则条件已满足' : '不满足自动条件'],
      ]} />
    </div>
    <RiskRelatedEvidence evidence={value.related_evidence} truncated={value.related_evidence_truncated} onViewUser={onViewUser} />
    {!!f.model_failures?.length && <section className="space-y-3"><h4 className="text-sm font-semibold">模型调用质量与基线</h4><Table className="min-w-[560px]" containerClassName="scroll-region scroll-region--sm" containerLabel="模型失败率与上一窗口基线"><TableHeader><TableRow>{['模型', '当前调用 / 失败', '当前失败率', '上一窗口调用 / 失败', '上一窗口失败率'].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{f.model_failures.map(model => <TableRow key={model.model}><TableCell className="max-w-64 break-all font-medium">{model.model}</TableCell><TableCell>{count(model.calls)} / {count(model.failures)}</TableCell><TableCell>{(model.failure_rate * 100).toFixed(1)}%</TableCell><TableCell>{count(model.previous_calls)} / {count(model.previous_failures)}</TableCell><TableCell>{model.previous_calls ? `${(model.previous_failure_rate * 100).toFixed(1)}%` : '样本不足'}</TableCell></TableRow>)}</TableBody></Table>{value.model_failures_truncated && <p className="text-xs text-amber-600">按失败次数显示前 20 个模型，规则计算覆盖全部模型。</p>}</section>}
    <div className="space-y-3 border-t pt-4">
      <h4 className="flex items-center gap-2 text-sm font-semibold">命中证据 <Badge variant="secondary">{value.findings?.length ?? 0}</Badge></h4>
      {value.findings?.length ? <div className="divide-y">{value.findings.map(finding => <article key={finding.id} className="py-3 first:pt-0 last:pb-0">
        <div className="flex flex-wrap items-center gap-2"><strong className="text-sm font-medium">{findingLabels[finding.id] ?? finding.id}</strong><Badge variant="secondary">{sourceLabel(finding.source)}</Badge><span className="ml-auto font-mono text-xs text-amber-600">+{finding.score}</span></div>
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">{finding.description}</p>
      </article>)}</div> : <p className="text-sm text-muted-foreground">当前窗口没有命中已配置规则。缺失的事件来源仍需单独确认。</p>}
    </div>
    {showAI && <div className="space-y-3 rounded-lg bg-muted/30 p-4">
      <div className="flex items-center gap-2"><h4 className="text-sm font-semibold">AI 辅助评估</h4><Badge variant="secondary">{aiLabels[value.ai?.status] ?? value.ai?.status ?? '未调用'}</Badge></div>
      {value.ai?.reason && <p className="text-sm leading-relaxed text-muted-foreground">{value.ai.reason}</p>}
      {value.ai?.risk_level && <dl className="rc-detail-grid"><div><dt>模型判断</dt><dd>{value.ai.risk_level}</dd></div><div><dt>模型置信度</dt><dd>{value.ai.confidence == null ? '—' : `${Math.round(value.ai.confidence * 100)}%`}</dd></div><div><dt>模型意见</dt><dd>{actionLabels[value.ai.recommendation ?? ''] ?? value.ai.recommendation ?? '—'}</dd></div></dl>}
    </div>}
    <RiskReviewPanel userId={value.user_id} assessmentId={value.id} review={value.review} latestReview={value.latest_review} onSaved={onReviewSaved} onUnauthorized={onUnauthorized} />
    {!!value.data_notes?.length && <ul className="rc-data-notes">{[...new Set(value.data_notes.map(dataNote))].map(note => <li key={note}>{note}</li>)}</ul>}
    {value.action_error && <Notice kind="error">{value.action_error}</Notice>}
  </div>
}

function Snapshot({ value, title }: { value?: UserSnapshot; title: string }) {
  return <div className="space-y-3 rounded-lg border bg-muted/20 p-4"><h4 className="text-sm font-semibold">{title}</h4>{value ? <dl className="rc-detail-grid">
    <div><dt>账号</dt><dd>{value.id ? `#${value.id}` : '—'}</dd></div>
    <div><dt>状态</dt><dd>{value.status === 1 ? '启用' : value.status === 2 ? '禁用' : value.status || '未记录'}</dd></div>
    <div><dt>角色</dt><dd>{value.role >= 100 ? '超级管理员' : value.role >= 10 ? '管理员' : '普通用户'}</dd></div>
    <div><dt>认证版本</dt><dd>{value.auth_version_known ? value.auth_version : '来源未提供'}</dd></div>
  </dl> : <p className="rc-muted">未记录</p>}</div>
}

export default function RiskControl({ tokenVersion = 0, quotaPerUnit = 500000, onUnauthorized }: {
  tokenVersion?: number; quotaPerUnit?: number; onUnauthorized: () => void
}) {
  const [tab, setTab] = useState<Tab>(() => {
    const params = new URLSearchParams(window.location.search)
    const view = params.get('view')
    if (workspaceTabs.some(item => item.id === view)) return view as Tab
    // Existing links that target an account still open its analysis workspace.
    return /^[1-9][0-9]*$/.test(params.get('user_id') ?? '') ? 'ai' : 'ranking'
  })
  const [reviewTab, setReviewTab] = useState<ReviewTab>('assessments')
  const historyKind: HistoryTab = tab === 'ai' ? reviewTab : tab === 'audit' ? 'audit' : 'actions'
  const [banView, setBanView] = useState<'current' | 'history'>('current')
  const [banPage, setBanPage] = useState(1)
  const [bans, setBans] = useState<(HistoryPage<BannedUser> & { history_available: boolean; history_warning?: string }) | null>(null)
  const [bansLoading, setBansLoading] = useState(!UI_PREVIEW_ONLY)
  const [bansError, setBansError] = useState('')
  const [banSearchDraft, setBanSearchDraft] = useState('')
  const [banSearch, setBanSearch] = useState('')
  const [selectedUser, setSelectedUser] = useState<{ id: number; username?: string } | null>(null)
  const [page, setPage] = useState(1)
  const [history, setHistory] = useState<HistoryPage<RecordType> | null>(null)
  const [historyLoading, setHistoryLoading] = useState(!UI_PREVIEW_ONLY)
  const [historyError, setHistoryError] = useState('')
  const [candidatePage, setCandidatePage] = useState(1)
  const [candidates, setCandidates] = useState<CandidatePage | null>(null)
  const [candidateLoading, setCandidateLoading] = useState(!UI_PREVIEW_ONLY)
  const [candidateError, setCandidateError] = useState('')
  const [matrix, setMatrix] = useState<AccountMatrixData | null>(null)
  const [matrixLoading, setMatrixLoading] = useState(!UI_PREVIEW_ONLY)
  const [matrixError, setMatrixError] = useState('')
  const [config, setConfig] = useState<RiskConfig | null>(null)
  const [draft, setDraft] = useState<RiskConfig | null>(() => UI_PREVIEW_ONLY ? previewConfigDraft() : null)
  const [whitelistDraft, setWhitelistDraft] = useState('')
  const [configLoading, setConfigLoading] = useState(!UI_PREVIEW_ONLY)
  const [dirty, setDirty] = useState(false)
  const dirtyRef = useRef(false)
  const [revision, setRevision] = useState(0)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [warning, setWarning] = useState('')
  const [busy, setBusy] = useState('')
  const operation = useRef<AbortController | null>(null)
  const mounted = useRef(true)
  const [userID, setUserID] = useState(() => {
    const initial = new URLSearchParams(window.location.search).get('user_id') ?? ''
    return /^[1-9][0-9]*$/.test(initial) && Number.isSafeInteger(Number(initial)) ? initial : ''
  })
  const [assessment, setAssessment] = useState<Assessment | null>(null)
  const [assessmentUsesAI, setAssessmentUsesAI] = useState(false)
  const [detail, setDetail] = useState<RecordDetail | null>(null)
  const [actionDialog, setActionDialog] = useState<ActionDialog | null>(null)
  const [actionUserID, setActionUserID] = useState('')
  const [reason, setReason] = useState('')
  const [note, setNote] = useState('')
  const [noteDialog, setNoteDialog] = useState<NoteTarget | null>(null)
  const [noteSnapshot, setNoteSnapshot] = useState<UserRemark | null>(null)
  const [noteLoading, setNoteLoading] = useState(false)
  const [noteRevision, setNoteRevision] = useState(0)
  const [dialogError, setDialogError] = useState('')
  const [scanOpen, setScanOpen] = useState(false)
  const [scanIDs, setScanIDs] = useState('')
  const [scanScope, setScanScope] = useState<'active' | 'selected'>('active')
  const [scanDryRun, setScanDryRun] = useState(true)
  const [trackedJob, setTrackedJob] = useState<ScanJob | null>(null)
  const [tracking, setTracking] = useState(false)
  const [confirmConfig, setConfirmConfig] = useState<RiskConfig | null>(null)
  const [autoConfirmed, setAutoConfirmed] = useState(false)
  const [aiTest, setAITest] = useState<AIResult | null>(null)
  const [aiBusy, setAIBusy] = useState<'models' | 'test' | ''>('')
  const [aiError, setAIError] = useState('')
  const [models, setModels] = useState<AIModels | null>(null)
  const [modelsError, setModelsError] = useState('')
  const [modelSearch, setModelSearch] = useState('')
  const aiOperation = useRef<AbortController | null>(null)
  const aiOperationKind = useRef<'models' | 'test' | ''>('')
  const [configExpanded, setConfigExpanded] = useState(false)
  const [logicOpen, setLogicOpen] = useState(false)
  const [focusWhitelist, setFocusWhitelist] = useState(false)
  const whitelistField = useRef<HTMLTextAreaElement>(null)
  const trackedSection = useRef<HTMLDivElement>(null)
  const [showApiKey, setShowApiKey] = useState(false)
  const [latestJob, setLatestJob] = useState<ScanJob | null>(null)

  const report = useCallback((cause: unknown) => {
    if (isAborted(cause)) return ''
    if (cause instanceof ApiError && cause.status === 401) {
      onUnauthorized()
      return '管理员会话已失效，请重新连接。'
    }
    return cause instanceof Error ? cause.message : '操作暂时不可用，请稍后重试。'
  }, [onUnauthorized])

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      operation.current?.abort()
      aiOperation.current?.abort()
    }
  }, [])

  useEffect(() => {
    operation.current?.abort()
    operation.current = null
    setBusy('')
    setAssessment(null)
    setAssessmentUsesAI(false)
    setDetail(null)
    setActionDialog(null)
    setNoteDialog(null)
    setNoteSnapshot(null)
    setNoteLoading(false)
    setWarning('')
    setSelectedUser(null)
    setBans(null)
    setNote('')
    setScanOpen(false)
    setLogicOpen(false)
    setFocusWhitelist(false)
    setShowApiKey(false)
    setConfirmConfig(null)
    setTracking(false)
    setTrackedJob(null)
    setLatestJob(null)
    setAITest(null)
    aiOperation.current?.abort()
    aiOperation.current = null
    aiOperationKind.current = ''
    setAIBusy('')
    setAIError('')
    setModels(null)
    setModelsError('')
    setModelSearch('')
    setDraft(UI_PREVIEW_ONLY ? previewConfigDraft() : null)
    setWhitelistDraft('')
    setConfig(null)
    setHistory(null)
    setCandidates(null)
    setCandidatePage(1)
    setDirty(false)
    dirtyRef.current = false
  }, [tokenVersion])

  useEffect(() => {
    aiOperation.current?.abort()
    aiOperation.current = null
    aiOperationKind.current = ''
    setAIBusy('')
    setModels(null)
    setModelsError('')
    setModelSearch('')
    setAITest(null)
    setAIError('')
  }, [draft?.ai.base_url, draft?.ai.api_key, draft?.ai.timeout_seconds])

  useEffect(() => {
    if (!noteDialog || UI_PREVIEW_ONLY) return
    const controller = new AbortController()
    setNoteLoading(true)
    setNoteSnapshot(null)
    setDialogError('')
    void requestJSON<UserRemark>(`/api/users/${noteDialog.user_id}/remark`, {}, controller.signal).then(value => {
      if (controller.signal.aborted) return
      if (value.user_id !== noteDialog.user_id || typeof value.remark !== 'string') {
        throw new Error('用户备注返回不完整，请重新读取。')
      }
      setNoteSnapshot(value)
      setNote(value.remark)
      if (!value.remark_supported) setDialogError('当前 NewAPI 用户表没有备注字段，暂不支持保存。')
    }).catch(cause => {
      if (!controller.signal.aborted) setDialogError(report(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setNoteLoading(false)
    })
    return () => controller.abort()
  }, [noteDialog, noteRevision, tokenVersion, report])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) {
      setConfigLoading(false)
      setError('')
      return
    }
    const controller = new AbortController()
    setConfigLoading(true)
    setError('')
    void requestJSON<RiskConfig>('/api/risk/config', {}, controller.signal).then(loaded => {
      if (controller.signal.aborted) return
      setConfig(loaded)
      if (!dirtyRef.current) {
        setDraft(cloneConfig(loaded))
        setWhitelistDraft(loaded.whitelist.join(', '))
      }
    }).catch(cause => {
      if (!controller.signal.aborted) setError(report(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setConfigLoading(false)
    })
    return () => controller.abort()
  }, [tokenVersion, revision, report])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || tab !== 'ai') return
    const controller = new AbortController()
    void requestJSON<HistoryPage<ScanJob>>('/api/risk/jobs?limit=1&offset=0', {}, controller.signal)
      .then(data => { if (!controller.signal.aborted) setLatestJob(data.items[0] ?? null) })
      .catch(cause => {
        if (controller.signal.aborted) return
        setLatestJob(null)
        if (cause instanceof ApiError && cause.status === 401) report(cause)
      })
    return () => controller.abort()
  }, [tab, tokenVersion, revision, report])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || tab === 'ranking' || tab === 'matrix' || tab === 'ip' ||
      tab === 'actions' && banView === 'current') {
      setHistoryLoading(false)
      setHistoryError('')
      setHistory(null)
      return
    }
    const controller = new AbortController()
    setHistoryLoading(true)
    setHistoryError('')
    setHistory(null)
    void requestJSON<HistoryPage<RecordType>>(`/api/risk/${historyKind}?limit=${PAGE_SIZE}&offset=${(page - 1) * PAGE_SIZE}`, {}, controller.signal)
      .then(data => {
        if (controller.signal.aborted) return
        if (page > 1 && !data.items.length) {
          setPage(Math.min(page - 1, Math.max(1, Math.ceil(data.total / PAGE_SIZE))))
          return
        }
        setHistory(data)
      }).catch(cause => {
        if (!controller.signal.aborted) setHistoryError(report(cause))
      }).finally(() => {
        if (!controller.signal.aborted) setHistoryLoading(false)
      })
    return () => controller.abort()
  }, [tab, banView, historyKind, page, tokenVersion, revision, report])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || tab !== 'actions' || banView !== 'current') {
      setBansLoading(false)
      return
    }
    const controller = new AbortController()
    setBans(null)
    setBansLoading(true)
    setBansError('')
    const params = new URLSearchParams({ search: banSearch, limit: String(PAGE_SIZE), offset: String((banPage - 1) * PAGE_SIZE) })
    void requestJSON<HistoryPage<BannedUser> & { history_available: boolean; history_warning?: string }>(`/api/risk/banned-users?${params}`, {}, controller.signal)
      .then(data => {
        if (controller.signal.aborted) return
        if (banPage > 1 && !data.items.length) { setBanPage(Math.max(1, Math.ceil(data.total / PAGE_SIZE))); return }
        setBans(data)
      }).catch(cause => { if (!controller.signal.aborted) setBansError(report(cause)) })
      .finally(() => { if (!controller.signal.aborted) setBansLoading(false) })
    return () => controller.abort()
  }, [tab, banView, banSearch, banPage, tokenVersion, revision, report])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || tab !== 'matrix') {
      if (tab !== 'matrix') { setMatrixLoading(false); setMatrixError('') }
      return
    }
    const controller = new AbortController()
    setMatrixLoading(true)
    setMatrixError('')
    void requestJSON<AccountMatrixData>('/api/risk/account-matrix', {}, controller.signal)
      .then(data => { if (!controller.signal.aborted) setMatrix(data) })
      .catch(cause => {
        if (controller.signal.aborted) return
        setMatrix(null)
        setMatrixError(report(cause))
      })
      .finally(() => { if (!controller.signal.aborted) setMatrixLoading(false) })
    return () => controller.abort()
  }, [tab, tokenVersion, revision, report])

  useEffect(() => {
    if (UI_PREVIEW_ONLY || tab !== 'ai') return
    const controller = new AbortController()
    setCandidateLoading(true)
    setCandidateError('')
    void requestJSON<CandidatePage>(`/api/risk/candidates?limit=${PAGE_SIZE}&offset=${(candidatePage - 1) * PAGE_SIZE}`, {}, controller.signal)
      .then(data => {
        if (controller.signal.aborted) return
        if (candidatePage > 1 && !data.items.length) {
          setCandidatePage(Math.min(candidatePage - 1, Math.max(1, Math.ceil(data.total / PAGE_SIZE))))
          return
        }
        setCandidates(data)
      }).catch(cause => {
        if (!controller.signal.aborted) {
          setCandidates(null)
          setCandidateError(report(cause))
        }
      }).finally(() => {
        if (!controller.signal.aborted) setCandidateLoading(false)
      })
    return () => controller.abort()
  }, [tab, candidatePage, tokenVersion, revision, report])

  useEffect(() => {
    if (!focusWhitelist || !configExpanded || !whitelistField.current) return
    whitelistField.current.focus({ preventScroll: true })
    whitelistField.current.scrollIntoView({ block: 'center' })
    setFocusWhitelist(false)
  }, [focusWhitelist, configExpanded])

  useEffect(() => {
    if (UI_PREVIEW_ONLY) return
    if (!tracking || !trackedJob?.id) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const id = trackedJob.id
    const poll = async () => {
      try {
        const data = await requestJSON<HistoryPage<ScanJob>>('/api/risk/jobs?limit=1000&offset=0', {}, controller.signal)
        if (controller.signal.aborted) return
        const current = data.items.find(job => job.id === id)
        if (!current) {
          setTracking(false)
          setError('任务不在最近 1000 条记录中，请在扫描任务中分页查找；未判断任务是否完成。')
          return
        }
        setTrackedJob(current)
        if (current.state === 'complete' || current.state === 'failed') {
          setTracking(false)
          setRevision(value => value + 1)
          setMessage(current.state === 'complete' ? '扫描任务已完成，记录已更新。' : '扫描任务失败，请查看任务详情。')
        } else {
          timer = setTimeout(poll, 3000)
        }
      } catch (cause) {
        if (!controller.signal.aborted) {
          setTracking(false)
          setError(report(cause))
        }
      }
    }
    timer = setTimeout(poll, 1200)
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [tracking, trackedJob?.id, tokenVersion, report])

  async function run(name: string, work: (signal: AbortSignal) => Promise<void>, dialog = false) {
    if (UI_PREVIEW_ONLY) return
    if (operation.current || aiOperation.current) return
    const controller = new AbortController()
    operation.current = controller
    setBusy(name)
    setError('')
    setMessage('')
    setWarning('')
    setDialogError('')
    try {
      await work(controller.signal)
    } catch (cause) {
      if (mounted.current && !controller.signal.aborted) {
        const text = report(cause)
        if (dialog) setDialogError(text)
        else setError(text)
      }
    } finally {
      if (operation.current === controller) operation.current = null
      if (mounted.current && !controller.signal.aborted) setBusy('')
    }
  }

  function chooseTab(next: Tab) {
    setTab(next)
    setPage(1)
    setHistory(null)
    setHistoryError('')
  }

  function chooseReview(next: ReviewTab) {
    setTab('ai')
    setReviewTab(next)
    setPage(1)
    setHistory(null)
  }

  function viewUser(id: number, username?: string) {
    setDetail(null)
    setSelectedUser({ id, username })
  }

  function reviewSaved(review: ManualRiskReview) {
    setAssessment(current => current?.id === review.assessment_id ? { ...current, review, latest_review: review } : current)
    setDetail(current => current?.kind === 'assessments' && current.record.id === review.assessment_id
      ? { ...current, record: { ...(current.record as Assessment), review, latest_review: review } } : current)
    setMessage(`复核已保存：${reviewLabels[review.status]}。`)
    setRevision(value => value + 1)
  }

  function userLink(id?: number, username?: string) {
    return id ? <UserIdentityButton userId={id} username={username} onClick={() => viewUser(id, username)} compact />
      : <span className="text-muted-foreground">—</span>
  }

  function auditUsers(item: AuditRecord, limit = 3) {
    const ids = item.user_ids?.length ? item.user_ids : item.user_id ? [item.user_id] : []
    if (!ids.length) return <span className="text-muted-foreground">—</span>
    return <div className="flex flex-wrap items-center gap-1.5">{ids.slice(0, limit).map((id, index) => <UserIdentityButton key={id} userId={id} username={item.usernames?.[index] || (id === item.user_id ? item.username ?? item.before?.username ?? item.after?.username : undefined)} compact onClick={() => viewUser(id)} />)}{ids.length > limit && <Badge variant="outline">另 {count(ids.length - limit)} 个账号</Badge>}</div>
  }

  function openNote(userID: number, actionID?: string) {
    setDetail(null)
    setActionDialog(null)
    setNoteDialog({ user_id: userID, action_id: actionID })
    setNoteSnapshot(null)
    setNote('')
    setNoteLoading(true)
    setDialogError('')
  }

  async function saveNote(event: FormEvent) {
    event.preventDefault()
    if (!noteDialog || UI_PREVIEW_ONLY || noteLoading || !noteSnapshot?.remark_supported) return
    if ([...note].length > NOTE_LIMIT) { setDialogError(`NewAPI 用户备注最多 ${NOTE_LIMIT} 字。`); return }
    const record = noteDialog
    await run('note', async signal => {
      await requestJSON<UserRemark & { action?: RiskAction }>(`/api/users/${record.user_id}/remark`, {
        method: 'PUT', body: JSON.stringify({ remark: note, expected_remark: noteSnapshot.remark, action_id: record.action_id }),
      }, signal)
      if (signal.aborted) return
      setNoteDialog(null)
      setMessage('已写入 NewAPI 管理员后台的用户备注，修改记录已保存。')
      setRevision(value => value + 1)
    }, true)
  }

  function editConfig(next: RiskConfig) {
    const connectionChanged = draft?.ai.base_url !== next.ai.base_url ||
      draft?.ai.api_key !== next.ai.api_key || draft?.ai.timeout_seconds !== next.ai.timeout_seconds
    if (connectionChanged || aiOperationKind.current === 'test') {
      aiOperation.current?.abort()
      aiOperation.current = null
      aiOperationKind.current = ''
      setAIBusy('')
    }
    if (connectionChanged) {
      setModels(null)
      setModelsError('')
      setModelSearch('')
    }
    setDraft(next)
    setDirty(true)
    dirtyRef.current = true
    setAITest(null)
    setAIError('')
  }

  async function analyze(useAI: boolean, selectedUser?: number) {
    if (UI_PREVIEW_ONLY) return
    let id: number
    try {
      const ids = parseIDs(selectedUser == null ? userID : String(selectedUser))
      if (ids.length !== 1) throw new Error('填写一个账号 ID 后再执行评估。')
      id = ids[0]
      if (selectedUser != null) setUserID(String(selectedUser))
    } catch (cause) {
      setError(report(cause))
      return
    }
    await run(useAI ? 'assess' : 'analysis', async signal => {
      const value = useAI
        ? await requestJSON<Assessment>('/api/risk/assess', { method: 'POST', body: JSON.stringify({ user_id: id }) }, signal)
        : await requestJSON<Assessment>(`/api/risk/users/${id}/analysis`, {}, signal)
      if (signal.aborted) return
      setAssessment(value)
      setAssessmentUsesAI(useAI)
      if (useAI) setRevision(value => value + 1)
    })
  }

  function openAction(kind: ActionDialog['kind'], record?: RiskAction, selectedUser?: number) {
    setDetail(null)
    setNoteDialog(null)
    setActionDialog({ kind, record, key: actionKey() })
    setActionUserID(String(selectedUser ?? record?.user_id ?? userID))
    setReason('')
    setNote('')
    setDialogError('')
  }

  async function submitAction(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY) return
    if (!actionDialog) return
    const current = actionDialog
    const trimmedReason = reason.trim()
    if (!trimmedReason) {
      setDialogError('请填写处置原因。')
      return
    }
    if (current.kind === 'ban' && [...note.trim()].length > NOTE_LIMIT) {
      setDialogError(`追加内容最多 ${NOTE_LIMIT} 字，连同原备注与标记合计不得超过 ${NOTE_LIMIT} 字。`)
      return
    }
    let id = current.record?.user_id
    if (current.kind === 'ban') {
      try {
        const ids = parseIDs(actionUserID)
        if (ids.length !== 1) throw new Error('请填写一个有效的账号 ID。')
        id = ids[0]
      } catch (cause) {
        setDialogError(report(cause))
        return
      }
    }
    await run('action', async signal => {
      const path = current.kind === 'ban' ? '/api/risk/actions' : `/api/risk/actions/${encodeURIComponent(current.record!.id)}/${current.kind}`
      const payload = current.kind === 'ban' ? { user_id: id, action: 'ban', reason: trimmedReason, note: note.trim() } : { reason: trimmedReason }
      const result = await requestJSON<{ action: RiskAction; resolution?: string; warning?: string }>(path, {
        method: 'POST', headers: { 'Idempotency-Key': current.key }, body: JSON.stringify(payload),
      }, signal)
      if (signal.aborted) return
      setActionDialog(null)
      if (result.warning || result.action.remark_sync && result.action.remark_sync.state !== 'synced') {
        setWarning(result.warning || `账号处置已完成，但用户备注未确认同步：${result.action.remark_sync?.error || '请在封禁列表检查用户备注。'}`)
      } else setMessage(current.kind === 'reconcile'
        ? `状态核对已记录：${stateLabels[result.action.state] ?? result.action.state}。`
        : `${current.kind === 'ban' ? '封禁处置' : '解除处置'}已确认：${stateLabels[result.action.state] ?? result.action.state}。`)
      setRevision(value => value + 1)
    }, true)
  }

  async function submitScan(event: FormEvent) {
    event.preventDefault()
    if (UI_PREVIEW_ONLY) return
    let ids: number[]
    try {
      ids = scanScope === 'active' ? [] : parseIDs(scanIDs)
      if (scanScope === 'selected' && ids.length === 0) throw new Error('填写至少一个账号 ID 后开始扫描。')
    } catch (cause) {
      setDialogError(report(cause))
      return
    }
    await run('scan', async signal => {
      const job = await requestJSON<ScanJob>('/api/risk/scan', {
        method: 'POST', body: JSON.stringify({ user_ids: ids, dry_run: scanDryRun }),
      }, signal)
      if (signal.aborted) return
      setTrackedJob(job)
      setLatestJob(job)
      setTracking(true)
      setScanOpen(false)
      chooseReview('jobs')
      setRevision(value => value + 1)
      setMessage('扫描任务已创建，正在等待后台执行。')
    }, true)
  }

  async function saveConfig(next: RiskConfig) {
    if (UI_PREVIEW_ONLY) return
    await run('save-config', async signal => {
      const saved = await requestJSON<RiskConfig>('/api/risk/config', { method: 'PUT', body: JSON.stringify(next) }, signal)
      if (signal.aborted) return
      setConfig(saved)
      setDraft(cloneConfig(saved))
      setWhitelistDraft(saved.whitelist.join(', '))
      setDirty(false)
      dirtyRef.current = false
      setConfirmConfig(null)
      setMessage(`规则配置已保存，当前版本 v${saved.version}。`)
    }, !!confirmConfig)
  }

  function submitConfig(event: FormEvent) {
    event.preventDefault()
    if (!draft || aiOperation.current) return
    try {
      const next = payloadConfig(draft, whitelistDraft)
      if (UI_PREVIEW_ONLY || next.auto_execute && !next.dry_run && !(config?.auto_execute && !config.dry_run)) {
        setConfirmConfig(next)
        setAutoConfirmed(false)
        setDialogError('')
        return
      }
      if (UI_PREVIEW_ONLY) return
      void saveConfig(next)
    } catch (cause) {
      setError(report(cause))
    }
  }

  function aiDraftPayload(needModel: boolean) {
    if (!draft || !aiCredentialScope(draft.ai.base_url)) throw new Error('请填写有效的 HTTP(S) API 地址，不含账号密码、查询参数或片段。')
    if (!draft.ai.api_key?.trim() && !canUseSavedKey) {
      throw new Error(config?.ai.api_key_configured ? 'API 地址已变化，请填写对应的新密钥。' : '请先填写 API 密钥。')
    }
    if (!Number.isInteger(draft.ai.timeout_seconds) || draft.ai.timeout_seconds < 1 || draft.ai.timeout_seconds > 60) {
      throw new Error('请求超时须为 1–60 秒。')
    }
    if (needModel && !draft.ai.model.trim()) throw new Error('请先选择或填写需要验证的模型。')
    return {
      base_url: draft.ai.base_url.trim(), api_key: draft.ai.api_key?.trim() || '',
      timeout_seconds: draft.ai.timeout_seconds,
      ...(needModel ? { model: draft.ai.model.trim() } : {}),
    }
  }

  async function loadModels() {
    if (UI_PREVIEW_ONLY || operation.current || aiOperation.current) return
    let payload: ReturnType<typeof aiDraftPayload>
    try { payload = aiDraftPayload(false) } catch (cause) { setModelsError(report(cause)); return }
    const controller = new AbortController()
    aiOperation.current = controller
    aiOperationKind.current = 'models'
    setAIBusy('models')
    setModels(null)
    setModelsError('')
    setModelSearch('')
    try {
      const result = await requestJSON<AIModels>('/api/risk/ai/models', {
        method: 'POST', body: JSON.stringify(payload),
      }, controller.signal)
      if (controller.signal.aborted || aiOperation.current !== controller || !mounted.current) return
      if (!Array.isArray(result.models) || result.models.length > 5000 ||
        result.models.some(model => typeof model.id !== 'string' || !model.id.trim()) ||
        result.total !== result.models.length || typeof result.endpoint !== 'string') {
        throw new Error('模型列表返回不完整，请重新拉取或手动填写模型。')
      }
      setModels(result)
    } catch (cause) {
      if (!controller.signal.aborted && aiOperation.current === controller && mounted.current) setModelsError(report(cause))
    } finally {
      if (aiOperation.current === controller) {
        aiOperation.current = null
        aiOperationKind.current = ''
        if (mounted.current) setAIBusy('')
      }
    }
  }

  async function testAI() {
    if (UI_PREVIEW_ONLY || operation.current || aiOperation.current) return
    let payload: ReturnType<typeof aiDraftPayload>
    try { payload = aiDraftPayload(true) } catch (cause) { setAIError(report(cause)); return }
    const controller = new AbortController()
    aiOperation.current = controller
    aiOperationKind.current = 'test'
    setAIBusy('test')
    setAIError('')
    setAITest(null)
    try {
      const result = await requestJSON<AIResult>('/api/risk/ai/test', {
        method: 'POST', body: JSON.stringify(payload),
      }, controller.signal)
      if (controller.signal.aborted || aiOperation.current !== controller || !mounted.current) return
      if (result.verified !== true || result.status !== 'assessed') throw new Error('连接尚未通过结构化响应验证。')
      setAITest(result)
    } catch (cause) {
      if (!controller.signal.aborted && aiOperation.current === controller && mounted.current) setAIError(report(cause))
    } finally {
      if (aiOperation.current === controller) {
        aiOperation.current = null
        aiOperationKind.current = ''
        if (mounted.current) setAIBusy('')
      }
    }
  }

  const activeHistoryLabel = historyLabels[historyKind]
  const uiBusy = !!busy
  const mutationBlocked = uiBusy || !!aiBusy || UI_PREVIEW_ONLY
  const canUseSavedKey = !!config?.ai.api_key_configured && !!draft &&
    !!aiCredentialScope(draft.ai.base_url) && aiCredentialScope(draft.ai.base_url) === aiCredentialScope(config.ai.base_url)
  const hasAIConnection = !!draft?.ai.base_url.trim() && (!!draft.ai.api_key?.trim() || canUseSavedKey)
  const modelNeedle = modelSearch.trim().toLocaleLowerCase()
  const filteredModels = models?.models.filter(model => !modelNeedle ||
    model.id.toLocaleLowerCase().includes(modelNeedle) || model.owned_by?.toLocaleLowerCase().includes(modelNeedle)) ?? []
  const canRelease = (record: RiskAction) => record.action === 'ban' && record.state === 'verified' && record.owns_transition && !record.released_by
  const canReconcile = (record: RiskAction) => ['prepared', 'executing', 'failed', 'uncertain', 'conflict'].includes(record.state)
  const openScan = () => {
    setDialogError('')
    setScanIDs('')
    setScanScope('active')
    setScanDryRun(true)
    setScanOpen(true)
  }

  function renderHistoryRow(record: RecordType, index: number) {
    if (historyKind === 'assessments') {
      const item = record as Assessment
      return <TableRow key={recordID(item, index)}>
        <TableCell>{userLink(item.user_id, item.username)}<small>{briefID(item.id)}</small></TableCell>
        <TableCell><span className="mr-2 font-mono text-base font-semibold">{item.score}<span className="text-xs font-normal text-muted-foreground">/100</span></span><RiskBadge assessment={item} /></TableCell>
        <TableCell><span>{count(item.findings?.length ?? 0)} 项规则命中</span>{item.review && <Badge variant={item.review.status === 'false_positive' ? 'success' : 'secondary'} className="ml-2">{reviewLabels[item.review.status]}</Badge>}<small>{actionLabels[item.proposed_action] ?? item.proposed_action}</small></TableCell>
        <TableCell><Badge variant={item.dry_run ? 'secondary' : 'outline'}>{item.dry_run ? '试运行' : '正式评估'}</Badge><small>AI · {aiLabels[item.ai?.status] ?? item.ai?.status ?? '未调用'}</small></TableCell>
        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{time(item.created_at)}</TableCell>
        <TableCell><Button variant="outline" size="sm" type="button" onClick={() => setDetail({ kind: 'assessments', record: item })}><Eye className="mr-2 h-3.5 w-3.5" />查看证据</Button></TableCell>
      </TableRow>
    }
    if (historyKind === 'jobs') {
      const item = record as ScanJob
      return <TableRow key={item.id}>
        <TableCell><code className="text-xs" title={item.id}>{briefID(item.id)}</code><small>{item.config?.dry_run ? '试运行' : '正式扫描'}</small></TableCell>
        <TableCell><StateBadge state={item.state} /></TableCell>
        <TableCell><span>{count(item.scanned)} / {count(item.user_ids?.length ?? 0)}</span><small>已评估 {count(item.assessed)}</small></TableCell>
        <TableCell><span>{count(item.banned)} 个账号</span><small>{item.errors?.length ? `${item.errors.length} 项待检查` : '无已记录错误'}</small></TableCell>
        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{time(item.created_at)}</TableCell>
        <TableCell><div className="flex items-center gap-2"><Button variant="outline" size="sm" type="button" onClick={() => setDetail({ kind: 'jobs', record: item })}><Eye className="mr-2 h-3.5 w-3.5" />详情</Button>
          {['pending', 'running'].includes(item.state) && <Button variant="outline" size="sm" type="button" disabled={UI_PREVIEW_ONLY} onClick={() => { setTrackedJob(item); setTracking(true) }}><Activity className="mr-2 h-3.5 w-3.5" />跟踪</Button>}</div></TableCell>
      </TableRow>
    }
    if (historyKind === 'actions') {
      const item = record as RiskAction
      return <TableRow key={item.id}>
        <TableCell>{userLink(item.user_id, item.username ?? item.before?.username)}<small>{actionLabels[item.action] ?? item.action}</small></TableCell>
        <TableCell><StateBadge state={item.state} />{item.released_by && <small>已有解除记录</small>}</TableCell>
        <TableCell><span className="rc-reason-preview" title={item.reason}>{item.reason}</span><small className="rc-reason-preview" title={item.note}>{item.note ? `当时补充说明：${item.note}` : '未填写补充说明'}</small><RemarkStatus sync={item.remark_sync} /><small>{item.operator}</small></TableCell>
        <TableCell><span className="flex items-center gap-2 whitespace-nowrap text-xs">{item.before?.status === 1 ? '启用' : item.before?.status === 2 ? '禁用' : '未记录'}<ArrowRight className="h-3 w-3 text-muted-foreground" />{item.after?.status === 1 ? '启用' : item.after?.status === 2 ? '禁用' : '待确认'}</span></TableCell>
        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{time(item.updated_at)}</TableCell>
        <TableCell><div className="flex items-center gap-2"><Button variant="outline" size="sm" type="button" onClick={() => setDetail({ kind: 'actions', record: item })}><Eye className="mr-2 h-3.5 w-3.5" />详情</Button>
          {item.action === 'ban' && <Button variant="ghost" size="sm" type="button" disabled={uiBusy} onClick={() => openNote(item.user_id, item.id)}><PencilLine className="mr-1 h-3.5 w-3.5" />用户备注</Button>}
          {canRelease(item) && <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openAction('release', item)}><RotateCcw className="mr-2 h-3.5 w-3.5" />解除</Button>}
          {canReconcile(item) && <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openAction('reconcile', item)}><Search className="mr-2 h-3.5 w-3.5" />核对</Button>}</div></TableCell>
      </TableRow>
    }
    const item = record as AuditRecord
    return <TableRow key={item.id}>
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{time(item.created_at)}</TableCell>
      <TableCell><strong className="font-medium">{eventLabels[item.event] ?? item.event}</strong>{item.action && <small>{actionLabels[item.action] ?? item.action}</small>}<small><StateBadge state={item.state} /></small></TableCell>
      <TableCell>{auditUsers(item)}</TableCell>
      <TableCell><span>{operatorLabels[item.operator ?? ''] ?? item.operator ?? '—'}</span></TableCell>
      <TableCell><p className="rc-reason-preview">{item.reason || '未记录原因'}</p>{item.note && <small className="rc-reason-preview">备注：{item.note}</small>}{item.warning && <small className="rc-reason-preview text-amber-600">{item.warning}</small>}{typeof item.affected === 'number' && <small>影响用户：{count(item.affected)}</small>}<Button variant="link" size="sm" type="button" className="h-7 p-0" onClick={() => setDetail({ kind: 'audit', record: item })}>{item.event.startsWith('user_lifecycle_') ? '查看删除记录' : '分析详情与风控指标'}<Eye className="ml-1 h-3.5 w-3.5" /></Button></TableCell>
    </TableRow>
  }

  function renderDetail() {
    if (!detail) return null
    if (detail.kind === 'assessments') {
      const item = detail.record as Assessment
      return <AssessmentView value={item} onViewUser={viewUser} onReviewSaved={reviewSaved} onUnauthorized={onUnauthorized} onBan={() => openAction('ban', undefined, item.user_id)} />
    }
    if (detail.kind === 'jobs') {
      const item = detail.record as ScanJob
      return <div className="rc-detail">
        <div className="rc-detail-heading"><code>{item.id}</code><StateBadge state={item.state} /></div>
        <dl className="rc-detail-grid">
          <div><dt>创建时间</dt><dd>{time(item.created_at)}</dd></div><div><dt>更新时间</dt><dd>{time(item.updated_at)}</dd></div>
          <div><dt>统计截止</dt><dd>{time(item.window_end)}</dd></div><div><dt>运行模式</dt><dd>{item.config.dry_run ? '试运行' : '正式扫描'}</dd></div>
          <div><dt>扫描进度</dt><dd>{item.scanned} / {item.user_ids.length}</dd></div><div><dt>完成评估</dt><dd>{item.assessed}</dd></div>
          <div><dt>已确认封禁</dt><dd>{item.banned}</dd></div><div><dt>规则版本</dt><dd>v{item.config.version}</dd></div>
        </dl>
        {item.errors?.length ? <Notice kind="warning"><ul className="rc-data-notes">{item.errors.map((value, index) => <li key={index}>{value}</li>)}</ul></Notice> : <p className="rc-muted">没有已记录的任务错误。</p>}
        <h4 className="mb-3 mt-5 text-sm font-semibold">扫描账号</h4><ScrollRegion label="本次扫描的账号" size="sm"><div className="rc-id-list">{item.user_ids.length ? item.user_ids.map(id => <UserIdentityButton userId={id} key={id} compact onClick={() => viewUser(id)} />) : <span className="rc-muted">窗口内没有可归属的账号。</span>}</div></ScrollRegion>
      </div>
    }
    if (detail.kind === 'actions') {
      const item = detail.record as RiskAction
      return <div className="rc-detail">
        <div className="rc-detail-heading">{userLink(item.user_id, item.username ?? item.before?.username)}<StateBadge state={item.state} /></div>
        <p className="rc-detail-reason">{item.reason}</p>
        <section className="space-y-2 rounded-lg border bg-muted/20 p-3"><h4 className="text-xs font-medium text-muted-foreground">处置时补充说明</h4><p className="whitespace-pre-wrap break-words text-sm">{item.note || '未填写补充说明'}</p><RemarkStatus sync={item.remark_sync} />{item.remark_sync && <p className="text-xs text-muted-foreground">此状态对应备注写入记录；当前完整备注在用户卡片或“用户备注”中查看。</p>}{item.remark_sync?.error && <Notice kind="warning">{item.remark_sync.error}</Notice>}</section>
        <dl className="rc-detail-grid">
          <div><dt>操作者</dt><dd>{item.operator}</dd></div><div><dt>创建时间</dt><dd>{time(item.created_at)}</dd></div>
          <div><dt>状态变更归属</dt><dd>{item.owns_transition ? '由本次处置改变' : '未确认本次变更'}</dd></div><div><dt>已解除</dt><dd>{item.released_by ? '是' : '否'}</dd></div>
          <div><dt>关联评估</dt><dd>{item.assessment_id || '人工处置'}</dd></div><div><dt>规则版本</dt><dd>{item.rule_version ? `v${item.rule_version}` : '—'}</dd></div>
        </dl>
        <div className="rc-snapshots"><Snapshot title="变更前" value={item.before} /><Snapshot title="变更后" value={item.after} /></div>
        {item.error && <Notice kind="warning">{item.error}</Notice>}
        <div className="mt-5 flex flex-wrap items-center gap-2">
          {item.action === 'ban' && <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openNote(item.user_id, item.id)}><PencilLine className="mr-2 h-4 w-4" />编辑用户备注</Button>}
          {canRelease(item) && <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openAction('release', item)}><RotateCcw className="mr-2 h-4 w-4" />解除本次封禁</Button>}
          {canReconcile(item) && <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openAction('reconcile', item)}><Search className="mr-2 h-4 w-4" />核对上游状态</Button>}
        </div>
      </div>
    }
    const item = detail.record as AuditRecord
    return <div className="rc-detail">
      <div className="rc-detail-heading"><strong>{eventLabels[item.event] ?? item.event}</strong><StateBadge state={item.state} /><RemarkStatus sync={item.remark_sync} /></div>
      <p className="rc-detail-reason">{item.reason || '未记录原因'}</p>
      <dl className="rc-detail-grid">
        <div><dt>账号</dt><dd>{auditUsers(item, 20)}</dd></div><div><dt>操作者</dt><dd>{operatorLabels[item.operator ?? ''] ?? item.operator ?? '—'}</dd></div>
        <div><dt>事件时间</dt><dd>{time(item.created_at)}</dd></div><div><dt>关联处置</dt><dd>{item.action_id || '—'}</dd></div>
        {item.resolution && <div><dt>核对结果</dt><dd>{item.resolution}</dd></div>}
        {item.action && <div><dt>操作类型</dt><dd>{actionLabels[item.action] ?? item.action}</dd></div>}
        {typeof item.affected === 'number' && <div><dt>影响用户数</dt><dd>{count(item.affected)}</dd></div>}
      </dl>
      {(item.event === 'action_note_updated' || item.event.startsWith('user_remark_') || item.event.startsWith('action_remark_')) && <div className="rc-snapshots"><section className="rounded-lg border p-3"><h4 className="text-xs text-muted-foreground">修改前用户备注</h4><p className="mt-2 whitespace-pre-wrap break-words text-sm">{typeof item.remark_before === 'string' ? item.remark_before || '空' : item.previous_note || '空'}</p></section><section className="rounded-lg border p-3"><h4 className="text-xs text-muted-foreground">提交的用户备注</h4><p className="mt-2 whitespace-pre-wrap break-words text-sm">{typeof item.remark_after === 'string' ? item.remark_after || '空' : item.note || '空'}</p></section></div>}
      {(item.error || item.remark_sync?.error) && <Notice kind="warning">{item.error || item.remark_sync?.error}</Notice>}
      {item.warning && <Notice kind="warning">{item.warning}</Notice>}
      {item.event.startsWith('user_lifecycle_') ? <section className="space-y-3 rounded-xl border bg-muted/10 p-4"><h4 className="text-sm font-semibold">操作时记录的账号信息</h4><p className="text-xs leading-relaxed text-muted-foreground">账号删除后仍保留当时的账号 ID、名称、原因与结果。已移除账号的当前详情可能不再可用。</p><ScrollRegion label="删除操作关联账号" size="sm"><div className="flex flex-wrap gap-2">{(item.user_ids ?? []).slice(0, 100).map((id, index) => <Badge key={id} variant="outline" className="max-w-full break-all">{item.usernames?.[index] || '未记录名称'} · #{id}</Badge>)}</div></ScrollRegion>{(item.user_ids?.length ?? 0) > 100 && <p className="text-xs text-muted-foreground">当前显示前 100 个账号，共记录 {count(item.user_ids?.length)} 个账号。</p>}</section> : <div className="rc-snapshots"><Snapshot title="原状态" value={item.before} /><Snapshot title="观测状态" value={item.observed_user_state ?? item.after} /></div>}
    </div>
  }

  function renderAIConfigPanel() {
    if (!draft) return <Card><EmptyState title={configLoading ? '正在读取配置...' : '规则与 AI 配置暂不可用'} /></Card>
    return <Card className="min-w-0 overflow-hidden">
      <CardHeader className="border-b bg-card py-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Button type="button" variant="ghost" className="h-auto gap-3 p-0 text-lg font-semibold hover:bg-transparent" aria-expanded={configExpanded} aria-controls="risk-review-config" onClick={() => setConfigExpanded(value => !value)}>
            <Settings2 className="h-5 w-5 text-primary" />规则与 AI 配置<ChevronDown className={`h-5 w-5 text-muted-foreground transition-transform ${configExpanded ? 'rotate-180' : ''}`} />
            {dirty && <Badge variant="warning">未保存</Badge>}
          </Button>
          <Button type="button" variant="link" size="sm" className="h-auto p-0" onClick={() => setLogicOpen(true)}><Info className="mr-2 h-4 w-4" />了解检测逻辑</Button>
        </div>
      </CardHeader>
      {configExpanded && <CardContent id="risk-review-config" className="space-y-6 pt-6">
        <div className="grid grid-cols-1 gap-6 md:grid-cols-2 md:gap-8">
          <div className="space-y-4">
            <label className="rc-field"><span>API 地址</span><Input type="url" className="h-10 bg-muted/20" placeholder="https://api.example.com" maxLength={2000} required={draft.ai.enabled} disabled={uiBusy} value={draft.ai.base_url} onChange={event => editConfig({ ...draft, ai: { ...draft.ai, base_url: event.target.value } })} /></label>
            <div className="rc-field"><label htmlFor="risk-ai-key">API 密钥</label><div className="relative">
              <Input id="risk-ai-key" type={showApiKey ? 'text' : 'password'} autoComplete="new-password" className="h-10 bg-muted/20 pr-10" maxLength={4096} disabled={uiBusy} placeholder={canUseSavedKey ? '留空保留当前地址的已保存密钥' : '填写该 API 地址的连接密钥'} required={draft.ai.enabled && !canUseSavedKey} value={draft.ai.api_key ?? ''} onChange={event => editConfig({ ...draft, ai: { ...draft.ai, api_key: event.target.value } })} />
              <Button variant="ghost" size="icon" type="button" className="absolute right-1 top-1 h-8 w-8 text-muted-foreground" aria-label={showApiKey ? '隐藏密钥' : '显示密钥'} onClick={() => setShowApiKey(value => !value)}>{showApiKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}</Button>
            </div><span className={`text-xs font-normal ${config?.ai.api_key_configured && !canUseSavedKey ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'}`}>{canUseSavedKey ? '当前地址已有已保存密钥，留空即可使用。' : config?.ai.api_key_configured ? 'API 地址已改变，填写对应新密钥；原密钥不会发送到新地址。' : '尚未保存连接密钥。'}</span></div>
            <label className="rc-field"><span>请求超时（秒）</span><Input type="number" min={1} max={60} required disabled={uiBusy} value={draft.ai.timeout_seconds || ''} onChange={event => editConfig({ ...draft, ai: { ...draft.ai, timeout_seconds: Number(event.target.value) } })} /></label>
          </div>
          <div className="space-y-4">
            <div className="rc-field">
              <label htmlFor="risk-ai-model">AI 模型</label>
              <div className="rc-model-controls"><Input id="risk-ai-model" className="h-10 min-w-0 flex-1 bg-muted/20" maxLength={200} required={draft.ai.enabled} disabled={uiBusy} placeholder="手动填写，或拉取后选择模型" value={draft.ai.model} onChange={event => editConfig({ ...draft, ai: { ...draft.ai, model: event.target.value } })} />
                <Button variant="outline" type="button" disabled={mutationBlocked || !hasAIConnection} onClick={() => void loadModels()}>{aiBusy === 'models' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <RefreshCw className="mr-2 h-4 w-4" />}{models ? '重新拉取' : '拉取模型'}</Button>
              </div>
              <p className="text-xs font-normal leading-relaxed text-muted-foreground">使用当前 API 地址和密钥拉取可用模型，无需先保存。拉取列表后，选定模型再验证审核连接。</p>
              {modelsError && <Notice kind="error">{modelsError}</Notice>}
              {aiBusy === 'models' && <p className="flex items-center gap-2 py-2 text-xs text-muted-foreground" role="status"><LoaderCircle className="h-4 w-4 animate-spin" />正在读取该密钥可用的模型…</p>}
              {models && <div className="rc-model-browser">
                <div className="rc-model-browser-heading"><strong>可用模型 <Badge variant="secondary">{count(models.total)}</Badge></strong><span>点击选择</span></div>
                <div className="relative"><Search className="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" /><Input type="search" className="h-9 pl-9" aria-label="搜索可用 AI 模型" placeholder="搜索模型名称或提供方" value={modelSearch} onChange={event => setModelSearch(event.target.value)} /></div>
                <ScrollRegion label="可用 AI 模型列表" size="sm" className="rc-model-options-scroll">
                  {filteredModels.length ? <ul className="rc-model-options">{filteredModels.slice(0, 200).map(model => <li key={model.id}>
                    <button type="button" className="rc-model-option" aria-pressed={draft.ai.model === model.id} disabled={uiBusy || model.id.length > 200} title={model.id.length > 200 ? '该模型名称超过配置字段的 200 字符限制，请手动填写有效别名。' : model.id} onClick={() => editConfig({ ...draft, ai: { ...draft.ai, model: model.id } })}>
                      <span><strong>{model.id}</strong>{model.owned_by && <small>{model.owned_by}</small>}</span>{draft.ai.model === model.id && <Check className="h-4 w-4 shrink-0" />}
                    </button>
                  </li>)}</ul> : <p className="rc-model-empty">{models.total === 0 ? '接口返回空模型列表，可继续手动填写模型并验证。' : '没有匹配模型，调整搜索关键词或手动填写。'}</p>}
                </ScrollRegion>
                <p className="text-xs font-normal text-muted-foreground">{filteredModels.length > 200 ? `匹配 ${count(filteredModels.length)} 个，当前显示前 200 个，输入更多关键词缩小范围。` : `显示 ${count(filteredModels.length)} / ${count(models.total)} 个模型。`}</p>
                <p className="rc-model-endpoint" title={models.endpoint}>列表来源：{models.endpoint}</p>
              </div>}
              <div className="rc-model-test-actions"><Button variant="outline" type="button" disabled={mutationBlocked || !hasAIConnection || !draft.ai.model.trim()} title="验证当前未保存草稿中的模型连接与结构化响应" onClick={() => void testAI()}>{aiBusy === 'test' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Activity className="mr-2 h-4 w-4" />}连接验证</Button><span>验证会调用当前模型，不保存配置或创建扫描。</span></div>
              {aiError && <Notice kind="error">{aiError}</Notice>}
              {aiTest && <div className="rc-model-verified" role="status"><Check className="h-4 w-4 shrink-0" /><span><strong>连接与结构化响应验证通过</strong><small>{aiTest.model || draft.ai.model} · 当前草稿{dirty ? '尚未保存' : '与已保存配置一致'}</small></span></div>}
            </div>
            <label className="rc-checkbox rounded-lg border bg-muted/20 p-3"><input type="checkbox" checked={draft.ai.enabled} disabled={uiBusy} onChange={event => editConfig({ ...draft, ai: { ...draft.ai, enabled: event.target.checked } })} /><span><strong className="block font-semibold text-foreground">启用 AI 辅助审核</strong><span className="mt-1 block text-xs">对结构化规则证据给出审核意见</span></span></label>
            <label className="rc-checkbox rounded-lg border bg-muted/20 p-3"><input type="checkbox" checked={draft.dry_run} disabled={uiBusy} onChange={event => editConfig({ ...draft, dry_run: event.target.checked })} /><span><strong className="block font-semibold text-foreground">试运行模式</strong><span className="mt-1 block text-xs">保留结果和执行计划，跳过账号变更</span></span></label>
            <div className="rc-field"><label htmlFor="risk-ai-schedule">定时扫描</label><Select id="risk-ai-schedule" aria-label="定时扫描" value={draft.enabled ? draft.scan_interval_seconds : 0} disabled={uiBusy} onChange={event => { const seconds = Number(event.target.value); editConfig({ ...draft, enabled: seconds > 0, scan_interval_seconds: seconds || draft.scan_interval_seconds }) }}>{scanSchedules.map(item => <option key={item.seconds} value={item.seconds}>{item.label}</option>)}{!scanSchedules.some(item => item.seconds === draft.scan_interval_seconds) && <option value={draft.scan_interval_seconds}>每 {draft.scan_interval_seconds} 秒（当前）</option>}</Select></div>
          </div>
        </div>
        <section className="space-y-4 border-t pt-5"><h3 className="text-sm font-semibold">检测阈值与处置策略</h3><div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">{ruleFields.map(field => <label className="rc-field" key={field.key}><span>{field.label}</span><Input type="number" min={field.min} max={field.max} step={field.step} required disabled={uiBusy} value={draft[field.key] || ''} onChange={event => editConfig({ ...draft, [field.key]: Number(event.target.value) })} /></label>)}</div>
          <label className="rc-checkbox rounded-lg border bg-muted/20 p-3"><input type="checkbox" checked={draft.auto_execute} disabled={uiBusy} onChange={event => editConfig({ ...draft, auto_execute: event.target.checked })} /><span><strong className="block font-semibold text-foreground">允许规则自动封禁</strong><span className="mt-1 block text-xs">完整历史、调用基线和组合规则同时满足时，正式扫描才执行。</span></span></label>
          <label className="rc-field"><span>账号白名单</span><textarea ref={whitelistField} className="rc-textarea" rows={2} disabled={uiBusy} placeholder="填写账号 ID，以逗号或换行分隔" value={whitelistDraft} onChange={event => { setWhitelistDraft(event.target.value); editConfig(draft) }} /></label>
        </section>
        <section className="space-y-4 border-t pt-5"><div><h3 className="text-sm font-semibold">关联与调用异常复核</h3><p className="mt-1 text-xs leading-relaxed text-muted-foreground">以下规则只进入人工复核。失败率与模型规则比较上一等长窗口，额度规则要求足够调用和额度基线；失败可能来自上游故障。</p></div><div className="grid gap-3 sm:grid-cols-2">{reviewRuleSwitches.map(field => <label key={field.key} className="rc-checkbox rounded-lg border bg-muted/10 p-3"><input type="checkbox" disabled={uiBusy} checked={draft.review_rules[field.key]} onChange={event => editConfig({ ...draft, review_rules: { ...draft.review_rules, [field.key]: event.target.checked } })} /><span>{field.label}</span></label>)}</div><div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{reviewRuleFields.map(field => <label key={field.key} className="rc-field"><span>{field.label}</span><Input type="number" min={field.min} max={field.max} step={field.step} required disabled={uiBusy} value={draft.review_rules[field.key] || ''} onChange={event => editConfig({ ...draft, review_rules: { ...draft.review_rules, [field.key]: Number(event.target.value) } })} /></label>)}</div></section>
        {draft.auto_execute && !draft.dry_run && <Notice kind="warning">当前草稿允许规则自动改变普通账号状态，首次开启时需确认。</Notice>}
        <div className="rc-ai-config-actions flex flex-wrap items-center justify-between gap-3 border-t pt-4"><p className="text-xs text-muted-foreground">当前版本 v{config?.version ?? draft.version} · 配置在保存后应用。</p><div className="flex gap-2"><Button variant="outline" type="button" disabled={uiBusy} onClick={() => { aiOperation.current?.abort(); aiOperation.current = null; aiOperationKind.current = ''; setAIBusy(''); setAITest(null); setAIError(''); if (config) { setDraft(cloneConfig(config)); setWhitelistDraft(config.whitelist.join(', ')); setDirty(false); dirtyRef.current = false }; setConfigExpanded(false) }}>取消修改</Button><Button type="submit" disabled={uiBusy || !!aiBusy || !dirty}>{busy === 'save-config' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}保存配置</Button></div></div>
      </CardContent>}
    </Card>
  }

  function renderAssessmentResult() {
    if (!assessment) return null
    return <Card className="border-2 border-primary/20 shadow-lg">
      <CardHeader className="border-b bg-primary/5 pb-3">
        <div className="flex items-center justify-between gap-3">
          <CardTitle className="flex items-center gap-2 text-lg"><Activity className="h-5 w-5 text-primary" />{assessmentUsesAI ? 'AI 评估结果' : '账号分析结果'}</CardTitle>
          <Button variant="ghost" size="sm" type="button" onClick={() => setAssessment(null)}>关闭</Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-5 p-4">
        <div className="flex flex-wrap items-center gap-3 text-sm"><span className="text-muted-foreground">账号</span>{userLink(assessment.user_id, assessment.username)}<Badge variant="secondary">{aiLabels[assessment.ai?.status] ?? assessment.ai?.status ?? '未调用'}</Badge></div>
        {assessmentUsesAI && <>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div className="rounded-lg border p-3 text-center"><div className="text-xl font-bold">{assessment.ai?.risk_level === 'high' ? '高风险' : assessment.ai?.risk_level === 'medium' ? '需复核' : assessment.ai?.risk_level === 'low' ? '低风险' : '—'}</div><div className="mt-1 text-xs text-muted-foreground">AI 风险判断</div></div>
            <div className="rounded-lg border p-3 text-center"><div className="text-2xl font-bold">{assessment.ai?.confidence == null ? '—' : `${Math.round(assessment.ai.confidence * 100)}%`}</div><div className="mt-1 text-xs text-muted-foreground">置信度</div></div>
            <div className="rounded-lg border p-3 text-center"><div className="text-xl font-bold">{actionLabels[assessment.ai?.recommendation ?? ''] ?? assessment.ai?.recommendation ?? '—'}</div><div className="mt-1 text-xs text-muted-foreground">AI 意见</div></div>
          </div>
          <div className="rounded-lg border bg-muted/30 p-3"><div className="mb-1 text-xs text-muted-foreground">AI 分析理由</div><p className="text-sm leading-relaxed">{assessment.ai?.reason || (assessment.ai?.status === 'disabled' ? 'AI 辅助审核未启用，本次记录为规则评估。' : '尚无模型分析理由。')}</p></div>
        </>}
        <AssessmentView value={assessment} onViewUser={viewUser} showAI={!assessmentUsesAI} onReviewSaved={reviewSaved} onUnauthorized={onUnauthorized} onBan={() => openAction('ban', undefined, assessment.user_id)} />
      </CardContent>
    </Card>
  }

  function renderAIReviewRow(record: RecordType) {
    const item = record as Assessment
    return <TableRow key={item.id || `${item.user_id}:${item.created_at}`}>
      <TableCell>{userLink(item.user_id, item.username)}<small className="mt-1 block text-xs text-muted-foreground">{time(item.created_at)}</small></TableCell>
      <TableCell><div className="flex flex-wrap gap-1.5"><RiskBadge assessment={item} />{item.review && <Badge variant="secondary">{reviewLabels[item.review.status]}</Badge>}{item.findings.slice(0, 2).map(finding => <Badge key={finding.id} variant="outline" className="max-w-[220px] truncate text-[11px]">{findingLabels[finding.id] ?? finding.id}</Badge>)}</div></TableCell>
      <TableCell className="text-right font-mono">{item.score}<span className="text-xs text-muted-foreground"> / 100</span></TableCell>
      <TableCell className="text-right font-mono">{count(item.features.calls)}</TableCell>
      <TableCell className="text-right font-mono">{count(item.features.login_failures)}</TableCell>
      <TableCell className="text-right font-mono">{count(item.features.call_ips.length)}</TableCell>
      <TableCell><div className="flex items-center justify-center gap-1">
        <Button variant="ghost" size="icon" type="button" className="h-8 w-8 text-primary" title="查看规则证据" aria-label={`查看账号 ${item.user_id} 的规则证据`} onClick={() => setDetail({ kind: 'assessments', record: item })}><Eye className="h-4 w-4" /></Button>
        <Button variant="ghost" size="icon" type="button" className="h-8 w-8 text-destructive" title={config?.ai.enabled ? '发起 AI 审核' : '发起规则评估'} aria-label={`评估账号 ${item.user_id}`} disabled={mutationBlocked} onClick={() => void analyze(true, item.user_id)}>{busy === 'assess' && userID === String(item.user_id) ? <LoaderCircle className="h-4 w-4 animate-spin" /> : <ShieldAlert className="h-4 w-4" />}</Button>
      </div></TableCell>
    </TableRow>
  }

  function renderHistoryTable() {
    const labels = historyKind === 'assessments' ? ['账号', '规则评分', '证据与意见', '评估方式', '评估时间', '操作']
      : historyKind === 'jobs' ? ['扫描批次', '状态', '进度', '处置结果', '创建时间', '操作']
        : historyKind === 'actions' ? ['用户与动作', '状态', '原因 / 备注 / 执行人', '状态变化', '更新时间', '操作']
          : ['日志时间', '审计动作', '目标用户', '执行人', '分析详情与风控指标']
    return <>
      {historyError && <div className="p-4"><Notice kind="error">{historyError}<Button variant="outline" size="sm" type="button" className="ml-3" disabled={mutationBlocked} onClick={() => setRevision(value => value + 1)}>重试</Button></Notice></div>}
      {history?.items.length ? <Table className="rc-history-table min-w-[960px]" containerClassName="scroll-region scroll-region--lg" containerLabel="风控审查与审计记录"><TableHeader><TableRow>{labels.map(label => <TableHead key={label} className="whitespace-nowrap">{label}</TableHead>)}</TableRow></TableHeader><TableBody>{history.items.map(renderHistoryRow)}</TableBody></Table>
        : historyLoading ? <div className="flex min-h-48 items-center justify-center gap-2 text-sm text-muted-foreground" role="status"><LoaderCircle className="h-5 w-5 animate-spin text-primary" />正在读取{activeHistoryLabel}...</div>
          : <ReviewEmpty icon={History} title={UI_PREVIEW_ONLY ? '数据获取已暂停' : historyError ? `${activeHistoryLabel}暂不可用` : `暂无${activeHistoryLabel}`} description={historyError ? '修复数据源后重新读取，当前不表示没有风险。' : historyKind === 'assessments' ? '单账号审核或扫描后生成账号结论。' : historyKind === 'jobs' ? '一次扫描包含多个账号，这里记录批次执行进度。' : '操作完成后在这里保留记录。'} />}
      <div className="rc-pagination border-t px-4 py-4" aria-label={`${activeHistoryLabel}分页`}><span className="text-sm text-muted-foreground">共 {UI_PREVIEW_ONLY ? '—' : count(history?.total)} 条记录</span><div className="flex items-center gap-2"><Button variant="outline" size="sm" type="button" disabled={mutationBlocked || historyLoading || page <= 1} onClick={() => setPage(value => Math.max(1, value - 1))}>上一页</Button><span className="px-2 text-sm font-medium">{page} / {Math.max(1, Math.ceil((history?.total ?? 0) / PAGE_SIZE))}</span><Button variant="outline" size="sm" type="button" disabled={mutationBlocked || historyLoading || !history?.has_more} onClick={() => setPage(value => value + 1)}>下一页</Button></div></div>
    </>
  }

  function renderBannedUsers() {
    return <>
      <form className="flex flex-wrap items-center gap-3 border-b p-4" onSubmit={event => { event.preventDefault(); setBanPage(1); setBanSearch(banSearchDraft.trim()) }}><Input className="max-w-sm" aria-label="搜索封禁用户" value={banSearchDraft} onChange={event => setBanSearchDraft(event.target.value)} placeholder="搜索用户 ID、用户名、昵称或邮箱" /><Button variant="outline" size="sm" type="submit" disabled={UI_PREVIEW_ONLY || bansLoading}><Search className="mr-2 h-4 w-4" />查询</Button></form>
      {bansError && <div className="p-4"><Notice kind="error">{bansError}</Notice></div>}
      {bans?.history_warning && <div className="p-4"><Notice kind="warning">{bans.history_warning}</Notice></div>}
      {bans?.items.length ? <Table className="rc-history-table min-w-[1060px]" containerClassName="scroll-region scroll-region--lg" containerLabel="当前封禁用户列表"><TableHeader><TableRow>{['用户信息', '使用统计', '封禁时间', '执行人', '封禁原因与备注', '操作'].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{bans.items.map(item => {
        const ban = item.ban
        const record = ban ?? item.last_action
        const money = (quota: number) => new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 }).format(quota / quotaPerUnit)
        return <TableRow key={item.user.id}>
          <TableCell><UserIdentityButton userId={item.user.id} username={item.user.username} displayName={item.user.display_name} group={item.user.group} email={item.user.email} onClick={() => viewUser(item.user.id, item.user.username)} /></TableCell>
          <TableCell><span>{count(item.user.upstream_request_count)} 次请求</span><small>已用额度 {money(item.user.used_quota)}</small><small>当前余额 {money(item.user.quota)}</small></TableCell>
          <TableCell className="whitespace-nowrap text-xs">{ban ? time(ban.created_at) : '上游未提供'}<small>{item.owned ? '本站确认持有' : '来源未确认'}</small></TableCell>
          <TableCell>{ban?.operator || '上游未提供'}</TableCell>
          <TableCell><p className="rc-reason-preview" title={ban?.reason}>{ban?.reason || '无可确认的本站封禁原因'}</p><small className="rc-reason-preview whitespace-pre-wrap" title={item.user.remark}>{item.user.remark_supported === false ? '当前 NewAPI 不支持用户备注' : item.user.remark ? `用户备注：${item.user.remark}` : '暂无用户备注'}</small><RemarkStatus sync={ban?.remark_sync} /></TableCell>
          <TableCell><div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" type="button" onClick={() => viewUser(item.user.id, item.user.username)}><Eye className="mr-1 h-3.5 w-3.5" />用户详情</Button>{item.owned && ban && canRelease(ban) && <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openAction('release', ban)}><RotateCcw className="mr-1 h-3.5 w-3.5" />解封</Button>}<Button variant="ghost" size="sm" type="button" disabled={mutationBlocked} onClick={() => openNote(item.user.id, ban?.id)}><PencilLine className="mr-1 h-3.5 w-3.5" />用户备注</Button>{record && <Button variant="ghost" size="sm" type="button" onClick={() => setDetail({ kind: 'actions', record })}>处置详情</Button>}{!item.owned && <small className="max-w-52 text-xs text-muted-foreground">本次封禁归属未确认，保留上游状态。</small>}</div></TableCell>
        </TableRow>
      })}</TableBody></Table>
        : bansLoading ? <div className="flex min-h-56 items-center justify-center gap-2 text-muted-foreground"><LoaderCircle className="h-5 w-5 animate-spin" />正在读取封禁用户...</div>
          : <ReviewEmpty icon={ShieldCheck} title={UI_PREVIEW_ONLY ? '数据获取已暂停' : bansError ? '封禁名单暂不可用' : '当前没有封禁用户'} description="此名单按账号当前状态读取；历史处置在另一页签保留。" />}
      <div className="rc-pagination border-t px-4 py-4"><span className="text-sm text-muted-foreground">共 {count(bans?.total)} 个封禁用户</span><div className="flex items-center gap-2"><Button variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || bansLoading || banPage <= 1} onClick={() => setBanPage(value => value - 1)}>上一页</Button><span className="text-sm">{banPage} / {Math.max(1, Math.ceil((bans?.total ?? 0) / PAGE_SIZE))}</span><Button variant="outline" size="sm" disabled={UI_PREVIEW_ONLY || bansLoading || !bans?.has_more} onClick={() => setBanPage(value => value + 1)}>下一页</Button></div></div>
    </>
  }

  const reviewMode = UI_PREVIEW_ONLY || !config ? '—' : config.dry_run ? '试运行' : config.auto_execute ? '正式 · 允许规则处置' : '正式 · 只评估'
  const nextScanAt = config?.enabled && latestJob?.created_at ? latestJob.created_at + config.scan_interval_seconds : null
  return <div className="rc-page space-y-6">
    <div className="flex flex-col items-start justify-between gap-4 sm:flex-row sm:items-center">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-3"><h1 className="text-3xl font-bold">风控中心</h1>
          {tab === 'ranking' ? <Badge variant="outline" className="gap-1.5 border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300"><span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />{UI_PREVIEW_ONLY ? '数据已暂停' : '实时流量监控'}</Badge>
            : tab === 'ai' && <Badge variant={UI_PREVIEW_ONLY || !config ? 'secondary' : config.enabled ? 'success' : 'outline'}>{UI_PREVIEW_ONLY ? '数据已暂停' : !config ? '状态未读取' : config.enabled ? '定时扫描运行中' : '定时扫描已关闭'}</Badge>}
        </div>
        <p className="mt-1 text-sm text-muted-foreground">实时 Top 10 · IP 监控 · 账号分析 · 封禁与审计</p>
      </div>
      <div className="flex flex-wrap gap-2">
        {tab !== 'ranking' && <Button variant="outline" size="sm" type="button" disabled={uiBusy || UI_PREVIEW_ONLY} onClick={() => setRevision(value => value + 1)}><RefreshCw className={`mr-2 h-4 w-4 ${configLoading ? 'animate-spin' : ''}`} />刷新</Button>}
        <Button variant="outline" size="sm" type="button" disabled={uiBusy} onClick={() => openAction('ban')}><ShieldBan className="mr-2 h-4 w-4" />人工封禁</Button>
      </div>
    </div>
    {error && <Notice kind="error">{error}</Notice>}
    {warning && <Notice kind="warning">{warning}</Notice>}
    {message && <Notice kind="success">{message}</Notice>}
    <Tabs value={tab} onValueChange={value => chooseTab(value as Tab)} className="min-w-0 space-y-6">
      <div className="rc-tabs-scroll"><TabsList aria-label="风险控制工作区" className="w-max">{workspaceTabs.map(item => <TabsTrigger key={item.id} value={item.id} className="gap-2"><item.icon className="h-4 w-4" />{item.label}</TabsTrigger>)}</TabsList></div>
      <TabsContent value="ranking" className="mt-0"><RiskRealtimeRanking tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} onViewUser={viewUser} onBanUser={id => openAction('ban', undefined, id)} refreshRevision={revision} actionsDisabled={mutationBlocked} /></TabsContent>
      <TabsContent value="matrix" className="mt-0">
        <Card className="min-w-0 overflow-hidden">
          <CardHeader className="border-b py-4"><div className="flex flex-wrap items-center justify-between gap-3"><CardTitle className="text-lg">账号矩阵</CardTitle><Badge variant={matrix?.groups.length ? 'warning' : 'secondary'}>{matrixLoading ? '读取中' : count(matrix?.total_groups ?? matrix?.groups.length)}</Badge></div></CardHeader>
          <CardContent className="space-y-4 p-4 sm:p-6">
            <p className="text-xs leading-relaxed text-muted-foreground">按已验证 BrowserID 和共享令牌将近 7 天账号分成证据组。矩阵只描述观测关联，不证明同一自然人；默认建议人工复核。</p>
            {matrixError && <Notice kind="error">{matrixError}</Notice>}
            {matrix?.warnings.map(warning => <Notice key={warning} kind="warning">{warning}</Notice>)}
            {matrixLoading ? <div className="flex min-h-56 items-center justify-center gap-2 text-muted-foreground"><LoaderCircle className="h-5 w-5 animate-spin" />正在读取账号矩阵...</div>
              : matrix?.groups.length ? <ScrollRegion label="账号矩阵证据组" size="lg" fixed><div className="space-y-4">{matrix.groups.map(group => <article key={group.id} className="rounded-xl border bg-muted/20 p-4">
                <div className="flex flex-wrap items-center gap-2"><strong className="rounded-full border bg-background px-3 py-1 font-mono text-xs">{group.id}</strong><Badge variant={group.strength === 'high' ? 'destructive' : 'warning'}>{group.strength === 'high' ? '强证据' : '中证据'}</Badge><Badge variant="secondary">{group.total_users ?? group.members.length} 个账号</Badge>{group.truncated && <Badge variant="outline">当前展示 {group.members.length} 个</Badge>}</div>
                <p className="mt-2 text-xs leading-relaxed text-muted-foreground">{group.reason}</p>
                <div className="mt-3 overflow-x-auto"><table className="min-w-[760px] w-full text-xs"><thead><tr className="border-b text-left text-muted-foreground">{['账号', 'BrowserID', '登录 IP', '调用 IP', '令牌', '邀请人'].map(label => <th key={label} className="px-2 py-2 font-medium">{label}</th>)}</tr></thead><tbody>{group.members.map(member => <tr key={member.user.id} className="border-b last:border-0"><td className="px-2 py-2"><UserIdentityButton userId={member.user.id} username={member.user.username} displayName={member.user.display_name} group={member.user.group} compact onClick={() => viewUser(member.user.id, member.user.username)} /></td><td className="px-2 py-2"><div className="flex flex-wrap gap-1">{(member.browser_ids ?? []).map(value => <code key={value} className="rounded-full border bg-muted/50 px-2 py-1 font-mono">{value}</code>)}</div></td><td className="px-2 py-2"><div className="flex max-w-52 flex-wrap gap-1">{(member.login_ips ?? []).slice(0, 3).map(value => <code key={value} className="rounded-full border bg-primary/5 px-2 py-1 font-mono text-primary">{value}</code>)}{(member.login_ips?.length ?? 0) > 3 && <span className="rounded-full border bg-muted/50 px-2 py-1">+{member.login_ips!.length - 3}</span>}</div></td><td className="px-2 py-2"><div className="flex max-w-64 flex-wrap gap-1">{(member.call_ips ?? []).slice(0, 3).map(value => <code key={value} className="rounded-full border bg-muted/50 px-2 py-1 font-mono">{value}</code>)}{(member.call_ips?.length ?? 0) > 3 && <span className="rounded-full border bg-primary/10 px-2 py-1 text-primary">+{member.call_ips!.length - 3}</span>}</div></td><td className="px-2 py-2"><div className="flex flex-wrap gap-1">{(member.token_ids ?? []).map(value => <code key={value} className="rounded-full border bg-muted/50 px-2 py-1 font-mono">#{value}</code>)}</div></td><td className="px-2 py-2">{member.inviter_id ? `#${member.inviter_id}` : '—'}</td></tr>)}</tbody></table></div>
              </article>)}</div></ScrollRegion>
                : <ReviewEmpty icon={ShieldCheck} title={UI_PREVIEW_ONLY ? '数据获取已暂停' : matrixError ? '账号矩阵暂不可用' : '当前窗口未发现多账号关联'} description="低风险或未覆盖的数据不会被推断为安全；请确认探针和调用导入均已运行。" />}
          </CardContent>
        </Card>
      </TabsContent>
      <TabsContent value="ai" className="mt-0 space-y-6">
        <div className="rc-review-metrics" aria-label="AI 审核状态"><ReviewMetric label="服务状态" value={UI_PREVIEW_ONLY || !config ? '未读取' : config.enabled ? '定时运行中' : '定时已关闭'} icon={ShieldCheck} tone="neutral" /><ReviewMetric label="运行模式" value={reviewMode} icon={Activity} tone="green" /><ReviewMetric label="AI 模型" value={UI_PREVIEW_ONLY || !config ? '—' : config.ai.enabled ? config.ai.model || '未填写' : '未启用'} icon={Settings2} tone="blue" /><ReviewMetric label="可疑用户" value={candidateLoading ? '读取中' : candidateError ? '不可用' : count(candidates?.total)} icon={ShieldAlert} tone="red" /></div>
        <form onSubmit={submitConfig}>{renderAIConfigPanel()}</form>
        <div className="rc-ai-status rounded-lg border border-blue-100 bg-blue-50/80 p-4 dark:border-blue-900 dark:bg-blue-950/20"><div className="flex min-w-0 items-start gap-3"><div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-blue-100 text-blue-600 dark:bg-blue-900 dark:text-blue-300"><ShieldCheck className="h-4 w-4" /></div><div className="min-w-0 text-sm"><div className="flex flex-wrap items-center gap-2 font-semibold">当前状态<Badge variant={config?.dry_run ? 'warning' : 'outline'}>{reviewMode}</Badge><Badge variant="outline">{config?.ai.enabled ? 'AI 辅助审核' : '规则审核'}</Badge></div><p className="mt-1 text-xs leading-relaxed text-muted-foreground">上次扫描：{time(latestJob?.created_at)} · {config?.enabled ? `每 ${config.scan_interval_seconds} 秒自动执行${nextScanAt ? ` · 上次计划对应下次时间 ${time(nextScanAt)}` : ''}` : '定时扫描已关闭，可手动扫描'}</p></div></div><div className="rc-ai-status-actions flex flex-wrap items-center gap-2"><Button variant="outline" size="sm" type="button" onClick={() => { setConfigExpanded(true); setFocusWhitelist(true) }}><ShieldCheck className="mr-2 h-4 w-4" />白名单（{config ? config.whitelist.length : '—'}）</Button><Button variant="outline" size="sm" type="button" disabled={mutationBlocked || candidateLoading} onClick={() => setRevision(value => value + 1)}><RefreshCw className={`mr-2 h-4 w-4 ${candidateLoading ? 'animate-spin' : ''}`} />刷新列表</Button><Button size="sm" type="button" disabled={uiBusy || !UI_PREVIEW_ONLY && !config} onClick={openScan}>{busy === 'scan' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Activity className="mr-2 h-4 w-4" />}{config?.ai.enabled ? '执行 AI 扫描' : '执行规则扫描'}</Button></div></div>
        <Card className="min-w-0 overflow-hidden"><CardHeader className="border-b py-4"><CardTitle className="text-lg">可疑用户列表</CardTitle><p className="text-xs leading-relaxed text-muted-foreground">最近 7 天、当前规则版本的最新中高风险结果，剔除管理员、白名单、当前已封禁账号及已标为误报的本次评估；后续新证据仍会重新检测。</p></CardHeader><CardContent className="p-0">
          {candidateError && <div className="p-4"><Notice kind="error">{candidateError}</Notice></div>}
          {candidates?.truncated && <div className="p-4"><Notice kind="warning">候选历史达到读取上限，当前名单不是全部历史结果。</Notice></div>}
          {candidates?.items.length ? <Table className="min-w-[1000px]" containerClassName="scroll-region scroll-region--lg" containerLabel="可疑用户列表"><TableHeader><TableRow>{['用户', '规则证据与风险', '规则评分', '调用次数', '登录失败', '调用出口', '操作'].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{candidates.items.map(renderAIReviewRow)}</TableBody></Table>
            : candidateLoading ? <div className="flex min-h-56 items-center justify-center gap-2 text-muted-foreground"><LoaderCircle className="h-5 w-5 animate-spin" />正在读取可疑用户...</div>
              : <ReviewEmpty icon={ShieldCheck} title={UI_PREVIEW_ONLY ? '数据获取已暂停' : candidateError ? '可疑用户暂不可用' : '当前没有待复核的可疑用户'} description={candidateError ? '这不是无风险结论，请恢复读取后刷新。' : '仅反映最近可用审查记录，未扫描或缺少事件不会被认定安全。'} />}
          <div className="rc-pagination border-t px-4 py-4"><span className="text-sm text-muted-foreground">共 {count(candidates?.total)} 位用户</span><div className="flex items-center gap-2"><Button variant="outline" size="sm" type="button" disabled={mutationBlocked || candidateLoading || candidatePage <= 1} onClick={() => setCandidatePage(value => value - 1)}>上一页</Button><span className="text-sm">{candidatePage} / {Math.max(1, Math.ceil((candidates?.total ?? 0) / PAGE_SIZE))}</span><Button variant="outline" size="sm" type="button" disabled={mutationBlocked || candidateLoading || !candidates?.has_more} onClick={() => setCandidatePage(value => value + 1)}>下一页</Button></div></div>
        </CardContent></Card>
        <Card className="overflow-hidden"><CardHeader className="border-b py-4"><div className="flex flex-wrap items-center justify-between gap-3"><CardTitle className="text-lg">审查记录</CardTitle><div className="inline-flex rounded-lg border bg-muted/40 p-1" role="group" aria-label="审查记录类型">{(['assessments', 'jobs'] as const).map(kind => <Button key={kind} size="sm" type="button" variant={reviewTab === kind ? 'default' : 'ghost'} className="h-8" aria-pressed={reviewTab === kind} onClick={() => chooseReview(kind)}>{historyLabels[kind]}</Button>)}</div></div><p className="text-xs text-muted-foreground">账号评估是一名用户的检测结论；扫描批次记录一组账号的执行进度与结果。</p></CardHeader><CardContent className="p-0">{renderHistoryTable()}</CardContent></Card>
        <Card><CardContent className="space-y-4 pt-5"><div><h3 className="text-sm font-semibold">按账号查询</h3><p className="mt-1 text-xs text-muted-foreground">查看当前规则分析不会调用模型；发起审核会保存一条账号评估。</p></div><form className="rc-analyze-form" onSubmit={event => { event.preventDefault(); void analyze(false) }}><label className="rc-field rc-user-field"><span>账号 ID</span><Input inputMode="numeric" pattern="[0-9]+" placeholder="输入账号 ID" value={userID} onChange={event => setUserID(event.target.value)} required /></label><Button variant="outline" size="sm" type="submit" disabled={mutationBlocked || !userID.trim()}><Search className="mr-2 h-4 w-4" />查看分析</Button><Button size="sm" type="button" disabled={mutationBlocked || !userID.trim()} onClick={() => void analyze(true)}><ShieldAlert className="mr-2 h-4 w-4" />{config?.ai.enabled ? '发起 AI 审核' : '发起规则审核'}</Button></form></CardContent></Card>
        {renderAssessmentResult()}
      </TabsContent>
      <TabsContent value="ip" className="mt-0"><RiskIPMonitor tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} onViewUser={viewUser} onBanUser={id => openAction('ban', undefined, id)} /></TabsContent>
      <TabsContent value="actions" className="mt-0"><Card className="overflow-hidden"><CardHeader className="border-b py-4"><div className="flex flex-wrap items-center justify-between gap-3"><CardTitle className="text-lg">封禁列表</CardTitle><div className="inline-flex rounded-lg border bg-muted/40 p-1" role="group" aria-label="封禁列表类型"><Button type="button" size="sm" variant={banView === 'current' ? 'default' : 'ghost'} onClick={() => setBanView('current')}>当前封禁</Button><Button type="button" size="sm" variant={banView === 'history' ? 'default' : 'ghost'} onClick={() => setBanView('history')}>处置历史</Button></div></div><p className="text-xs text-muted-foreground">当前封禁按账号状态读取，处置历史包含封禁、解除及状态核对。</p></CardHeader><CardContent className="p-0">{banView === 'current' ? renderBannedUsers() : renderHistoryTable()}</CardContent></Card></TabsContent>
      <TabsContent value="audit" className="mt-0"><Card className="overflow-hidden"><CardHeader className="border-b py-4"><CardTitle className="text-lg">审计日志</CardTitle><p className="text-xs text-muted-foreground">保存执行人、原因、状态快照与备注修改，查看目标用户会打开账号卡片。</p></CardHeader><CardContent className="p-0">{renderHistoryTable()}</CardContent></Card></TabsContent>
    </Tabs>

    {trackedJob && <Card ref={trackedSection} aria-live="polite"><CardContent className="flex flex-wrap items-center gap-4 p-4"><div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">{tracking ? <LoaderCircle className="h-5 w-5 animate-spin" /> : <ClipboardList className="h-5 w-5" />}</div><div className="min-w-40 flex-1 space-y-2"><strong className="text-sm font-medium">扫描批次 <code className="font-mono text-xs text-muted-foreground">{briefID(trackedJob.id)}</code></strong><p className="text-xs text-muted-foreground">{count(trackedJob.scanned)} / {count(trackedJob.user_ids?.length ?? 0)} 个账号 · 已评估 {count(trackedJob.assessed)} · 已确认处置 {count(trackedJob.banned)}</p><progress className="rc-progress" max={Math.max(1, trackedJob.user_ids?.length ?? 0)} value={trackedJob.scanned} aria-label="扫描进度" /></div><StateBadge state={trackedJob.state} />{tracking ? <Button variant="outline" size="sm" type="button" onClick={() => setTracking(false)}><Square className="mr-2 h-3.5 w-3.5" />停止页面跟踪</Button> : ['pending', 'running'].includes(trackedJob.state) ? <Button variant="outline" size="sm" type="button" disabled={UI_PREVIEW_ONLY} onClick={() => setTracking(true)}>继续跟踪</Button> : <Button variant="ghost" size="icon" type="button" onClick={() => setTrackedJob(null)} aria-label="收起批次状态"><X className="h-4 w-4" /></Button>}</CardContent></Card>}

    {selectedUser && <AdminDialog title={`用户详情 · ${selectedUser.username || `#${selectedUser.id}`}`} wide onClose={() => setSelectedUser(null)}><RiskUserCard userId={selectedUser.id} username={selectedUser.username} tokenVersion={tokenVersion} onUnauthorized={onUnauthorized} /></AdminDialog>}

    {detail && <AdminDialog title={detail.kind === 'assessments' ? '账号审核证据' : detail.kind === 'jobs' ? '扫描批次详情' : detail.kind === 'actions' ? '处置详情' : '审计事件详情'} wide onClose={() => setDetail(null)}>
      {renderDetail()}
      <details className="rc-raw-record"><summary>原始记录</summary><pre>{JSON.stringify(detail.record, null, 2)}</pre></details>
    </AdminDialog>}

    {actionDialog && <AdminDialog title={actionDialog.kind === 'ban' ? '人工封禁账号' : actionDialog.kind === 'release' ? '解除本次封禁' : '核对上游状态'} onClose={() => { if (!uiBusy) setActionDialog(null) }}>
      <form className="rc-dialog-form" onSubmit={submitAction}>
        <Notice kind={actionDialog.kind === 'ban' ? 'warning' : 'info'}>{actionDialog.kind === 'ban'
          ? '将改变该账号状态并记录处置原因。管理员账号由后端保护。'
          : actionDialog.kind === 'release' ? '仅恢复本次处置改变的账号状态；存在后续变更时会拒绝覆盖。'
            : '本次只读取当前上游状态并记录核对结果，不重新执行封禁或解封。'}</Notice>
        {dialogError && <Notice kind="error">{dialogError}</Notice>}
        {UI_PREVIEW_ONLY && <p className="text-xs text-muted-foreground">数据获取已暂停</p>}
        <label className="rc-field"><span>账号 ID</span><Input inputMode="numeric" pattern="[0-9]+" value={actionUserID} onChange={event => {
          setActionUserID(event.target.value)
          setActionDialog(current => current ? { ...current, key: actionKey() } : null)
        }} disabled={actionDialog.kind !== 'ban' || uiBusy} required /></label>
        {actionDialog.record && <dl className="rc-detail-grid"><div><dt>原处置</dt><dd><code>{briefID(actionDialog.record.id)}</code></dd></div><div><dt>当前记录</dt><dd><StateBadge state={actionDialog.record.state} /></dd></div></dl>}
        <label className="rc-field"><span>{actionDialog.kind === 'reconcile' ? '核对原因' : '处置原因'}</span><textarea className="rc-textarea" rows={3} maxLength={500} required disabled={uiBusy} value={reason} onChange={event => {
          setReason(event.target.value)
          setActionDialog(current => current ? { ...current, key: actionKey() } : null)
        }} placeholder="填写本次操作原因" /></label>
        {actionDialog.kind === 'ban' && <label className="rc-field"><span>追加用户备注（选填）</span><textarea className="rc-textarea" rows={3} disabled={uiBusy} value={note} onChange={event => {
          setNote(event.target.value)
          setActionDialog(current => current ? { ...current, key: actionKey() } : null)
        }} placeholder="记录复核依据、联系情况或后续处理说明" /><small className={['text-xs', [...note.trim()].length > NOTE_LIMIT ? 'text-destructive' : 'text-muted-foreground'].join(' ')}>已填写 {[...note.trim()].length} 字。追加到 NewAPI 管理员用户备注，保留原内容；原备注、风控标记与追加内容合计最多 {NOTE_LIMIT} 字，超长会提示修改。</small></label>}
        <div className="rc-dialog-actions">
          <Button variant="ghost" type="button" disabled={uiBusy} onClick={() => setActionDialog(null)}>取消</Button>
          <Button variant={actionDialog.kind === 'ban' ? 'destructive' : 'default'} type="submit" disabled={mutationBlocked || !reason.trim()}>{busy === 'action' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Check className="mr-2 h-4 w-4" />}确认{actionDialog.kind === 'ban' ? '封禁' : actionDialog.kind === 'release' ? '解除' : '核对'}</Button>
        </div>
      </form>
    </AdminDialog>}

    {noteDialog && <AdminDialog title={`NewAPI 用户备注 · #${noteDialog.user_id}`} onClose={() => { if (!uiBusy) setNoteDialog(null) }}>
      <form className="rc-dialog-form" onSubmit={saveNote}>
        <p className="text-sm leading-relaxed text-muted-foreground">读取并编辑 NewAPI 管理员后台的完整用户备注，仅管理员可见。提交空内容会清除备注，修改前后内容会保存在审计日志中。</p>
        {noteLoading && <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status"><LoaderCircle className="h-4 w-4 animate-spin" />正在读取当前用户备注…</div>}
        {dialogError && <Notice kind="error">{dialogError}</Notice>}
        <label className="rc-field"><span>用户备注（最多 {NOTE_LIMIT} 字）</span><textarea className="rc-textarea" rows={5} disabled={uiBusy || noteLoading || !noteSnapshot?.remark_supported} value={note} onChange={event => setNote(event.target.value)} placeholder="提交空内容会清除备注" /><small className={`text-xs ${[...note].length > NOTE_LIMIT ? 'text-destructive' : 'text-muted-foreground'}`}>{[...note].length} / {NOTE_LIMIT} 字 · 保存时会核对当前内容，防止覆盖其他管理员的修改。</small></label>
        <div className="rc-dialog-actions"><Button variant="outline" type="button" disabled={uiBusy || noteLoading || UI_PREVIEW_ONLY} onClick={() => setNoteRevision(value => value + 1)}><RefreshCw className="mr-2 h-4 w-4" />重新读取（覆盖当前输入）</Button><Button variant="ghost" type="button" disabled={uiBusy} onClick={() => setNoteDialog(null)}>取消</Button><Button type="submit" disabled={mutationBlocked || noteLoading || !noteSnapshot?.remark_supported || [...note].length > NOTE_LIMIT}>{busy === 'note' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}保存用户备注</Button></div>
      </form>
    </AdminDialog>}

    {logicOpen && <AdminDialog title="检测思路与执行逻辑" wide onClose={() => setLogicOpen(false)}>
      <div className="space-y-5 text-sm leading-relaxed">
        <section className="space-y-2"><h3 className="font-semibold">1. 分开采集三类证据</h3><p className="text-muted-foreground">调用日志用于分析请求频率、同令牌出口与调用基线；登录、注册来自独立探针。调用 IP 是应用或代理出口，不当作用户登录位置。代理、VPN、多个 IP 或共享 IP 不单独触发封禁。</p></section>
        <section className="space-y-3"><h3 className="font-semibold">2. 组合规则筛选可疑账号</h3><dl className="rc-detail-grid rounded-lg border bg-muted/20 p-4"><div><dt>分析窗口</dt><dd>{config ? `${count(config.window_seconds)} 秒` : '未读取'}</dd></div><div><dt>窗口最小调用样本</dt><dd>{count(config?.min_requests)}</dd></div><div><dt>单分钟调用阈值</dt><dd>{count(config?.max_requests_per_minute)} RPM</dd></div><div><dt>同分钟同令牌出口阈值</dt><dd>{count(config?.concurrent_token_ips)}</dd></div><div><dt>相较上一窗口调用增幅</dt><dd>{config ? `${config.min_burst_ratio} 倍` : '未读取'}</dd></div><div><dt>登录失败复核阈值</dt><dd>{count(config?.login_failures_threshold)}</dd></div></dl><p className="text-muted-foreground">高频与同令牌多出口必须在同一分钟同时出现，再结合足够样本及相较上一等长窗口的增幅判断。登录连续失败、新浏览器组合以及未认证用户名尝试用于人工复核；未认证尝试不等于账号本人行为。</p></section>
        <section className="space-y-2"><h3 className="font-semibold">3. AI 审核与规则结果独立保留</h3><p className="text-muted-foreground">模型只接收结构化指标、命中规则和证据说明，并返回风险意见。AI 不直接授权封禁；读取分析不调用模型，发起审核或扫描才按保存配置调用。启用 AI 时，调用失败、响应格式无效或返回未知意见会暂停该次自动处置。</p></section>
        <section className="space-y-2"><h3 className="font-semibold">关联账号与异常调用的复核链路</h3><p className="text-muted-foreground">仅成功登录或注册后确认账号的 BrowserID，以及调用日志确认的共享令牌进入关联证据；共享 IP、代理和邀请人不独立认定多账号。失败率和模型集中失败比较前一等长窗口，调用及额度样本不足时明确标为基线不足。所有新规则仅进入人工复核，不新增自动封禁资格。复核结论、理由和对应评估写入管理数据库与审计日志；误报只排除已复核的本次评估。</p></section>
        <section className="space-y-2"><h3 className="font-semibold">4. 正式处置需满足全部保护条件</h3><p className="text-muted-foreground">正式扫描、允许自动处置、完整调用历史、充足历史基线及组合规则同时成立，且账号不在白名单、不是管理员，才允许规则改变账号状态。执行前核对当前策略与证据版本，执行后读取上游状态验证；冲突和不确定结果保留核对记录，不盲目重试。</p></section>
        <section className="space-y-2"><h3 className="font-semibold">5. 各类记录的区别</h3><p className="text-muted-foreground">可疑用户是最新待复核名单；账号评估是一名账号在某个窗口的检测结论；扫描批次是一组账号的后台任务；封禁列表是当前账号状态；处置历史和审计日志用于追溯实际操作。</p></section>
      </div>
    </AdminDialog>}

    <Dialog open={scanOpen} onOpenChange={open => { if (!uiBusy) setScanOpen(open) }}>
      <DialogContent className="gap-6 sm:max-w-[600px]">
        <DialogHeader className="space-y-2">
          <DialogTitle className="flex items-center gap-2 text-lg"><Activity className="h-5 w-5 text-primary" />执行扫描</DialogTitle>
          <DialogDescription>按已保存的规则和 AI 配置评估账号，扫描结果记录到评估与处置日志。</DialogDescription>
        </DialogHeader>
        <form className="space-y-5" onSubmit={submitScan}>
          {dialogError && <Notice kind="error">{dialogError}</Notice>}
          {UI_PREVIEW_ONLY && <Notice kind="info">数据获取已暂停，执行策略未读取。</Notice>}
          <div className="rc-field">
            <label htmlFor="risk-scan-scope">账号范围</label>
            <Select id="risk-scan-scope" aria-label="账号范围" value={scanScope} disabled={uiBusy} onChange={event => setScanScope(event.target.value as 'active' | 'selected')}>
              <option value="active">分析窗口内有已归属事件的账号</option>
              <option value="selected">指定账号</option>
            </Select>
          </div>
          {scanScope === 'selected' && <label className="rc-field"><span>账号 ID</span><textarea className="rc-textarea" rows={3} maxLength={100000} required placeholder="输入账号 ID，以逗号或换行分隔" value={scanIDs} disabled={uiBusy} onChange={event => setScanIDs(event.target.value)} /></label>}
          <label className="rc-checkbox rounded-lg border bg-muted/30 p-3">
            <input type="checkbox" checked={scanDryRun} disabled={uiBusy} onChange={event => setScanDryRun(event.target.checked)} />
            <span><strong className="block font-medium text-foreground">试运行模式</strong><span className="mt-1 block text-xs">仅生成评估和处置计划，跳过账号变更</span></span>
          </label>
          <dl className="rc-detail-grid rounded-lg border bg-muted/20 p-3">
            <div><dt>分析窗口</dt><dd>{config ? `${config.window_seconds} 秒` : '—'}</dd></div>
            <div><dt>AI 辅助审核</dt><dd>{config ? config.ai.enabled ? config.ai.model || '已启用' : '未启用' : '—'}</dd></div>
            <div><dt>规则版本</dt><dd>{config ? `v${config.version}` : '—'}</dd></div>
            <div><dt>已保存自动处置</dt><dd>{config ? config.auto_execute && !config.dry_run ? '允许' : '未生效' : '—'}</dd></div>
          </dl>
          {!scanDryRun && !UI_PREVIEW_ONLY && <Notice kind="warning">{config?.auto_execute && !config.dry_run ? '正式扫描在同时满足组合规则、历史覆盖和当前策略时改变账号状态。AI 意见不单独触发封禁。' : '已保存策略未允许正式自动处置，本次扫描只生成评估及审查记录。'}</Notice>}
          <DialogFooter className="rc-scan-footer gap-2 border-t pt-4 sm:gap-2 sm:space-x-0">
            <Button type="button" variant="ghost" disabled={uiBusy} onClick={() => setScanOpen(false)}>取消</Button>
            <Button type="submit" disabled={mutationBlocked || scanScope === 'selected' && !scanIDs.trim()}>{busy === 'scan' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <Activity className="mr-2 h-4 w-4" />}开始扫描</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>

    {confirmConfig && <AdminDialog title={UI_PREVIEW_ONLY ? '规则草稿确认' : '确认启用自动处置'} onClose={() => { if (!uiBusy) setConfirmConfig(null) }}>
      <div className="rc-dialog-form">
        <Notice kind={UI_PREVIEW_ONLY ? 'info' : 'warning'}>{UI_PREVIEW_ONLY ? '数据获取已暂停，以下为尚未保存的本地配置。' : '保存后，后台扫描在满足规则及完整性条件时可自动封禁普通账号。'}</Notice>
        {dialogError && <Notice kind="error">{dialogError}</Notice>}
        <dl className="rc-detail-grid">
          <div><dt>最小调用样本</dt><dd>{count(confirmConfig.min_requests)}</dd></div><div><dt>峰值 RPM</dt><dd>{count(confirmConfig.max_requests_per_minute)}</dd></div>
          <div><dt>令牌出口阈值</dt><dd>{count(confirmConfig.concurrent_token_ips)}</dd></div><div><dt>最小基线增幅</dt><dd>{confirmConfig.min_burst_ratio} 倍</dd></div>
          {UI_PREVIEW_ONLY && <><div><dt>定时扫描</dt><dd>{confirmConfig.enabled ? '开启' : '关闭'}</dd></div><div><dt>运行模式</dt><dd>{confirmConfig.dry_run ? '试运行' : '正式运行'}</dd></div><div><dt>自动处置</dt><dd>{confirmConfig.auto_execute ? '允许' : '关闭'}</dd></div><div><dt>AI 辅助评估</dt><dd>{confirmConfig.ai.enabled ? '开启' : '关闭'}</dd></div></>}
        </dl>
        <label className="rc-checkbox"><input type="checkbox" checked={autoConfirmed} onChange={event => setAutoConfirmed(event.target.checked)} disabled={uiBusy} /><span>{UI_PREVIEW_ONLY ? '确认已检查本地草稿中的执行策略' : '确认按上述策略允许自动改变账号状态'}</span></label>
        <div className="rc-dialog-actions">
          <Button variant="ghost" type="button" disabled={uiBusy} onClick={() => setConfirmConfig(null)}>返回修改</Button>
          <Button variant="destructive" type="button" disabled={mutationBlocked || !autoConfirmed} onClick={() => void saveConfig(confirmConfig)}>{busy === 'save-config' ? <LoaderCircle className="mr-2 h-4 w-4 animate-spin" /> : <ShieldCheck className="mr-2 h-4 w-4" />}确认保存</Button>
        </div>
      </div>
    </AdminDialog>}
  </div>
}
