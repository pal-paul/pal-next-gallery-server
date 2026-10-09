import { BarChart3, EllipsisVertical, Heart, Images, LayoutGrid, LogOut, Map, Plus, Sparkles, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type { LibraryView } from '../types/gallery'
import { UploadButton } from './UploadButton'

type Props = {
  view: LibraryView
  showingAlbum: boolean
  albumTitle?: string
  onShowAlbums: () => void
    onShowMoments: () => void
  onShowMedia: () => void
  onShowFavorites: () => void
  onShowTrash: () => void
  onShowMap: () => void
  onShowStorage: () => void
  onCreateAlbum: () => void
  onCreateMoment: () => void
  onFindMoments: () => void
  aiEnabled: boolean
  generatingMoments: boolean
  canUpload: boolean
  onUploaded: () => Promise<void>
  onLogout: () => void
}

export function AppHeader({ view, showingAlbum, albumTitle, onShowAlbums, onShowMoments, onShowMedia, onShowFavorites, onShowTrash, onShowMap, onShowStorage, onCreateAlbum, onCreateMoment, onFindMoments, aiEnabled, generatingMoments, canUpload, onUploaded, onLogout }: Props) {
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  const currentLabel = albumTitle ?? (view === 'favorites' ? 'Favorites' : view === 'trash' ? 'Trash' : view === 'map' ? 'Map' : view === 'storage' ? 'Storage' : view === 'media' && !showingAlbum ? 'All' : 'Albums')

  useEffect(() => {
    const closeMenu = (event: MouseEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setMenuOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuOpen(false)
    }
    document.addEventListener('mousedown', closeMenu)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('mousedown', closeMenu)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [])

  const selectOption = (action: () => void) => {
    action()
    setMenuOpen(false)
  }

  return (
    <>
      <header className="topbar">
        <button className="brand" onClick={onShowAlbums} aria-label="Open albums">
          <img className="brand-mark" src={`${import.meta.env.BASE_URL}icon.png`} alt="" />
          <span>{showingAlbum ? currentLabel : 'Next Gallery'}</span>
        </button>
        <div className="top-actions">
          <UploadButton disabled={!canUpload} onUploaded={onUploaded} />
          {view === 'albums' && <button className="header-action" onClick={onCreateAlbum} aria-label="New album" title="New album"><Plus size={22} /></button>}
          <div className="options-menu" ref={menuRef}>
            <button className="options-trigger" onClick={() => setMenuOpen((open) => !open)} aria-label="Options" title="Options" aria-expanded={menuOpen} aria-haspopup="menu">
              <EllipsisVertical size={21} />
            </button>
            {menuOpen && (
              <div className="options-list" role="menu">
        {view === 'moments' ? <>
          <button role="menuitem" onClick={() => selectOption(onCreateMoment)}><Plus size={17} /> New moment</button>
          {aiEnabled && <button role="menuitem" disabled={generatingMoments} onClick={() => selectOption(onFindMoments)}><Sparkles size={17} /> {generatingMoments ? 'Looking...' : 'Find moments'}</button>}
        </> : <button role="menuitem" onClick={() => selectOption(onCreateAlbum)}><Plus size={17} /> New Album</button>}
                <button role="menuitem" onClick={() => selectOption(onLogout)}><LogOut size={17} /> Sign out</button>
              </div>
            )}
          </div>
        </div>
      </header>
      <nav className="tabbar" aria-label="Gallery sections">
        <button className={view === 'albums' ? 'active' : ''} onClick={onShowAlbums}><Images /><span>Albums</span></button>
        <button className={view === 'moments' ? 'active' : ''} onClick={onShowMoments}><Sparkles /><span>Moments</span></button>
        <button className={view === 'media' && !showingAlbum ? 'active' : ''} onClick={onShowMedia}><LayoutGrid /><span>Library</span></button>
        <button className={view === 'favorites' ? 'active' : ''} onClick={onShowFavorites}><Heart /><span>Favorites</span></button>
        <button className={view === 'trash' ? 'active' : ''} onClick={onShowTrash}><Trash2 /><span>Deleted</span></button>
        <button className={view === 'map' ? 'active' : ''} onClick={onShowMap}><Map /><span>Map</span></button>
        <button className={view === 'storage' ? 'active' : ''} onClick={onShowStorage}><BarChart3 /><span>Storage</span></button>
      </nav>
    </>
  )
}
