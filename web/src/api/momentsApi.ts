import type { Moment } from '../types/gallery'
import { request, resolveApiUrl } from './apiClient'

type GenerateResult = { created: number }
export type MetadataSuggestion = { title: string; description: string; confidence: number }

const normalizeMoment = (moment: Moment): Moment => ({
  ...moment,
  startTime: moment.startTime.slice(0, 10),
  endTime: moment.endTime.slice(0, 10),
  createdAt: moment.createdAt.slice(0, 10),
  coverUrl: moment.coverMediaId ? resolveApiUrl(`/media/files/${moment.coverMediaId}/thumbnail`) : undefined,
  media: moment.media?.map((item) => ({
    ...item,
    capturedAt: item.capturedAt.slice(0, 10),
    thumbnailUrl: item.thumbnailUrl ? resolveApiUrl(item.thumbnailUrl) : undefined,
  })),
})

export const momentsApi = {
  features() {
    return request<{ ai: boolean }>('/features')
  },
  async list(signal?: AbortSignal) {
    return (await request<Moment[]>('/moments', { signal })).map(normalizeMoment)
  },
  async get(momentId: string, signal?: AbortSignal) {
    return normalizeMoment(await request<Moment>(`/moments/${momentId}`, { signal }))
  },
  generate() {
    return request<GenerateResult>('/moments/generate', { method: 'POST' })
  },
  async create(title: string, description: string, mediaIds: string[]) {
    return normalizeMoment(await request<Moment>('/moments', { method: 'POST', body: JSON.stringify({ title, description, mediaIds }) }))
  },
  update(momentId: string, title: string, description: string, status: Moment['status']) {
    return request<void>(`/moments/${momentId}`, { method: 'PATCH', body: JSON.stringify({ title, description, status }) })
  },
  delete(momentId: string) {
    return request<void>(`/moments/${momentId}`, { method: 'DELETE' })
  },
  addMedia(momentId: string, mediaIds: string[]) {
    return request<void>(`/moments/${momentId}/media`, { method: 'POST', body: JSON.stringify({ mediaIds }) })
  },
  removeMedia(momentId: string, mediaId: string) {
    return request<void>(`/moments/${momentId}/media/${mediaId}`, { method: 'DELETE' })
  },
  suggestMetadata(momentId: string) {
    return request<MetadataSuggestion>(`/moments/${momentId}/metadata-suggestion`, { method: 'POST' })
  },
}
