import type { ErrorDetail } from './protocol'

export class APIError extends Error {
  constructor(public status: number, public detail: ErrorDetail) {
    super(detail.message)
  }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch('/api' + path, { ...init, cache: 'no-store' })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new APIError(response.status, body?.error || {
      code: 'http_error', message: response.status + ' ' + response.statusText,
    })
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export function command<T>(path: string, method: string, body?: unknown, signal?: AbortSignal) {
  return request<T>(path, {
    method, signal,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export const segment = (id: string) => encodeURIComponent(id)
export const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)
export const isAbort = (error: unknown) => error instanceof Error && error.name === 'AbortError'
