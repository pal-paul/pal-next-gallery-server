import type { Moment } from '../types/gallery'
import { request, resolveApiUrl } from './apiClient'

type GenerateResult = { created: number }

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
  async list(signal?: AbortSignal) {
    return (await request<Moment[]>('/moments', { signal })).map(normalizeMoment)
  },
  async get(momentId: string, signal?: AbortSignal) {
    return normalizeMoment(await request<Moment>(`/moments/${momentId}`, { signal }))
  },
  generate() {
    return request<GenerateResult>('/moments/generate', { method: 'POST' })
  },
}
