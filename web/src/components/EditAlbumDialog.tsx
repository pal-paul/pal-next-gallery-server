import { Check, LoaderCircle, Pencil, Plus, Sparkles, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { Album, MediaItem } from '../types/gallery'
import { AuthenticatedImage } from './AuthenticatedMedia'

type Props = {
  open: boolean
  album: Album
  media: MediaItem[]
  name: string
  description: string
  coverMediaId?: string
  onNameChange: (value: string) => void
  onDescriptionChange: (value: string) => void
  onCoverChange: (id: string) => void
  onClose: () => void
  onSave: () => void
  aiEnabled: boolean
  aiPending: boolean
  onSuggestMetadata: () => void
  startAdding: boolean
  onAddMedia: (mediaIds: string[]) => Promise<void>
}

export function EditAlbumDialog({ open, album, media, name, description, coverMediaId, onNameChange, onDescriptionChange, onCoverChange, onClose, onSave, aiEnabled, aiPending, onSuggestMetadata, startAdding, onAddMedia }: Props) {
  const [adding, setAdding] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [addingPending, setAddingPending] = useState(false)
  useEffect(() => {
    if (!open) return
  setAdding(startAdding)
  setSelected([])
    const closeOnEscape = (event: KeyboardEvent) => event.key === 'Escape' && onClose()
    document.addEventListener('keydown', closeOnEscape)
    return () => document.removeEventListener('keydown', closeOnEscape)
  }, [onClose, open, startAdding])

  if (!open) return null
  const photos = media.filter((item) => album.mediaIds.includes(item.id) && item.kind === 'photo')
  const availablePhotos = media.filter((item) => !item.deletedAt && item.kind === 'photo' && !album.mediaIds.includes(item.id))
  const toggleSelected = (id: string) => setSelected((items) => items.includes(id) ? items.filter((item) => item !== id) : [...items, id])
  const addSelected = async () => {
    if (selected.length === 0 || addingPending) return
    setAddingPending(true)
    try {
      await onAddMedia(selected)
      setSelected([])
      setAdding(false)
    } finally {
      setAddingPending(false)
    }
  }

  return (
    <div className="modal-backdrop" onMouseDown={onClose}>
      <section className="modal edit-album-modal" role="dialog" aria-modal="true" aria-labelledby="edit-album-title" onMouseDown={(event) => event.stopPropagation()}>
        <header><div className="modal-icon"><Pencil size={20} /></div><button className="icon-button" onClick={onClose} title="Close"><X size={19} /></button></header>
        <h2 id="edit-album-title">Edit album</h2>
    <button className="secondary-button ai-suggest-button" onClick={() => setAdding((value) => !value)}><Plus size={16} /> Add images</button>
    {adding && <section className="album-image-picker" aria-label="Add images to album">
      {availablePhotos.length > 0 ? <><div className="moment-picker">{availablePhotos.map((item) => <button type="button" aria-label={`Select ${item.title}`} aria-pressed={selected.includes(item.id)} className={selected.includes(item.id) ? 'selected' : ''} key={item.id} onClick={() => toggleSelected(item.id)}><AuthenticatedImage src={item.thumbnailUrl ?? item.url} alt="" />{selected.includes(item.id) && <span><Check size={14} /></span>}</button>)}</div><div className="album-picker-actions"><span>{selected.length} selected</span><button className="primary-button" disabled={selected.length === 0 || addingPending} onClick={() => void addSelected()}>{addingPending ? <LoaderCircle className="spin" size={16} /> : <Plus size={16} />} Add selected</button></div></> : <p className="cover-empty">All available photos are already in this album.</p>}
    </section>}
    		{aiEnabled && photos.length > 0 && <button className="secondary-button ai-suggest-button" onClick={onSuggestMetadata} disabled={aiPending}>{aiPending ? <LoaderCircle className="spin" size={16} /> : <Sparkles size={16} />} Suggest title and description</button>}
        <label className="field-label">Album title<input autoFocus value={name} onChange={(event) => onNameChange(event.target.value)} onKeyDown={(event) => event.key === 'Enter' && onSave()} /></label>
        <label className="field-label">Description<textarea rows={3} maxLength={240} value={description} onChange={(event) => onDescriptionChange(event.target.value)} placeholder="Add a short description" /></label>
        <fieldset className="cover-picker">
          <legend>Cover image</legend>
          {photos.length > 0
            ? <div className="cover-options">{photos.map((item) => (
                <button className={coverMediaId === item.id ? 'selected' : ''} type="button" key={item.id} onClick={() => onCoverChange(item.id)} aria-label={`Use ${item.title} as cover`} aria-pressed={coverMediaId === item.id}>
                  <AuthenticatedImage src={item.url} alt="" />
                  {coverMediaId === item.id && <span><Check size={15} /></span>}
                </button>
              ))}</div>
            : <p className="cover-empty">Add a photo to this album to choose a cover.</p>}
        </fieldset>
        <footer><button className="secondary-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={!name.trim()} onClick={onSave}>Save changes</button></footer>
      </section>
    </div>
  )
}
