import { ArrowLeft, CalendarDays, Image, Images, LoaderCircle, MapPin, Sparkles } from 'lucide-react'
import type { CSSProperties } from 'react'
import type { Moment } from '../types/gallery'
import { formatDate } from '../utils/date'
import { AuthenticatedImage } from './AuthenticatedMedia'

type Props = {
  moments: Moment[]
  activeMoment?: Moment
  loading: boolean
  generating: boolean
  onGenerate: () => void
  onOpen: (id: string) => void
  onBack: () => void
  onViewMedia: (id: string) => void
}

export function MomentsView({ moments, activeMoment, loading, generating, onGenerate, onOpen, onBack, onViewMedia }: Props) {
  if (activeMoment) return (
    <section className="moment-detail">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All moments</button>
      <div className="moment-hero">
        {activeMoment.coverUrl && <AuthenticatedImage src={activeMoment.coverUrl} alt="" />}
        <div>
          <span>{formatDate(activeMoment.startTime)}</span>
          <h1>{activeMoment.title}</h1>
          {activeMoment.description && <p>{activeMoment.description}</p>}
          <small><Images size={14} /> {activeMoment.imageCount} photos{activeMoment.locationName && <><b>·</b><MapPin size={14} /> {activeMoment.locationName}</>}</small>
        </div>
      </div>
      <div className="moment-media-grid">
        {activeMoment.media?.map((item) => (
          <button key={item.id} onClick={() => onViewMedia(item.id)} aria-label={`View ${item.fileName}`}>
            {item.thumbnailUrl ? <AuthenticatedImage src={item.thumbnailUrl} alt="" /> : <Image size={24} />}
          </button>
        ))}
      </div>
    </section>
  )

  return (
    <section className="moments-view">
      <header className="moments-heading">
        <div><p>Scenes gathered from photos captured together</p><h1>Moments</h1></div>
        <button className="secondary-button" onClick={onGenerate} disabled={generating}>
          {generating ? <LoaderCircle className="spin" size={17} /> : <Sparkles size={17} />}
          {generating ? 'Looking...' : 'Find moments'}
        </button>
      </header>
      {loading ? <div className="empty-state"><LoaderCircle className="spin" size={26} /><p>Loading moments</p></div> : (
        <div className="moment-grid">
          {moments.map((moment, index) => (
            <button className="moment-card" key={moment.id} onClick={() => onOpen(moment.id)} style={{ '--delay': `${index * 45}ms` } as CSSProperties}>
              {moment.coverUrl ? <AuthenticatedImage src={moment.coverUrl} alt="" /> : <span className="empty-cover"><Image size={28} /></span>}
              <span className="moment-card-copy">
                <strong>{moment.title}</strong>
                <span><CalendarDays size={13} /> {formatDate(moment.startTime)} <b>·</b> <Images size={13} /> {moment.imageCount}</span>
              </span>
            </button>
          ))}
        </div>
      )}
      {!loading && moments.length === 0 && <div className="empty-state"><Sparkles size={28} /><h2>No moments yet</h2><p>Find moments after uploading and processing at least two photos.</p></div>}
    </section>
  )
}
