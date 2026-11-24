import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { CheckCircle2, Download, FilePenLine, History, Loader2, RefreshCw, Save, ShieldCheck } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { AdminDialog, Notice } from './ModuleUI'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'

export type IPAttributeInfo = {
  ip: string; address_class?: string; country_code?: string; country?: string
  region?: string; city?: string; asn?: number; as_org?: string; isp?: string
  network_type: string; proxy: Record<string, string>; data_source: string; updated_at: number
}
type IPAttributeAudit = {
  id: string; ip: string; at: number; reason: string; operator: string
  before: IPAttributeInfo | null; after: IPAttributeInfo | null
}
type IPAttributeEnvelope = { attributes: IPAttributeInfo | null; revision: string; audit: IPAttributeAudit[] }
type Draft = {
  country_code: string; country: string; region: string; city: string
  asn: string; as_org: string; isp: string; network_type: string
  data_source: string; proxy: Record<string, string>
}

const proxyFields = [
  ['vpn', 'VPN'], ['public_proxy', '公共代理'], ['residential_proxy', '住宅代理'], ['tor', 'Tor 出口'],
] as const
const networkNames: Record<string, string> = {
  '': '保留自动判断', unknown: '未知', residential: '住宅网络', mobile: '移动网络',
  hosting: '机房 / 托管网络', corporate: '企业网络', non_residential: '非住宅网络',
}
const attributeNames: Record<string, string> = {
  country_code: '国家代码', country: '国家 / 地区', region: '省 / 州', city: '城市',
  asn: 'ASN', as_org: '所属组织', isp: '运营商', network_type: '网络类型', data_source: '证据来源',
}
const proxyNames: Record<string, string> = { '': '保留自动判断', yes: '是', no: '否', unknown: '未知' }

function toDraft(info: IPAttributeInfo | null): Draft {
  return {
    country_code: info?.country_code ?? '', country: info?.country ?? '',
    region: info?.region ?? '', city: info?.city ?? '', asn: info?.asn ? String(info.asn) : '',
    as_org: info?.as_org ?? '', isp: info?.isp ?? '', network_type: info?.network_type ?? '',
    data_source: info?.data_source ?? '',
    proxy: Object.fromEntries(proxyFields.map(([key]) => [key, info?.proxy?.[key] ?? ''])),
  }
}

function shownTime(value?: number) {
  return value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '暂无记录'
}

function changedFields(record: IPAttributeAudit) {
  const changes: { label: string; before: string; after: string }[] = []
  for (const [key, label] of Object.entries(attributeNames)) {
    const field = key as keyof IPAttributeInfo
    const before = record.before?.[field]
    const after = record.after?.[field]
    if (String(before ?? '') === String(after ?? '')) continue
    changes.push({
      label,
      before: key === 'network_type' ? networkNames[String(before ?? '')] ?? String(before) : String(before || '未补录'),
      after: key === 'network_type' ? networkNames[String(after ?? '')] ?? String(after) : String(after || '未补录'),
    })
  }
  for (const [key, label] of proxyFields) {
    const before = record.before?.proxy?.[key] ?? ''
    const after = record.after?.proxy?.[key] ?? ''
    if (before !== after) changes.push({ label, before: proxyNames[before] ?? before, after: proxyNames[after] ?? after })
  }
  return changes
}

function exportAudit(ip: string, records: IPAttributeAudit[]) {
  const cell = (value: string | number) => {
    const text = String(value)
    return `"${(/^[=+\-@\t\r]/.test(text) ? "'" + text : text).replaceAll('"', '""')}"`
  }
  const rows = [
    ['记录ID', 'IP', '时间', '操作人', '变更原因', '变更前', '变更后'],
    ...records.map(record => [record.id, record.ip, shownTime(record.at), record.operator, record.reason,
      JSON.stringify(record.before) ?? '', JSON.stringify(record.after) ?? '']),
  ]
  const blob = new Blob(['\uFEFF' + rows.map(row => row.map(cell).join(',')).join('\r\n')], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `IP属性变更-${ip.replaceAll(':', '-')}.csv`
  link.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export default function IPAttributeEditor({ ip, onSaved, onUnauthorized }: {
  ip: string; onSaved?: () => void; onUnauthorized: () => void
}) {
  const [open, setOpen] = useState(false)
  const [view, setView] = useState('attributes')
  const [data, setData] = useState<IPAttributeEnvelope | null>(null)
  const [draft, setDraft] = useState<Draft>(() => toDraft(null))
  const [reason, setReason] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [conflict, setConflict] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const latestIP = useRef(ip)
  const unauthorized = useRef(onUnauthorized)
  latestIP.current = ip
  unauthorized.current = onUnauthorized

  useEffect(() => {
    if (!open || UI_PREVIEW_ONLY) return
    const abort = new AbortController()
    setLoading(true)
    setData(null)
    setDraft(toDraft(null))
    setReason('')
    setError('')
    setSuccess('')
    setConflict(false)
    void requestJSON<IPAttributeEnvelope>(`/api/ip/attributes?ip=${encodeURIComponent(ip.trim())}`, {}, abort.signal)
      .then(result => {
        if (abort.signal.aborted) return
        setData(result)
        setDraft(toDraft(result.attributes))
      })
      .catch(cause => {
        if (abort.signal.aborted) return
        if (cause instanceof ApiError && cause.status === 401) unauthorized.current()
        else setError(cause instanceof Error ? cause.message : '读取人工 IP 属性失败')
      })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [open, ip, refresh])

  function field(name: Exclude<keyof Draft, 'proxy'>, value: string) {
    setDraft(current => ({ ...current, [name]: value }))
    setSuccess('')
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (!data || loading || saving || conflict || UI_PREVIEW_ONLY) return
    const targetIP = ip
    const countryCode = draft.country_code.trim().toUpperCase()
    if (countryCode && !/^[A-Z]{2}$/.test(countryCode)) {
      setError('国家代码使用两个英文字母，例如 CN、US。')
      return
    }
    if (!countryCode && [draft.country, draft.region, draft.city].some(value => value.trim())) {
      setError('补录归属地时必须同时填写国家代码。')
      return
    }
    if (countryCode && !draft.country.trim()) {
      setError('补录国家代码时同时填写国家 / 地区名称。')
      return
    }
    const asn = draft.asn.trim() ? Number(draft.asn) : 0
    if (!Number.isInteger(asn) || asn < 0 || asn > 4294967295) {
      setError('ASN 使用 1–4294967295 的整数；留空表示不覆盖。')
      return
    }
    if (!draft.data_source.trim() || !reason.trim()) {
      setError('填写证据来源和变更原因后保存。')
      return
    }
    const proxy = Object.fromEntries(Object.entries(draft.proxy).filter(([, value]) => value !== ''))
    setSaving(true)
    setError('')
    setSuccess('')
    try {
      const result = await requestJSON<IPAttributeEnvelope>('/api/ip/attributes', {
        method: 'POST',
        body: JSON.stringify({
          ip: targetIP.trim(), country_code: countryCode, country: draft.country.trim(),
          region: draft.region.trim(), city: draft.city.trim(), asn, as_org: draft.as_org.trim(),
          isp: draft.isp.trim(), network_type: draft.network_type, proxy,
          data_source: draft.data_source.trim(), reason: reason.trim(), expected_revision: data.revision,
        }),
      })
      if (latestIP.current !== targetIP) return
      setData(current => ({
        ...result,
        audit: [...result.audit, ...(current?.audit ?? [])].filter((record, index, records) =>
          records.findIndex(candidate => candidate.id === record.id) === index).slice(0, 50),
      }))
      setDraft(toDraft(result.attributes))
      setReason('')
      setSuccess('IP 属性已保存，相关 IP 查询和分析会读取本次修正。')
      try {
        const latest = await requestJSON<IPAttributeEnvelope>(`/api/ip/attributes?ip=${encodeURIComponent(targetIP.trim())}`)
        if (latestIP.current !== targetIP) return
        setData(latest)
        setDraft(toDraft(latest.attributes))
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 401) unauthorized.current()
        else setError('属性已保存，但完整变更记录刷新失败；重新读取可核实最新记录。')
      }
      onSaved?.()
    } catch (cause) {
      if (latestIP.current !== targetIP) return
      if (cause instanceof ApiError && cause.status === 401) unauthorized.current()
      else if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true)
        setError('该 IP 的人工属性已被其他操作更新。重新加载最新版本后核对修改，系统已阻止覆盖。')
      } else setError(cause instanceof Error ? cause.message : '保存 IP 属性失败')
    } finally {
      setSaving(false)
    }
  }

  return <>
    <Button variant="outline" size="sm" type="button" disabled={UI_PREVIEW_ONLY || !ip.trim()}
      onClick={() => { setView('attributes'); setOpen(true) }}>
      <FilePenLine className="mr-2 h-4 w-4" />补录 / 修正属性
    </Button>
    {open && <AdminDialog title="IP 属性补录与变更记录" wide onClose={() => { if (!saving) setOpen(false) }}>
      <div className="space-y-5">
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-muted/35 p-4">
          <div className="min-w-0">
            <p className="break-all font-mono text-lg font-semibold">{ip}</p>
            <p className="mt-1 text-xs text-muted-foreground">{data?.attributes ? `最近补录：${shownTime(data.attributes.updated_at)}` : '尚无人工补录，使用数据库与情报源自动判断'}</p>
          </div>
          <Badge variant="outline"><ShieldCheck className="mr-1 h-3.5 w-3.5" />人工复核</Badge>
        </div>
        {error && <Notice kind="error">{error}</Notice>}
        {!loading && !data && <Button variant="outline" size="sm" type="button" onClick={() => setRefresh(value => value + 1)}><RefreshCw className="mr-2 h-4 w-4" />重新读取属性</Button>}
        {success && <div role="status" className="flex items-start gap-2 rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300"><CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0" />{success}</div>}
        <Tabs value={view} onValueChange={setView}>
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="attributes" className="gap-2"><FilePenLine className="h-4 w-4" />属性补录</TabsTrigger>
            <TabsTrigger value="history" className="gap-2"><History className="h-4 w-4" />变更记录 {data?.audit.length ? `(${data.audit.length})` : ''}</TabsTrigger>
          </TabsList>
          <TabsContent value="attributes" className="mt-5">
            {loading ? <div className="flex items-center justify-center gap-2 py-16 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />读取人工属性…</div>
              : data && <form onSubmit={event => void save(event)} className="space-y-5">
                <p className="rounded-lg border bg-muted/20 p-3 text-xs leading-relaxed text-muted-foreground">仅显示已人工补录的值。未补录的属性组保留自动判断；归属地四项作为一组修正，省份和城市留空表示未核实。代理状态需有证据再填写“是 / 否”。</p>
                <fieldset disabled={saving || conflict} className="space-y-5 disabled:opacity-65">
                  <div className="space-y-3">
                    <h3 className="text-sm font-semibold">归属地</h3>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <label className="grid gap-1.5 text-sm"><span>国家代码</span><Input value={draft.country_code} maxLength={2} placeholder="CN" onChange={event => field('country_code', event.target.value.toUpperCase())} /></label>
                      <label className="grid gap-1.5 text-sm"><span>国家 / 地区</span><Input value={draft.country} maxLength={128} placeholder="中国" onChange={event => field('country', event.target.value)} /></label>
                      <label className="grid gap-1.5 text-sm"><span>省 / 州</span><Input value={draft.region} maxLength={128} placeholder="留空表示未核实" onChange={event => field('region', event.target.value)} /></label>
                      <label className="grid gap-1.5 text-sm"><span>城市</span><Input value={draft.city} maxLength={128} placeholder="留空表示未核实" onChange={event => field('city', event.target.value)} /></label>
                    </div>
                  </div>
                  <div className="space-y-3 border-t pt-5">
                    <h3 className="text-sm font-semibold">网络属性</h3>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <label className="grid gap-1.5 text-sm"><span>ASN</span><Input value={draft.asn} type="number" min={1} max={4294967295} step={1} inputMode="numeric" placeholder="例如 13335" onChange={event => field('asn', event.target.value)} /></label>
                      <label className="grid gap-1.5 text-sm"><span>所属组织</span><Input value={draft.as_org} maxLength={256} placeholder="组织名称" onChange={event => field('as_org', event.target.value)} /></label>
                      <label className="grid gap-1.5 text-sm"><span>运营商</span><Input value={draft.isp} maxLength={256} placeholder="运营商名称" onChange={event => field('isp', event.target.value)} /></label>
                      <label className="grid gap-1.5 text-sm"><span>网络类型</span><Select value={draft.network_type} onChange={event => field('network_type', event.target.value)}>{Object.entries(networkNames).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</Select></label>
                    </div>
                    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                      {proxyFields.map(([key, label]) => <label key={key} className="grid gap-1.5 text-sm"><span>{label}</span><Select value={draft.proxy[key] ?? ''} onChange={event => setDraft(current => ({ ...current, proxy: { ...current.proxy, [key]: event.target.value } }))}>{Object.entries(proxyNames).map(([value, name]) => <option key={value} value={value}>{name}</option>)}</Select></label>)}
                    </div>
                  </div>
                  <div className="grid gap-3 border-t pt-5">
                    <label className="grid gap-1.5 text-sm"><span>证据来源 <span className="text-destructive">*</span></span><Input required value={draft.data_source} maxLength={128} placeholder="例如：运营商资料核验、用户提供的网络信息" onChange={event => field('data_source', event.target.value)} /></label>
                    <label className="grid gap-1.5 text-sm"><span>变更原因 <span className="text-destructive">*</span></span><textarea required rows={3} maxLength={500} value={reason} className="w-full resize-y rounded-lg border bg-background px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring" placeholder="写明核查依据、修正内容与原因" onChange={event => setReason(event.target.value)} /></label>
                  </div>
                </fieldset>
                <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
                  <Button variant="outline" type="button" disabled={loading || saving} onClick={() => setRefresh(value => value + 1)}><RefreshCw className="mr-2 h-4 w-4" />{conflict ? '重新加载最新版本' : '重新加载'}</Button>
                  <Button type="submit" disabled={saving || conflict || !reason.trim() || !draft.data_source.trim()}>{saving ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}{saving ? '保存中…' : '保存修正'}</Button>
                </div>
              </form>}
          </TabsContent>
          <TabsContent value="history" className="mt-5 space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-xs text-muted-foreground">保存人工修改前后快照、原因与时间</p>
              <Button variant="outline" size="sm" type="button" disabled={!data?.audit.length} onClick={() => data && exportAudit(ip, data.audit)}><Download className="mr-2 h-4 w-4" />导出变更记录</Button>
            </div>
            {data?.audit.length ? <div className="scroll-region scroll-region--lg space-y-3">
              {data.audit.map(record => <article key={record.id} className="rounded-xl border bg-card p-4">
                <div className="flex flex-wrap items-center justify-between gap-2"><span className="text-sm font-medium">{shownTime(record.at)}</span><Badge variant="secondary">{record.operator || 'admin'}</Badge></div>
                <p className="mt-2 break-words text-sm leading-relaxed">{record.reason}</p>
                <div className="mt-3 divide-y rounded-lg border bg-muted/20">
                  {changedFields(record).map(change => <div key={change.label} className="grid grid-cols-[5rem_minmax(0,1fr)] gap-2 p-2.5 text-xs sm:grid-cols-[6rem_minmax(0,1fr)_minmax(0,1fr)]">
                    <span className="text-muted-foreground">{change.label}</span>
                    <span className="break-words text-muted-foreground"><span className="sm:hidden">原：</span>{change.before}</span>
                    <span className="col-start-2 break-words font-medium text-foreground sm:col-start-auto"><span className="sm:hidden">现：</span>{change.after}</span>
                  </div>)}
                </div>
              </article>)}
            </div> : <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">{loading ? '读取变更记录…' : '暂无人工属性变更记录'}</div>}
          </TabsContent>
        </Tabs>
      </div>
    </AdminDialog>}
  </>
}
