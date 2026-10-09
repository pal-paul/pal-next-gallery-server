import { ArrowLeft, Pencil, Plus } from 'lucide-react'
import type { Album, LibraryView } from '../types/gallery'
import { formatDate } from '../utils/date'

type Props = {
  view: LibraryView
  album?: Album
  albumCount: number
  mediaCount: number
  favoriteCount: number
  onBack: () => void
  onEdit: () => void
	onAddImages: () => void
}

export function PageHeading({ view, album, albumCount, mediaCount, favoriteCount, onBack, onEdit, onAddImages }: Props) {
  const summary = album
    ? `${album.mediaIds.length} memories · Created ${formatDate(album.createdAt)}`
    : view === 'albums' ? `${albumCount} albums · ${mediaCount} memories`
      : view === 'favorites' ? `${mediaCount} favorite ${mediaCount === 1 ? 'memory' : 'memories'}`
        : view === 'trash' ? `${mediaCount} deleted ${mediaCount === 1 ? 'item' : 'items'}`
        : view === 'map' ? `${mediaCount} memories with location data`
        : view === 'storage' ? 'Storage usage and maintenance insights'
        : `${mediaCount} memories from your local library`

  return (
    <section className="page-heading">
      {view === 'albums' && !album && (
        <div className="library-summary" aria-label="Library summary">
          <div><strong>{albumCount}</strong><span>Albums</span></div>
          <div><strong>{mediaCount}</strong><span>Items</span></div>
          <div><strong>{favoriteCount}</strong><span>Favorites</span></div>
        </div>
      )}
      <div>
        {album && <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All albums</button>}
        {album?.description && <p className="album-description">{album.description}</p>}
        <div className="page-summary-row">
          <p className="page-summary">{summary}</p>
      {album && !album.automatic && <div className="album-heading-actions"><button className="edit-album-button" onClick={onAddImages} aria-label="Add images" title="Add images"><Plus size={15} /></button><button className="edit-album-button" onClick={onEdit} aria-label="Edit album" title="Edit album"><Pencil size={14} /></button></div>}
        </div>
      </div>
    </section>
  )
}
