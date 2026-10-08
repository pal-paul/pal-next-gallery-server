import type { MediaGrouping, MediaItem } from '../types/gallery'
import { MediaGrid } from './MediaGrid'

type Props = {
  media: MediaItem[]
  grouping: MediaGrouping
  selected: string[]
  canRemove: boolean
  canDelete: boolean
  inTrash: boolean
  coverMediaId?: string
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

const groupLabel = (dateValue: string, grouping: Exclude<MediaGrouping, 'none'>) => {
  const date = new Date(`${dateValue.slice(0, 10)}T12:00:00`)
  if (grouping === 'year') return String(date.getFullYear())
  if (grouping === 'month') return date.toLocaleDateString('en-US', { month: 'long', year: 'numeric' })
  return date.toLocaleDateString('en-US', { day: 'numeric', month: 'long', year: 'numeric' })
}

export function MediaTimeline({ media, grouping, ...gridProps }: Props) {
  if (grouping === 'none') return <MediaGrid media={media} {...gridProps} />

  const groups = new Map<string, MediaItem[]>()
  media.forEach((item) => {
    const label = groupLabel(item.createdAt, grouping)
    groups.set(label, [...(groups.get(label) ?? []), item])
  })

  return (
    <div className="media-timeline">
      {[...groups].map(([label, items]) => (
        <section className="timeline-group" key={label}>
          <h2>{label}</h2>
          <MediaGrid media={items} {...gridProps} />
        </section>
      ))}
    </div>
  )
}
