export class ApiError extends Error {
  status: number
  retryAfter: number | null

  constructor(message: string, status: number, retryAfter: number | null = null) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.retryAfter = retryAfter
  }
}

function readRetryAfter(response: Response) {
  const value = response.headers.get("Retry-After")
  if (!value) return null

  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.ceil(seconds)

  const date = Date.parse(value)
  if (!Number.isFinite(date)) return null
  return Math.max(0, Math.ceil((date - Date.now()) / 1000))
}

export async function api<T>(
  url: string,
  options?: RequestInit,
  behavior?: { ignoreUnauthorized?: boolean }
): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", ...options })
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as {
      error?: string
      message?: string
    } | null
    if (response.status === 401 && !behavior?.ignoreUnauthorized) {
      window.dispatchEvent(new Event("clamav-auth-expired"))
    }
    throw new ApiError(
      body?.error || body?.message || response.statusText,
      response.status,
      readRetryAfter(response)
    )
  }
  return response.json() as Promise<T>
}

export function jsonRequest(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }
}

export function sleep(milliseconds: number) {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds))
}
