import type { Album, GalleryID, MediaItem, StorageStats } from '../types/gallery'
import { request, resolveApiUrl } from './apiClient'
import type { MetadataSuggestion } from './momentsApi'

type ApiMedia = {
  id: GalleryID
  title: string
  kind: 'photo' | 'video'
  favorite?: boolean
  createdAt: string
  deletedAt?: string
  fileName?: string
  fileSize?: number
  thumbnailUrl?: string
  width?: number
  height?: number
  durationSeconds?: number
  latitude?: number
  longitude?: number
  ownerUsername?: string
  permission?: 'owner' | 'read' | 'write'
}
type ApiAlbum = Omit<Album, 'createdAt' | 'mediaIds'> & { createdAt: string; media?: ApiMedia[] }
type ApiStorage = Pick<StorageStats, 'totalBytes' | 'photoBytes' | 'videoBytes' | 'photoCount' | 'videoCount'>
type AlbumMetadataJob = {
  id: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  result?: MetadataSuggestion
  error?: string
}

const albumMetadataJobs = new Map<GalleryID, string>()

const normalizeAlbum = (album: ApiAlbum, mediaIds: GalleryID[] = []): Album => ({
  ...album,
  createdAt: album.createdAt.slice(0, 10),
  mediaIds,
  coverUrl: album.coverMediaId ? resolveApiUrl(`/media/files/${album.coverMediaId}/thumbnail`) : undefined,
})

const normalizeMedia = (media: ApiMedia): MediaItem => ({
  ...media,
  permission: media.permission ?? 'owner',
  createdAt: media.createdAt.slice(0, 10),
  tags: [],
  duration: media.durationSeconds ? `${Math.floor(media.durationSeconds / 60)}:${String(media.durationSeconds % 60).padStart(2, '0')}` : undefined,
  url: resolveApiUrl(`/media/files/${media.id}/download`),
  thumbnailUrl: media.thumbnailUrl ? resolveApiUrl(media.thumbnailUrl) : undefined,
})

export const galleryApi = {
  async loadAlbums(signal?: AbortSignal) {
    const albums = await request<ApiAlbum[]>('/albums', { signal })
    return albums.map((album) => normalizeAlbum(album))
  },
  async loadMedia(signal?: AbortSignal) {
    return (await request<ApiMedia[]>('/media', { signal })).map(normalizeMedia)
  },
  async loadAlbum(albumId: GalleryID, signal?: AbortSignal) {
    const album = await request<ApiAlbum>(`/albums/${albumId}`, { signal })
    const media = (album.media ?? []).map(normalizeMedia)
    return {
      album: normalizeAlbum(album, media.map((item) => item.id)),
      media,
    }
  },
  async createAlbum(title: string) {
    const album = await request<ApiAlbum>('/albums', { method: 'POST', body: JSON.stringify({ title }) })
    return { ...album, createdAt: album.createdAt.slice(0, 10), mediaIds: [] } satisfies Album
  },
  async loadTrash() {
    return (await request<ApiMedia[]>('/media?trash=true')).map(normalizeMedia)
  },
  updateAlbum(albumId: GalleryID, title: string, description: string) {
    return request<void>(`/albums/${albumId}`, { method: 'PATCH', body: JSON.stringify({ title, description }) })
  },
  async suggestAlbumMetadata(albumId: GalleryID, signal?: AbortSignal) {
    signal?.throwIfAborted()
    const path = `/albums/${albumId}/metadata-suggestion`
    const outstandingJobId = albumMetadataJobs.get(albumId)
    let job = outstandingJobId
      ? await request<AlbumMetadataJob>(`${path}/${outstandingJobId}`, { signal })
      : await request<AlbumMetadataJob>(path, { method: 'POST' })
    albumMetadataJobs.set(albumId, job.id)
    for (let attempt = 0; attempt <= 300; attempt += 1) {
      signal?.throwIfAborted()
      if (job.status === 'completed') {
        if (!job.result) throw new Error('The completed suggestion has no result.')
        albumMetadataJobs.delete(albumId)
        return job.result
      }
      if (job.status === 'failed') {
        albumMetadataJobs.delete(albumId)
        throw new Error(job.error || 'Unable to generate album metadata.')
      }
      if (attempt === 300) break
      await new Promise((resolve) => setTimeout(resolve, 2000))
      signal?.throwIfAborted()
      job = await request<AlbumMetadataJob>(`${path}/${job.id}`, { signal })
    }
    throw new Error('The suggestion is still processing. Try again later to retrieve it.')
  },
  reorderAlbums(albumIds: GalleryID[]) {
    return request<void>('/albums/order', { method: 'PATCH', body: JSON.stringify({ albumIds }) })
  },
  addMedia(albumId: GalleryID, mediaIds: GalleryID[]) {
    return request<void>(`/albums/${albumId}/media`, { method: 'POST', body: JSON.stringify({ mediaIds }) })
  },
  removeMedia(albumId: GalleryID, mediaId: GalleryID) {
    return request<void>(`/albums/${albumId}/media/${mediaId}`, { method: 'DELETE' })
  },
  setAlbumCover(albumId: GalleryID, mediaId: GalleryID) {
    return request<void>(`/albums/${albumId}/cover`, { method: 'PATCH', body: JSON.stringify({ mediaId }) })
  },
  setMediaFavorite(mediaId: GalleryID, favorite: boolean) {
    return request<void>(`/media/files/${mediaId}/favorite`, { method: 'PATCH', body: JSON.stringify({ favorite }) })
  },
  trashMedia(mediaId: GalleryID) {
    return request<void>(`/media/files/${mediaId}`, { method: 'DELETE' })
  },
  deleteMedia(mediaId: GalleryID) {
    return request<void>(`/media/files/${mediaId}/permanent`, { method: 'DELETE' })
  },
  restoreMedia(mediaId: GalleryID) {
    return request<void>(`/media/files/${mediaId}/restore`, { method: 'PATCH' })
  },
  shareMedia(mediaId: GalleryID, username: string, permission: 'read' | 'write') {
    return request<void>(`/media/files/${mediaId}/shares`, { method: 'POST', body: JSON.stringify({ username, permission }) })
  },
  async storage() {
    const storage = await request<ApiStorage>('/storage')
    return { ...storage, thumbnailCacheBytes: 0, largeVideos: [], duplicateGroups: [] } satisfies StorageStats
  },
}
