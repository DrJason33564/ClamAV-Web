export class ApiError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = "ApiError"
    this.status = status
  }
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
      response.status
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
