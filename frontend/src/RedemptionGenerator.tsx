// Adapted from new_api_tools (Generator.tsx), MIT License; see tools-reference-LICENSE.txt.
import { useCallback, useEffect, useRef, useState } from 'react'
import { Sparkles } from 'lucide-react'
import { ApiError, requestJSON, UI_PREVIEW_ONLY } from './api'
import { useToast } from './components/Toast'
import { GeneratorForm } from './RedemptionGeneratorForm'
import type { GenerateFormData } from './RedemptionGeneratorForm'
import { ResultModal } from './RedemptionResultModal'
import type { GenerateResult } from './RedemptionResultModal'
import { saveRedemptionHistory } from './redemption-history'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from './components/ui/card'

type GeneratedBatch = {
  keys: string[]
  count: number
  quotas: number[]
  expired_time: number
  mode: string
  persisted: boolean
}

function amountJSON(text: string): string {
  const match = /^(\d*(?:\.\d*)?)([eE][+-]?\d+)?$/.exec(text.trim())
  if (!match || !/\d/.test(match[1])) throw new Error('额度需要填写大于 0 的有效数字。')
  const [integer, fraction] = match[1].split('.')
  const whole = (integer || '0').replace(/^0+(?=\d)/, '')
  const decimal = whole + (fraction ? `.${fraction}` : '') + (match[2] || '')
  if (!Number.isFinite(Number(decimal)) || Number(decimal) <= 0) throw new Error('额度需要填写大于 0 的有效数字。')
  return decimal
}

export default function RedemptionGenerator({ tokenVersion = 0, quotaPerUnit = 500000, timezone = 'Asia/Shanghai', onUnauthorized }: {
  tokenVersion?: number
  quotaPerUnit?: number
  timezone?: string
  demoMode?: boolean
  onUnauthorized: () => void
}) {
  const { showToast } = useToast()
  const [isLoading, setIsLoading] = useState(false)
  const [result, setResult] = useState<GenerateResult | null>(null)
  const [showModal, setShowModal] = useState(false)
  const controller = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const pending = useRef(false)

  useEffect(() => {
    generation.current++
    controller.current?.abort()
    pending.current = false
    setIsLoading(false)
    setResult(null)
    setShowModal(false)
    return () => { generation.current++; controller.current?.abort() }
  }, [tokenVersion])

  const closeResult = useCallback(() => {
    setShowModal(false)
    setResult(null)
  }, [])

  const handleSubmit = async (formData: GenerateFormData) => {
    if (UI_PREVIEW_ONLY || pending.current) return
    pending.current = true
    setIsLoading(true)
    const serial = ++generation.current
    const requestController = new AbortController()
    controller.current = requestController
    try {
      const requestBody: Record<string, unknown> = {
        name: formData.name.trim(), count: formData.count, key_prefix: formData.key_prefix || '',
        quota_mode: formData.quota_mode, expire_mode: formData.expire_mode,
      }
      const amounts = formData.quota_mode === 'fixed'
        ? `"fixed_amount":${amountJSON(formData.amount_text?.fixed_amount ?? String(formData.fixed_amount))}`
        : `"min_amount":${amountJSON(formData.amount_text?.min_amount ?? String(formData.min_amount))},"max_amount":${amountJSON(formData.amount_text?.max_amount ?? String(formData.max_amount))}`
      if (formData.expire_mode === 'days') requestBody.expire_days = formData.expire_days
      // The existing backend interprets this wall time in the configured site timezone.
      if (formData.expire_mode === 'date') requestBody.expire_date = formData.expire_date
      const data = await requestJSON<GeneratedBatch>('/api/redemptions/generate', {
        // The exact decimal text reaches the backend's json.Number parser.
        method: 'POST', body: JSON.stringify(requestBody).slice(0, -1) + ',' + amounts + '}',
      }, requestController.signal)
      if (serial !== generation.current || requestController.signal.aborted) return
      if (!Array.isArray(data.keys) || !Array.isArray(data.quotas) || data.keys.length !== data.count ||
        data.quotas.length !== data.count || data.count < 1) throw new Error('生成结果不完整，请核对本次生成状态。')
      const generateResult: GenerateResult = {
        keys: data.keys, count: data.count, name: formData.name.trim(), persisted: data.persisted,
      }
      setResult(generateResult)
      setShowModal(true)
      try {
        await saveRedemptionHistory({
          id: typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : `${Date.now()}-${serial}`,
          timestamp: new Date().toISOString(), name: formData.name.trim(), count: data.count,
          keys: data.keys, quota_mode: formData.quota_mode, expire_mode: formData.expire_mode, mode: data.mode,
        })
        if (serial === generation.current) setResult(current => current ? { ...current, historySaved: true } : null)
      } catch {
        if (serial === generation.current) {
          setResult(current => current ? { ...current, historySaved: false } : null)
          showToast('error', '兑换码已生成，本地历史保存失败，请复制或下载本次兑换码。')
        }
      }
    } catch (error) {
      if (serial !== generation.current || requestController.signal.aborted) return
      if (error instanceof ApiError && error.status === 401) {
        closeResult()
        onUnauthorized()
      } else showToast('error', error instanceof Error ? error.message : '网络错误，请检查后端服务是否运行')
    } finally {
      if (serial === generation.current) { pending.current = false; setIsLoading(false) }
    }
  }

  return (
    <div className="space-y-6 animate-in fade-in duration-500">
      <div>
        <h2 className="text-3xl font-bold tracking-tight">生成器</h2>
        <p className="text-muted-foreground mt-1">批量生成新的额度兑换码</p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Sparkles className="w-5 h-5 text-primary" />
            配置参数
          </CardTitle>
          <CardDescription>
            填写以下信息以生成兑换码，生成后的兑换码可以在“兑换码管理”中查看。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <GeneratorForm key={tokenVersion} onSubmit={handleSubmit} isLoading={isLoading}
            disabled={UI_PREVIEW_ONLY} quotaPerUnit={quotaPerUnit} timezone={timezone} />
        </CardContent>
      </Card>
      {showModal && result && <ResultModal result={result} onClose={closeResult} />}
    </div>
  )
}
