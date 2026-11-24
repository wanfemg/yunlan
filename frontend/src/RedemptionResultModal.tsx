// Adapted from new_api_tools (ResultModal.tsx), MIT License; see tools-reference-LICENSE.txt.
import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useToast } from './components/Toast'
import { CheckCircle, X, Copy, Download, ExternalLink } from 'lucide-react'
import { Button } from './components/ui/button'

export interface GenerateResult {
  keys: string[]
  count: number
  name?: string
  persisted?: boolean
  historySaved?: boolean
}

interface ResultModalProps {
  result: GenerateResult
  onClose: () => void
}

export function ResultModal({ result, onClose }: ResultModalProps) {
  const { showToast } = useToast()
  const modalRef = useRef<HTMLDivElement>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    const previousFocus = document.activeElement as HTMLElement | null
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    closeRef.current?.focus()
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); onClose() }
      if (e.key === 'Tab') {
        const buttons = modalRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], [tabindex="0"]')
        if (!buttons?.length) return
        const first = buttons[0]
        const last = buttons[buttons.length - 1]
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
      }
    }
    document.addEventListener('keydown', handleEscape, true)
    return () => {
      document.removeEventListener('keydown', handleEscape, true)
      document.body.style.overflow = previousOverflow
      previousFocus?.focus()
    }
  }, [onClose])

  const handleBackdropClick = (e: React.MouseEvent) => {
    if (e.target === modalRef.current) onClose()
  }

  const copyToClipboard = async (text: string, label: string) => {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text)
        showToast('success', `${label}已复制到剪贴板`)
        return
      }
      const textArea = document.createElement('textarea')
      textArea.value = text
      textArea.style.position = 'fixed'
      textArea.style.left = '-9999px'
      document.body.appendChild(textArea)
      textArea.select()
      const copied = document.execCommand('copy')
      document.body.removeChild(textArea)
      if (!copied) throw new Error('copy rejected')
      showToast('success', `${label}已复制到剪贴板`)
    } catch {
      showToast('error', '复制失败，请手动复制')
    }
  }

  const copyAllKeys = () => copyToClipboard(result.keys.join('\n'), '全部兑换码')

  const copyAndGoToLinuxDo = () => {
    void copyToClipboard(result.keys.join('\n'), '全部兑换码')
    window.open('https://cdk.linux.do/dashboard', '_blank', 'noopener,noreferrer')
  }

  const downloadKeys = () => {
    const content = result.keys.join('\n')
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${result.name || 'redemption'}_keys_${new Date().toISOString().slice(0, 10)}.txt`
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
    showToast('success', '兑换码已下载')
  }

  return createPortal(
    <div
      ref={modalRef}
      onClick={handleBackdropClick}
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4"
    >
      <div role="dialog" aria-modal="true" aria-labelledby="redemption-result-title"
        data-siteadmin-dialog-content="" data-state="open" data-dialog-layout="flex"
        className="bg-card rounded-xl shadow-2xl max-w-2xl w-full max-h-[85vh] flex flex-col overflow-hidden">
        {/* Header */}
        <div className="bg-gradient-to-r from-green-500 to-emerald-600 px-6 py-4 flex-shrink-0">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="bg-white/20 rounded-full p-2">
                <CheckCircle className="h-6 w-6 text-white" />
              </div>
              <div>
                <h2 id="redemption-result-title" className="text-xl font-bold text-white">{result.persisted === false ? '演示生成成功' : '添加成功'}</h2>
                <p className="text-green-100 text-sm">已生成 {result.count} 个兑换码</p>
              </div>
            </div>
            <button ref={closeRef} type="button" aria-label="关闭" onClick={onClose} className="text-white hover:bg-white/20 rounded-full p-2 transition-colors">
              <X className="h-5 w-5" />
            </button>
          </div>
        </div>

        {/* Main Copy Buttons */}
        <div className="px-6 py-4 border-b bg-muted/50 space-y-2 flex-shrink-0">
          {result.persisted === false && <p className="text-sm text-muted-foreground">演示兑换码已加入本地管理列表，未写入 NewAPI，不能用于兑换。</p>}
          <Button onClick={copyAllKeys} className="w-full" size="lg">
            <Copy className="h-5 w-5 mr-2" />
            复制全部兑换码
          </Button>
          <Button onClick={copyAndGoToLinuxDo} variant="outline" className="w-full" size="lg">
            <ExternalLink className="h-5 w-5 mr-2" />
            复制兑换码并前往 LINUX DO 分发站
          </Button>
        </div>

        {/* Keys List */}
        <div className="px-6 py-4 flex-1 overflow-y-auto min-h-0">
          <div className="flex items-center justify-between mb-3">
            <span className="text-sm font-medium">兑换码列表</span>
            <Button variant="ghost" size="sm" onClick={downloadKeys}>
              <Download className="h-4 w-4 mr-1" />
              下载 TXT
            </Button>
          </div>

          <div className="space-y-2">
            {result.keys.map((key, index) => (
              <div
                key={index}
                className="flex items-center justify-between bg-muted hover:bg-muted/80 rounded-lg px-4 py-2.5 group transition-colors"
              >
                <div className="flex min-w-0 items-center gap-3">
                  <span className="text-xs text-muted-foreground w-6 shrink-0">{index + 1}.</span>
                  <code className="text-sm font-mono select-all break-all">{key}</code>
                </div>
                <button
                  type="button"
                  aria-label={`复制第 ${index + 1} 个兑换码`}
                  onClick={() => copyToClipboard(key, '兑换码')}
                  className="shrink-0 opacity-100 sm:opacity-0 sm:group-hover:opacity-100 text-muted-foreground hover:text-primary p-1.5 rounded transition-all"
                >
                  <Copy className="h-4 w-4" />
                </button>
              </div>
            ))}
          </div>
        </div>

        {/* Footer */}
        <div className="px-6 py-4 bg-muted/50 border-t flex justify-between items-center flex-shrink-0">
          <p className="text-sm text-muted-foreground">{result.historySaved === true ? '提示：兑换码已自动保存到历史记录' : result.historySaved === false ? '提示：本地历史保存失败，请复制或下载兑换码' : '提示：正在保存到本地历史记录'}</p>
          <Button variant="secondary" onClick={onClose}>关闭</Button>
        </div>
      </div>
    </div>,
    document.body,
  )
}
