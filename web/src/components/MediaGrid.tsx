import { ArchiveRestore, Check, Circle, Ellipsis, Film, FolderMinus, Heart, Image, ImageUp, Play, Share2, Trash2, X } from 'lucide-react'
import type { CSSProperties } from 'react'
import { useEffect, useState } from 'react'
import type { MediaItem } from '../types/gallery'
import { formatDate } from '../utils/date'
import { AuthenticatedImage } from './AuthenticatedMedia'

type Props = {
  media: MediaItem[]
  layout?: 'grid' | 'mosaic'
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

export function MediaGrid({ media, layout = 'grid', selected, canRemove, canDelete, inTrash, coverMediaId, onToggle, onRemove, onSetCover, onFavorite, onTrash, onDelete, onRestore, onShare, onView }: Props) {
  const [actionMenuId, setActionMenuId] = useState<string | null>(null)

  useEffect(() => {
    const closeMenu = (event: PointerEvent) => {
      if (!(event.target as Element).closest('.media-actions')) setActionMenuId(null)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setActionMenuId(null)
    }
    document.addEventListener('pointerdown', closeMenu)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('pointerdown', closeMenu)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [])

  const runAction = (action: () => void) => {
    setActionMenuId(null)
    action()
  }

  return (
    <section className={`media-grid ${layout === 'mosaic' ? 'album-mosaic' : ''}`} aria-live="polite">
      {media.map((item, index) => {
        const isSelected = selected.includes(item.id)
        const permission = item.permission ?? 'owner'
        return (
          <article className={`media-card ${isSelected ? 'selected' : ''} ${actionMenuId === item.id ? 'menu-open' : ''}`} key={item.id} style={{ '--delay': `${index * 35}ms` } as CSSProperties}>
            <div className="media-thumb">
              <button className="media-preview" onClick={() => onView(item.id)} aria-label={`View ${item.title} fullscreen`}>
                {item.kind === 'video'
                  ? <AuthenticatedImage src={item.thumbnailUrl ?? item.url} alt={item.title} />
                  : <AuthenticatedImage src={item.url} alt={item.title} />}
              </button>
              {item.kind === 'video' && <span className="video-badge"><Play size={12} fill="currentColor" /> {item.duration}</span>}
              {isSelected && <span className="selection-indicator" aria-label={`${item.title} selected`}><Check size={15} /></span>}
            </div>
            <div className="media-actions">
              <button className="media-actions-trigger" onClick={() => setActionMenuId((current) => current === item.id ? null : item.id)} aria-label={`Actions for ${item.title}`} aria-expanded={actionMenuId === item.id} aria-haspopup="menu"><Ellipsis size={18} /></button>
              {actionMenuId === item.id && (
                <div className="media-actions-menu" role="menu">
                  {!inTrash && <button role="menuitem" onClick={() => runAction(() => onToggle(item.id))}>{isSelected ? <Check size={17} /> : <Circle size={17} />} {isSelected ? 'Deselect' : 'Select'}</button>}
                  {!inTrash && <button role="menuitem" onClick={() => runAction(() => onFavorite(item.id, !item.favorite))}><Heart size={17} fill={item.favorite ? 'currentColor' : 'none'} /> {item.favorite ? 'Remove favorite' : 'Favorite'}</button>}
                  {canRemove && coverMediaId !== item.id && <button role="menuitem" onClick={() => runAction(() => onSetCover(item.id))}><ImageUp size={17} /> Set as cover</button>}
                  {canRemove && <button role="menuitem" onClick={() => runAction(() => onRemove(item.id))}><FolderMinus size={17} /> Remove from album</button>}
                  {!inTrash && permission === 'owner' && <button role="menuitem" onClick={() => runAction(() => onShare(item.id))}><Share2 size={17} /> Share</button>}
                  {!inTrash && permission !== 'read' && <button className="destructive" role="menuitem" onClick={() => runAction(() => onTrash(item.id))}><Trash2 size={17} /> Move to deleted</button>}
                  {canDelete && <button className="destructive" role="menuitem" onClick={() => runAction(() => onDelete(item.id))}><X size={17} /> Delete permanently</button>}
                  {inTrash && <button role="menuitem" onClick={() => runAction(() => onRestore(item.id))}><ArchiveRestore size={17} /> Restore</button>}
                </div>
              )}
            </div>
            <div className="media-meta">
              <div><strong>{item.title}</strong><span>{permission === 'owner' ? formatDate(item.createdAt) : `Shared by ${item.ownerUsername} · ${permission === 'write' ? 'Write' : 'Read'} access`}</span></div>
              <span className="kind-icon" title={item.kind}>{item.kind === 'video' ? <Film size={15} /> : <Image size={15} />}</span>
            </div>
          </article>
        )
      })}
    </section>
  )
}
