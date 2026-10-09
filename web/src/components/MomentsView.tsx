import { ArrowLeft, CalendarDays, Check, Image, Images, LoaderCircle, MapPin, Pencil, Plus, Sparkles, Trash2, X } from 'lucide-react'
import { useState, type CSSProperties } from 'react'
import type { MediaItem, Moment } from '../types/gallery'
import { formatDate } from '../utils/date'
import { AuthenticatedImage } from './AuthenticatedMedia'

type Props = {
  moments: Moment[]
  activeMoment?: Moment
  loading: boolean
  onOpen: (id: string) => void
  onBack: () => void
  onViewMedia: (id: string) => void
  media: MediaItem[]
  aiEnabled: boolean
  onCreate: (title: string, description: string, mediaIds: string[]) => Promise<void>
  onUpdate: (title: string, description: string, status: Moment['status']) => Promise<void>
  onDelete: () => Promise<void>
  onAddMedia: (mediaIds: string[]) => Promise<void>
  onRemoveMedia: (mediaId: string) => Promise<void>
  onSuggestMetadata: () => Promise<{ title: string; description: string }>
  creating: boolean
  onCreatingChange: (creating: boolean) => void
}

export function MomentsView({ moments, activeMoment, loading, onOpen, onBack, onViewMedia, media, aiEnabled, onCreate, onUpdate, onDelete, onAddMedia, onRemoveMedia, onSuggestMetadata, creating, onCreatingChange }: Props) {
  const [editing, setEditing] = useState(false)
  const [title, setTitle] = useState(activeMoment?.title ?? '')
  const [description, setDescription] = useState(activeMoment?.description ?? '')
  const [picked, setPicked] = useState<string[]>([])
  const [pending, setPending] = useState(false)
  const photos = media.filter((item) => item.kind === 'photo' && !item.deletedAt)
  const currentIds = new Set(activeMoment?.media?.map((item) => item.id) ?? [])
  const togglePicked = (id: string) => setPicked((items) => items.includes(id) ? items.filter((item) => item !== id) : [...items, id])
  const run = async (action: () => Promise<void>) => { setPending(true); try { await action() } finally { setPending(false) } }
  if (activeMoment) return (
    <section className="moment-detail">
    <div className="moment-detail-toolbar"><button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All moments</button><div>
    <button className="secondary-button" onClick={() => setEditing((value) => !value)}><Pencil size={16} /> Edit</button>
    {activeMoment.status === 'draft' && <button className="primary-button" disabled={pending} onClick={() => void run(() => onUpdate(title, description, 'published'))}><Check size={16} /> Accept</button>}
    <button className="danger-button" disabled={pending} onClick={() => window.confirm(activeMoment.status === 'draft' ? 'Decline this draft? Its images will be available for new moments.' : 'Delete this moment?') && void run(onDelete)}><Trash2 size={16} /> {activeMoment.status === 'draft' ? 'Decline' : 'Delete'}</button>
    </div></div>
    {editing && <section className="moment-editor"><div className="moment-editor-heading"><strong>Edit moment</strong>{aiEnabled && <button className="secondary-button" disabled={pending} onClick={() => void run(async () => { const suggestion = await onSuggestMetadata(); setTitle(suggestion.title); setDescription(suggestion.description) })}><Sparkles size={16} /> Suggest with AI</button>}</div><label className="field-label">Title<input value={title} onChange={(event) => setTitle(event.target.value)} /></label><label className="field-label">Description<textarea rows={3} value={description} onChange={(event) => setDescription(event.target.value)} /></label><div className="moment-editor-actions"><button className="secondary-button" onClick={() => setEditing(false)}>Cancel</button><button className="primary-button" disabled={!title.trim() || pending} onClick={() => void run(async () => { await onUpdate(title, description, activeMoment.status); setEditing(false) })}>Save</button></div><details><summary>Add images</summary><div className="moment-picker">{photos.filter((item) => !currentIds.has(item.id)).map((item) => <button type="button" aria-label={`Select ${item.title}`} className={picked.includes(item.id) ? 'selected' : ''} key={item.id} onClick={() => togglePicked(item.id)}><AuthenticatedImage src={item.thumbnailUrl ?? item.url} alt="" />{picked.includes(item.id) && <span><Check size={14} /></span>}</button>)}</div><button className="primary-button" disabled={picked.length === 0 || pending} onClick={() => void run(async () => { await onAddMedia(picked); setPicked([]) })}><Plus size={16} /> Add selected</button></details></section>}
      <div className="moment-hero">
        {activeMoment.coverUrl && <AuthenticatedImage src={activeMoment.coverUrl} alt="" />}
        <div>
		  <span>{formatDate(activeMoment.startTime)} <b className="status-badge">{activeMoment.status}</b></span>
          <h1>{activeMoment.title}</h1>
          {activeMoment.description && <p>{activeMoment.description}</p>}
          <small><Images size={14} /> {activeMoment.imageCount} photos{activeMoment.locationName && <><b>·</b><MapPin size={14} /> {activeMoment.locationName}</>}</small>
        </div>
      </div>
      <div className="moment-media-grid">
        {activeMoment.media?.map((item) => (
		  <div className="moment-media-item" key={item.id}><button onClick={() => onViewMedia(item.id)} aria-label={`View ${item.fileName}`}>
            {item.thumbnailUrl ? <AuthenticatedImage src={item.thumbnailUrl} alt="" /> : <Image size={24} />}
		  </button>{editing && activeMoment.imageCount > 1 && <button className="moment-remove" title="Remove image" onClick={() => void run(() => onRemoveMedia(item.id))}><X size={15} /></button>}</div>
        ))}
      </div>
    </section>
  )

  return (
    <section className="moments-view">
      <header className="moments-heading">
        <div><p>Scenes gathered from photos captured together</p><h1>Moments</h1></div>
      </header>
    {creating && <section className="moment-editor"><div className="moment-editor-heading"><strong>Create draft moment</strong><button className="icon-button" title="Close" onClick={() => onCreatingChange(false)}><X size={17} /></button></div><label className="field-label">Title<input autoFocus value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Untitled moment" /></label><label className="field-label">Description<textarea rows={2} value={description} onChange={(event) => setDescription(event.target.value)} /></label><div className="moment-picker">{photos.map((item) => <button type="button" aria-label={`Select ${item.title}`} className={picked.includes(item.id) ? 'selected' : ''} key={item.id} onClick={() => togglePicked(item.id)}><AuthenticatedImage src={item.thumbnailUrl ?? item.url} alt="" />{picked.includes(item.id) && <span><Check size={14} /></span>}</button>)}</div><div className="moment-editor-actions"><span>{picked.length} selected</span><button className="primary-button" disabled={picked.length === 0 || pending} onClick={() => void run(async () => { await onCreate(title, description, picked); onCreatingChange(false); setPicked([]) })}>Create draft</button></div></section>}
      {loading ? <div className="empty-state"><LoaderCircle className="spin" size={26} /><p>Loading moments</p></div> : (
        <div className="moment-grid">
          {moments.map((moment, index) => (
            <button className="moment-card" key={moment.id} onClick={() => onOpen(moment.id)} style={{ '--delay': `${index * 45}ms` } as CSSProperties}>
              {moment.coverUrl ? <AuthenticatedImage src={moment.coverUrl} alt="" /> : <span className="empty-cover"><Image size={28} /></span>}
              <span className="moment-card-copy">
                <strong>{moment.title}</strong>
        				{moment.status === 'draft' && <em className="status-badge">Draft</em>}
                <span><CalendarDays size={13} /> {formatDate(moment.startTime)} <b>·</b> <Images size={13} /> {moment.imageCount}</span>
              </span>
            </button>
          ))}
        </div>
      )}
      {!loading && moments.length === 0 && <div className="empty-state"><Sparkles size={28} /><h2>No moments yet</h2><p>Find moments after uploading and processing at least three photos.</p></div>}
    </section>
  )
}
