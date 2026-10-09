export type MediaKind = 'photo' | 'video'
export type LibraryView = 'albums' | 'moments' | 'media' | 'favorites' | 'trash' | 'map' | 'storage'
export type MediaFilter = 'all' | MediaKind
export type MediaSort = 'newest' | 'oldest' | 'title'
export type MediaGrouping = 'none' | 'day' | 'month' | 'year'
export type GalleryID = string

export type MediaItem = {
  id: GalleryID
  title: string
  kind: MediaKind
  favorite?: boolean
  createdAt: string
  tags: string[]
  url: string
  thumbnailUrl?: string
  path?: string
  duration?: string
  deletedAt?: string
  fileName?: string
  fileSize?: number
  width?: number
  height?: number
  camera?: string
  latitude?: number
  longitude?: number
  ownerUsername?: string
  permission?: 'owner' | 'read' | 'write'
}

export type StorageStats = {
  totalBytes: number
  photoBytes: number
  videoBytes: number
  thumbnailCacheBytes: number
  photoCount: number
  videoCount: number
  largeVideos: MediaItem[]
  duplicateGroups: { fileSize: number; items: MediaItem[] }[]
}

export type Album = {
  id: GalleryID
  title: string
  description?: string
  createdAt: string
  automatic: boolean
  itemCount: number
  mediaIds: GalleryID[]
  coverMediaId?: GalleryID
  coverUrl?: string
}

export type MomentMedia = {
  id: GalleryID
  fileName: string
  thumbnailUrl?: string
  capturedAt: string
  representative: boolean
}

export type Moment = {
  id: GalleryID
  title: string
  description: string
  status: 'draft' | 'published' | 'archived'
  startTime: string
  endTime: string
  locationName?: string
  imageCount: number
  coverMediaId?: GalleryID
  coverUrl?: string
  createdAt: string
  media?: MomentMedia[]
}
