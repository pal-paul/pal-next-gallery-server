import { request } from './apiClient'

export type LoginChallenge = {
  challengeToken: string
  provisioningUri?: string
  secret?: string
  authenticated?: boolean
}

export type Session = {
  id: string
  username: string
  role: 'admin' | 'user'
  applianceOwner?: boolean
}

export const authApi = {
  session: () => request<Session>('/auth/session'),
  login: (username: string, password: string) => request<LoginChallenge>('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  }),
  verify: (challengeToken: string, code: string) => request<void>('/auth/verify', {
    method: 'POST',
    body: JSON.stringify({ challengeToken, code }),
  }),
  logout: () => request<void>('/auth/logout', { method: 'POST' }),
}
