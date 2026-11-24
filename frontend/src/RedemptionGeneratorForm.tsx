// Adapted from new_api_tools (GeneratorForm.tsx), MIT License; see tools-reference-LICENSE.txt.
import { useState } from 'react'
import type { FormEvent } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from './components/ui/button'

export type QuotaMode = 'fixed' | 'random'
export type ExpireMode = 'never' | 'days' | 'date'
type AmountField = 'fixed_amount' | 'min_amount' | 'max_amount'

export interface GenerateFormData {
  name: string
  count: number
  key_prefix: string
  quota_mode: QuotaMode
  fixed_amount: number
  min_amount: number
  max_amount: number
  expire_mode: ExpireMode
  expire_days: number
  expire_date: string
  amount_text?: Partial<Record<AmountField, string>>
}

interface GeneratorFormProps {
  onSubmit: (data: GenerateFormData) => void
  isLoading: boolean
  disabled?: boolean
  quotaPerUnit?: number
  timezone?: string
}

const defaultFormData: GenerateFormData = {
  name: '',
  count: 1,
  key_prefix: '',
  quota_mode: 'fixed',
  fixed_amount: 1,
  min_amount: 1,
  max_amount: 10,
  expire_mode: 'never',
  expire_days: 30,
  expire_date: '',
}

export function GeneratorForm({ onSubmit, isLoading, disabled = false, quotaPerUnit = 500000, timezone = 'Asia/Shanghai' }: GeneratorFormProps) {
  const [formData, setFormData] = useState<GenerateFormData>(defaultFormData)
  const [errors, setErrors] = useState<Partial<Record<keyof GenerateFormData, string>>>({})

  const validateForm = (): boolean => {
    const newErrors: Partial<Record<keyof GenerateFormData, string>> = {}

    if (!formData.name.trim()) newErrors.name = '请输入兑换码名称'
    else if ([...formData.name.trim()].length > 20) newErrors.name = '兑换码名称最多 20 个字符'
    if (!Number.isInteger(formData.count) || formData.count < 1 || formData.count > 1000) newErrors.count = '数量必须在 1-1000 之间'
    if (new TextEncoder().encode(formData.key_prefix).length > 20) newErrors.key_prefix = '前缀最多 20 字节'
    else if (/[\s\u0000-\u001f]/.test(formData.key_prefix)) newErrors.key_prefix = '前缀不能包含空白或控制字符'

    if (formData.quota_mode === 'fixed') {
      if (formData.fixed_amount <= 0) newErrors.fixed_amount = '固定额度必须大于 0'
    } else {
      if (formData.min_amount <= 0) newErrors.min_amount = '最小额度必须大于 0'
      if (formData.max_amount <= 0) newErrors.max_amount = '最大额度必须大于 0'
      if (formData.min_amount > formData.max_amount) newErrors.max_amount = '最大额度必须大于等于最小额度'
    }

    if (formData.expire_mode === 'days' && formData.expire_days < 1) {
      newErrors.expire_days = '过期天数必须大于 0'
    } else if (formData.expire_mode === 'date') {
      if (!formData.expire_date) newErrors.expire_date = '请选择过期日期'
      else {
        const parts = Object.fromEntries(new Intl.DateTimeFormat('en-CA', {
          timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit',
          hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
        }).formatToParts(new Date()).map(part => [part.type, part.value]))
        const siteNow = `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`
        if (formData.expire_date <= siteNow) newErrors.expire_date = '过期日期必须在未来'
      }
    }

    setErrors(newErrors)
    return Object.keys(newErrors).length === 0
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (isLoading || disabled) return
    if (validateForm()) onSubmit(formData)
  }

  const updateField = <K extends keyof GenerateFormData>(field: K, value: GenerateFormData[K]) => {
    setFormData((prev) => ({ ...prev, [field]: value }))
    if (errors[field]) setErrors((prev) => ({ ...prev, [field]: undefined }))
  }

  const updateAmount = (field: AmountField, text: string) => {
    setFormData(prev => ({
      ...prev, [field]: parseFloat(text) || 0,
      amount_text: { ...prev.amount_text, [field]: text },
    }))
    if (errors[field]) setErrors(prev => ({ ...prev, [field]: undefined }))
  }

  const inputClass = (hasError: boolean) =>
    `w-full px-3 py-2 border rounded-lg bg-background focus:ring-2 focus:ring-primary focus:border-primary transition-colors ${
      hasError ? 'border-destructive' : 'border-input'
    }`

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      {/* 基本信息 */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium mb-1">
            兑换码名称 <span className="text-destructive">*</span>
          </label>
          <input
            type="text"
            value={formData.name}
            onChange={(e) => updateField('name', e.target.value)}
            placeholder="例如: 新用户福利"
            className={inputClass(!!errors.name)}
          />
          {errors.name && <p className="mt-1 text-sm text-destructive">{errors.name}</p>}
        </div>

        <div>
          <label className="block text-sm font-medium mb-1">
            生成数量 <span className="text-destructive">*</span>
          </label>
          <input
            type="number"
            min={1}
            max={1000}
            value={formData.count}
            onChange={(e) => updateField('count', parseInt(e.target.value) || 1)}
            className={inputClass(!!errors.count)}
          />
          {errors.count && <p className="mt-1 text-sm text-destructive">{errors.count}</p>}
        </div>
      </div>

      {/* 前缀 */}
      <div>
        <label className="block text-sm font-medium mb-1">Key 前缀 (可选)</label>
        <input
          type="text"
          value={formData.key_prefix}
          onChange={(e) => updateField('key_prefix', e.target.value)}
          placeholder="例如: VIP"
          maxLength={20}
          className={inputClass(!!errors.key_prefix)}
        />
        <p className="mt-1 text-xs text-muted-foreground">最多 20 个字符，将作为兑换码的前缀</p>
        {errors.key_prefix && <p className="mt-1 text-sm text-destructive">{errors.key_prefix}</p>}
      </div>

      {/* 额度模式 */}
      <div>
        <label className="block text-sm font-medium mb-2">额度模式</label>
        <div className="flex gap-4 mb-3">
          {(['fixed', 'random'] as const).map((mode) => (
            <label key={mode} className="flex items-center gap-2 cursor-pointer">
              <input
                type="radio"
                name="quota_mode"
                value={mode}
                checked={formData.quota_mode === mode}
                onChange={() => updateField('quota_mode', mode)}
                className="text-primary focus:ring-primary"
              />
              <span className="text-sm">{mode === 'fixed' ? '固定额度' : '随机额度'}</span>
            </label>
          ))}
        </div>

        {formData.quota_mode === 'fixed' ? (
          <div>
            <label className="block text-sm font-medium mb-1">固定额度 (USD)</label>
            <input
              type="number"
              min={0.01}
              step={0.01}
              value={formData.amount_text?.fixed_amount ?? formData.fixed_amount}
              onChange={(e) => updateAmount('fixed_amount', e.target.value)}
              className={inputClass(!!errors.fixed_amount)}
            />
            <p className="mt-1 text-xs text-muted-foreground">1 USD = {quotaPerUnit.toLocaleString('en-US')} Token</p>
            {errors.fixed_amount && <p className="mt-1 text-sm text-destructive">{errors.fixed_amount}</p>}
          </div>
        ) : (
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-sm font-medium mb-1">最小额度 (USD)</label>
              <input
                type="number"
                min={0.01}
                step={0.01}
                value={formData.amount_text?.min_amount ?? formData.min_amount}
                onChange={(e) => updateAmount('min_amount', e.target.value)}
                className={inputClass(!!errors.min_amount)}
              />
              {errors.min_amount && <p className="mt-1 text-sm text-destructive">{errors.min_amount}</p>}
            </div>
            <div>
              <label className="block text-sm font-medium mb-1">最大额度 (USD)</label>
              <input
                type="number"
                min={0.01}
                step={0.01}
                value={formData.amount_text?.max_amount ?? formData.max_amount}
                onChange={(e) => updateAmount('max_amount', e.target.value)}
                className={inputClass(!!errors.max_amount)}
              />
              {errors.max_amount && <p className="mt-1 text-sm text-destructive">{errors.max_amount}</p>}
            </div>
          </div>
        )}
      </div>

      {/* 过期模式 */}
      <div>
        <label className="block text-sm font-medium mb-2">过期模式</label>
        <div className="flex flex-wrap gap-4 mb-3">
          {([['never', '永不过期'], ['days', '指定天数'], ['date', '指定日期']] as const).map(([mode, label]) => (
            <label key={mode} className="flex items-center gap-2 cursor-pointer">
              <input
                type="radio"
                name="expire_mode"
                value={mode}
                checked={formData.expire_mode === mode}
                onChange={() => updateField('expire_mode', mode)}
                className="text-primary focus:ring-primary"
              />
              <span className="text-sm">{label}</span>
            </label>
          ))}
        </div>

        {formData.expire_mode === 'days' && (
          <div>
            <label className="block text-sm font-medium mb-1">过期天数</label>
            <input
              type="number"
              min={1}
              value={formData.expire_days}
              onChange={(e) => updateField('expire_days', parseInt(e.target.value) || 1)}
              className={inputClass(!!errors.expire_days)}
            />
            {errors.expire_days && <p className="mt-1 text-sm text-destructive">{errors.expire_days}</p>}
          </div>
        )}

        {formData.expire_mode === 'date' && (
          <div>
            <label className="block text-sm font-medium mb-1">过期日期</label>
            <input
              type="datetime-local"
              title={`站点时区：${timezone}`}
              value={formData.expire_date}
              onChange={(e) => updateField('expire_date', e.target.value)}
              className={inputClass(!!errors.expire_date)}
            />
            {errors.expire_date && <p className="mt-1 text-sm text-destructive">{errors.expire_date}</p>}
          </div>
        )}
      </div>

      {/* 提交按钮 */}
      <div className="pt-4">
        <Button type="submit" disabled={isLoading || disabled} className="w-full" size="lg">
          {isLoading ? (
            <>
              <Loader2 className="h-5 w-5 mr-2 animate-spin" />
              添加中...
            </>
          ) : (
            '添加兑换码'
          )}
        </Button>
      </div>
    </form>
  )
}
