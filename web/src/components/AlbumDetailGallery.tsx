import { CalendarDays, Images, Play } from 'lucide-react'
import type { Album, MediaItem } from '../types/gallery'
import { formatDate } from '../utils/date'
import { AuthenticatedImage } from './AuthenticatedMedia'
import { MediaGrid } from './MediaGrid'

type Props = {
  album: Album
  media: MediaItem[]
  selected: string[]
  onToggle: (id: string) => void
  onRemove: (id: string) => void
  onSetCover: (id: string) => void
  onFavorite: (id: string, favorite: boolean) => void
  onTrash: (id: string) => void
  onDelete: (id: string) => void
  onRestore: (id: string) => void
  onShare: (id: string) => void
  onView: (id: string) => void
}

export function AlbumDetailGallery({ album, media, onView, ...gridProps }: Props) {
  const cover = media.find((item) => item.id === album.coverMediaId) ?? media[0]
  const otherMedia = cover ? media.filter((item) => item.id !== cover.id) : media

  return (
    <section className="album-detail-gallery">
      {cover && (
        <button className="album-detail-cover" onClick={() => onView(cover.id)} aria-label={`View ${cover.title} fullscreen`}>
          <AuthenticatedImage src={cover.kind === 'video' ? cover.thumbnailUrl ?? cover.url : cover.url} alt={cover.title} />
          {cover.kind === 'video' && <Play className="album-cover-play" fill="currentColor" aria-hidden="true" />}
          <span className="album-detail-copy">
            <strong>{album.title}</strong>
            <span><CalendarDays size={14} /> {formatDate(album.createdAt)} <b>·</b> <Images size={14} /> {media.length}</span>
          </span>
        </button>
      )}
      <MediaGrid
        media={otherMedia}
        layout="mosaic"
        canRemove
        canDelete={false}
        inTrash={false}
        coverMediaId={album.coverMediaId}
        onView={onView}
        {...gridProps}
      />
    </section>
  )
}
