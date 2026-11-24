import type { DashboardData, DashboardPeriod, SystemMeta } from './types'

const API_BASE = import.meta.env.VITE_API_BASE ?? ''
export const UI_PREVIEW_ONLY = import.meta.env.VITE_UI_PREVIEW_ONLY === 'true'
export const AUTH_EXPIRED_EVENT = 'siteadmin:auth-expired'

let csrfToken = ''
let authRevision = 0
let unauthorizedRevision = -1

export function setCSRFToken(value?: string) {
  const next = value ?? ''
  if (next !== csrfToken) {
    csrfToken = next
    authRevision++
    unauthorizedRevision = -1
  }
}

export function getCSRFToken() { return csrfToken }
export function getAuthRevision() { return String(authRevision) }
function ensureCurrentAuthentication(revision: string) {
  if (revision !== getAuthRevision()) throw new DOMException('登录身份已变化，旧请求已取消。', 'AbortError')
}

export function notifyUnauthorized(status: number, path = '', requestRevision?: string) {
  if (status !== 401 || path.startsWith('/api/auth/') || unauthorizedRevision === authRevision ||
    requestRevision != null && requestRevision !== getAuthRevision()) return
  unauthorizedRevision = authRevision
  window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT))
}

export function authenticatedHeaders(initial?: HeadersInit, method = 'GET') {
  const headers = new Headers(initial)
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase()) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }
  return headers
}

export class ApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message)
    this.name = 'ApiError'
  }
}

export async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  if (UI_PREVIEW_ONLY) throw new ApiError('数据获取已暂停', 0)
  const requestRevision = getAuthRevision()
  const response = await fetch(`${API_BASE}${path}`, {
    credentials: 'same-origin',
    cache: 'no-store',
    signal,
  })
  if (!response.ok) {
    ensureCurrentAuthentication(requestRevision)
    notifyUnauthorized(response.status, path, requestRevision)
    const payload = await response.json().catch(() => null) as { error?: string } | null
    throw new ApiError(payload?.error ?? `请求失败 (${response.status})`, response.status)
  }
  const payload = await response.json() as T
  ensureCurrentAuthentication(requestRevision)
  return payload
}

export const getMeta = () => get<SystemMeta>('/api/meta')
export const getDashboard = (period: DashboardPeriod) =>
  get<DashboardData>(`/api/dashboard?period=${period}`)

export async function requestJSON<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  if (UI_PREVIEW_ONLY) throw new ApiError('数据获取已暂停', 0)
  const requestRevision = getAuthRevision()
  const headers = authenticatedHeaders(init.headers, init.method ?? 'GET')
  if (typeof init.body === 'string' && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(`${API_BASE}${path}`, {
    ...init, credentials: 'same-origin', headers, signal: signal ?? init.signal, cache: 'no-store', redirect: 'error',
  })
  const payload = await response.json().catch(() => null) as {
    success?: boolean; data?: T; error?: string; message?: string
  } | null
  ensureCurrentAuthentication(requestRevision)
  if (!response.ok || payload?.success === false) {
    notifyUnauthorized(response.status, path, requestRevision)
    throw new ApiError(payload?.error || payload?.message || `请求失败 (${response.status})`, response.status)
  }
  if (!payload || payload.success !== true || !('data' in payload)) {
    throw new ApiError('服务器返回了无法识别的数据', response.status)
  }
  return payload.data as T
}

export function actionKey() {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')
}

export async function downloadCSV(path: string, filename: string, signal?: AbortSignal) {
  if (UI_PREVIEW_ONLY) throw new ApiError('数据获取已暂停', 0)
  const requestRevision = getAuthRevision()
  const response = await fetch(`${API_BASE}${path}`, {
    credentials: 'same-origin',
    signal, cache: 'no-store', redirect: 'error',
  })
  if (!response.ok) {
    ensureCurrentAuthentication(requestRevision)
    notifyUnauthorized(response.status, path, requestRevision)
    const error = await response.json().catch(() => null) as { error?: string } | null
    throw new ApiError(error?.error || `导出失败 (${response.status})`, response.status)
  }
  if (!response.headers.get('Content-Type')?.includes('text/csv')) throw new ApiError('导出文件格式不正确', response.status)
  const blob = await response.blob()
  ensureCurrentAuthentication(requestRevision)
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
