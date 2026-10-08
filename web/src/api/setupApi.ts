import { request } from './apiClient'

type SetupRequest = {
  bootstrapUsername: string
  bootstrapPassword: string
  username: string
  password: string
  confirmPassword: string
}

export const setupApi = {
  status() {
    return request<{ required: boolean }>('/setup/status')
  },
  create(payload: SetupRequest) {
    return request<void>('/setup', { method: 'POST', body: JSON.stringify(payload) })
  },
}
