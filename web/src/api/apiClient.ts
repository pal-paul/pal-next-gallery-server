const tailscaleHostname = import.meta.env.ENV_TS_HOSTNAME?.replace(/^https?:\/\//, '').replace(/\/$/, '')
const pageProtocol = globalThis.location.protocol === 'https:' ? 'https:' : 'http:'
const productionBase = import.meta.env.BASE_URL.replace(/\/$/, '')
const apiBase = import.meta.env.ENV_API_URL ?? (import.meta.env.DEV
  ? (tailscaleHostname ? `https://${tailscaleHostname}` : `${pageProtocol}//${globalThis.location.hostname}:8081`)
  : productionBase)

export const demoMode = import.meta.env.MODE === 'demo'

export class ApiError extends Error {
  status: number

  constructor(status: number) {
    super(`Gallery API returned ${status}`)
    this.status = status
  }
}

export const request = async <T>(path: string, init?: RequestInit): Promise<T> => {
	const headers = init?.body instanceof FormData ? init.headers : { 'Content-Type': 'application/json', ...init?.headers }
  const response = await fetch(`${apiBase}${path}`, {
    ...init,
    credentials: 'include',
    headers,
  })
  if (!response.ok) throw new ApiError(response.status)
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const resolveApiUrl = (path: string) => path.startsWith('http') ? path : `${apiBase}${path}`
