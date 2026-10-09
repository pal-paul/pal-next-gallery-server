import { useDeferredValue, useEffect, useState } from 'react'
import { Search } from 'lucide-react'
import { ApiError, demoMode } from './api/apiClient'
import { galleryApi } from './api/galleryApi'
import { momentsApi } from './api/momentsApi'
import { AlbumGrid } from './components/AlbumGrid'
import { AlbumDetailGallery } from './components/AlbumDetailGallery'
import { AppHeader } from './components/AppHeader'
import { CreateAlbumDialog } from './components/CreateAlbumDialog'
import { EditAlbumDialog } from './components/EditAlbumDialog'
import { LibraryToolbar } from './components/LibraryToolbar'
import { MediaTimeline } from './components/MediaTimeline'
import { MediaMap } from './components/MediaMap'
import { MediaViewer } from './components/MediaViewer'
import { MomentsView } from './components/MomentsView'
import type { Album, GalleryID, LibraryView, MediaFilter, MediaGrouping, MediaItem, MediaSort, Moment, StorageStats } from './types/gallery'
import { PageHeading } from './components/PageHeading'
import { SelectionBar } from './components/SelectionBar'
import { ShareMediaDialog, type SharePermission } from './components/ShareMediaDialog'
import { StorageDashboard } from './components/StorageDashboard'
import { albumSeed, mediaSeed } from './data/gallerySeed'
import './App.css'

type Props = { onLogout: () => Promise<void> }

const sharingErrorMessage = (error: unknown) => {
  if (!(error instanceof ApiError)) return 'Unable to share the library. Please try again.'
  if (error.status === 400) return 'That account cannot receive this share.'
  if (error.status === 401) return 'Your session has expired. Sign in and try again.'
  if (error.status === 403) return 'You do not have permission to share this library.'
  if (error.status === 404) return 'The username or media could not be found.'
  return 'Unable to share the library. Please try again.'
}

function App({ onLogout }: Props) {
  const [view, setView] = useState<LibraryView>('albums')
  const [albums, setAlbums] = useState<Album[]>(() => demoMode ? structuredClone(albumSeed) : [])
  const [media, setMedia] = useState<MediaItem[]>(() => demoMode ? structuredClone(mediaSeed) : [])
  const [moments, setMoments] = useState<Moment[]>([])
  const [activeMoment, setActiveMoment] = useState<Moment>()
  const [momentsLoading, setMomentsLoading] = useState(false)
  const [generatingMoments, setGeneratingMoments] = useState(false)
	const [creatingMoment, setCreatingMoment] = useState(false)
  const [aiEnabled, setAiEnabled] = useState(false)
  const [albumAiPending, setAlbumAiPending] = useState(false)
  const [activeAlbumId, setActiveAlbumId] = useState<GalleryID | null>(null)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<MediaFilter>('all')
  const [sort, setSort] = useState<MediaSort>('newest')
  const [grouping, setGrouping] = useState<MediaGrouping>('none')
  const [selected, setSelected] = useState<GalleryID[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [newAlbumName, setNewAlbumName] = useState('')
  const [targetAlbumId, setTargetAlbumId] = useState(() => demoMode ? albumSeed[0]?.id ?? '' : '')
  const [connected, setConnected] = useState(false)
  const [initializing, setInitializing] = useState(!demoMode)
  const [viewerMediaId, setViewerMediaId] = useState<GalleryID | null>(null)
  const [showEditAlbum, setShowEditAlbum] = useState(false)
	const [editAlbumAdding, setEditAlbumAdding] = useState(false)
  const [storageStats, setStorageStats] = useState<StorageStats>()
  const [editAlbumName, setEditAlbumName] = useState('')
  const [editAlbumDescription, setEditAlbumDescription] = useState('')
  const [editCoverMediaId, setEditCoverMediaId] = useState<GalleryID | undefined>()
  const [shareMediaId, setShareMediaId] = useState<GalleryID | null>(null)
  const [shareUsername, setShareUsername] = useState('')
  const [sharePermission, setSharePermission] = useState<SharePermission>('read')
  const [shareError, setShareError] = useState<string>()
  const [sharing, setSharing] = useState(false)
  const deferredQuery = useDeferredValue(query.toLowerCase().trim())

  useEffect(() => {
    if (demoMode) return
    const controller = new AbortController()
    Promise.all([galleryApi.loadAlbums(controller.signal), galleryApi.loadMedia(controller.signal), momentsApi.list(controller.signal), momentsApi.features()]).then(([loadedAlbums, loadedMedia, loadedMoments, features]) => {
      setAlbums(loadedAlbums)
      setMedia(loadedMedia)
      setMoments(loadedMoments)
	  setAiEnabled(features.ai)
      setTargetAlbumId(loadedAlbums[0]?.id ?? '')
      setConnected(true)
    }).catch((error: unknown) => {
      if (error instanceof DOMException && error.name === 'AbortError') return
      setConnected(false)
    }).finally(() => setInitializing(false))
    return () => controller.abort()
  }, [])

  const activeAlbum = albums.find((album) => album.id === activeAlbumId)
  const visibleMedia = media
    .filter((item) => view === 'trash' ? Boolean(item.deletedAt) : !item.deletedAt)
    .filter((item) => activeAlbum ? activeAlbum.mediaIds.includes(item.id) : true)
    .filter((item) => view !== 'favorites' || item.favorite)
    .filter((item) => filter === 'all' || item.kind === filter)
    .filter((item) => !deferredQuery || `${item.title} ${item.fileName ?? ''} ${item.path ?? ''} ${item.camera ?? ''} ${item.width ?? ''}x${item.height ?? ''} ${item.tags.join(' ')} ${item.createdAt}`.toLowerCase().includes(deferredQuery))
    .sort((first, second) => sort === 'title'
      ? first.title.localeCompare(second.title)
      : sort === 'oldest' ? first.createdAt.localeCompare(second.createdAt) : second.createdAt.localeCompare(first.createdAt))

  const visibleAlbums = albums.filter((album) => {
    const albumMedia = media.filter((item) => album.mediaIds.includes(item.id))
    const searchable = `${album.title} ${album.description ?? ''} ${album.createdAt} ${albumMedia.flatMap((item) => [item.title, ...item.tags]).join(' ')}`
    return !deferredQuery || searchable.toLowerCase().includes(deferredQuery)
  })

  const showAlbums = () => {
    setView('albums')
    setActiveAlbumId(null)
    setSelected([])
  }

  const showMoments = async () => {
    setView('moments')
    setActiveAlbumId(null)
    setActiveMoment(undefined)
  	setCreatingMoment(false)
    setSelected([])
    if (!connected) return
    setMomentsLoading(true)
    try {
      setMoments(await momentsApi.list())
    } finally {
      setMomentsLoading(false)
    }
  }

  const openMoment = async (id: GalleryID) => {
    setMomentsLoading(true)
    try {
      setActiveMoment(await momentsApi.get(id))
    } finally {
      setMomentsLoading(false)
    }
  }

  const generateMoments = async () => {
    if (!connected || generatingMoments) return
    setGeneratingMoments(true)
    try {
      await momentsApi.generate()
      setMoments(await momentsApi.list())
    } finally {
      setGeneratingMoments(false)
    }
  }

  const refreshActiveMoment = async () => {
    if (!activeMoment) return
    const refreshed = await momentsApi.get(activeMoment.id)
    setActiveMoment(refreshed)
    setMoments((items) => items.map((item) => item.id === refreshed.id ? refreshed : item))
  }

  const createMoment = async (title: string, description: string, mediaIds: string[]) => {
    const created = await momentsApi.create(title, description, mediaIds)
    setMoments((items) => [created, ...items])
    setActiveMoment(created)
  }

  const updateMoment = async (title: string, description: string, status: Moment['status']) => {
    if (!activeMoment) return
    await momentsApi.update(activeMoment.id, title, description, status)
    setActiveMoment({ ...activeMoment, title, description, status })
    setMoments((items) => items.map((item) => item.id === activeMoment.id ? { ...item, title, description, status } : item))
  }

  const deleteMoment = async () => {
    if (!activeMoment) return
    await momentsApi.delete(activeMoment.id)
    setMoments((items) => items.filter((item) => item.id !== activeMoment.id))
    setActiveMoment(undefined)
  }

  const addMomentMedia = async (mediaIds: string[]) => {
    if (!activeMoment) return
    await momentsApi.addMedia(activeMoment.id, mediaIds)
    await refreshActiveMoment()
  }

  const removeMomentMedia = async (mediaId: string) => {
    if (!activeMoment) return
    await momentsApi.removeMedia(activeMoment.id, mediaId)
    await refreshActiveMoment()
  }

  const suggestMomentMetadata = async () => {
    if (!activeMoment) return { title: '', description: '' }
    return momentsApi.suggestMetadata(activeMoment.id)
  }

  const loadAllMedia = async () => {
    if (!connected) return
    setMedia(await galleryApi.loadMedia())
  }

  const showAllMedia = () => {
    setView('media')
    setActiveAlbumId(null)
    setSelected([])
    void loadAllMedia()
  }

  const showFavorites = () => {
    setView('favorites')
    setActiveAlbumId(null)
    setSelected([])
    void loadAllMedia()
  }

  const showTrash = async () => {
    setView('trash')
    setActiveAlbumId(null)
    setSelected([])
    if (!connected) return
    const trashed = await galleryApi.loadTrash()
    setMedia((current) => [...current.filter((item) => !item.deletedAt), ...trashed])
  }

  const showMap = () => {
    setView('map')
    setActiveAlbumId(null)
    setSelected([])
    void loadAllMedia()
  }

  const showStorage = async () => {
    setView('storage')
    setActiveAlbumId(null)
    setSelected([])
    if (demoMode) {
      const photos = media.filter((item) => item.kind === 'photo' && !item.deletedAt)
      const videos = media.filter((item) => item.kind === 'video' && !item.deletedAt)
      const photoBytes = photos.reduce((total, item) => total + (item.fileSize ?? 0), 0)
      const videoBytes = videos.reduce((total, item) => total + (item.fileSize ?? 0), 0)
      const bySize = [...photos, ...videos].reduce((groups, item) => {
        if (!item.fileSize) return groups
        groups.set(item.fileSize, [...(groups.get(item.fileSize) ?? []), item])
        return groups
      }, new Map<number, MediaItem[]>())
      const duplicateGroups = [...bySize.entries()]
        .filter(([, items]) => items.length > 1)
        .map(([fileSize, items]) => ({ fileSize, items }))
      setStorageStats({ totalBytes: photoBytes + videoBytes, photoBytes, videoBytes, thumbnailCacheBytes: 18_874_368, photoCount: photos.length, videoCount: videos.length, largeVideos: videos.toSorted((first, second) => (second.fileSize ?? 0) - (first.fileSize ?? 0)).slice(0, 2), duplicateGroups })
      return
    }
    setStorageStats(await galleryApi.storage())
  }

  const openAlbum = async (id: GalleryID) => {
    setActiveAlbumId(id)
    setView('media')
    setSelected([])
    if (!connected) return
    const detail = await galleryApi.loadAlbum(id)
    setAlbums((current) => current.map((album) => album.id === id ? detail.album : album))
    setMedia((current) => {
      const albumMediaIds = new Set(detail.media.map((item) => item.id))
      return [...current.filter((item) => !albumMediaIds.has(item.id)), ...detail.media]
    })
  }

  const moveAlbum = async (id: GalleryID, offset: -1 | 1) => {
    const currentIndex = albums.findIndex((album) => album.id === id)
    const targetIndex = currentIndex + offset
    if (currentIndex < 0 || targetIndex < 0 || targetIndex >= albums.length) return
    const reordered = [...albums]
    const [moved] = reordered.splice(currentIndex, 1)
    reordered.splice(targetIndex, 0, moved)
    setAlbums(reordered)
    if (connected) await galleryApi.reorderAlbums(reordered.map((album) => album.id))
  }

  const createAlbum = async () => {
    const title = newAlbumName.trim()
    if (!title) return
    const album = connected
      ? await galleryApi.createAlbum(title)
      : { id: crypto.randomUUID(), title, createdAt: new Date().toISOString().slice(0, 10), automatic: false, itemCount: 0, mediaIds: [] }
    setAlbums((current) => [album, ...current])
    setTargetAlbumId(album.id)
    setNewAlbumName('')
    setShowCreate(false)
  }

  const toggleSelection = (id: GalleryID) => setSelected((current) => current.includes(id)
    ? current.filter((selectedId) => selectedId !== id)
    : [...current, id])

  const addSelectedToAlbum = async () => {
    if (connected) await galleryApi.addMedia(targetAlbumId, selected)
    setAlbums((current) => current.map((album) => album.id === targetAlbumId
      ? { ...album, mediaIds: [...new Set([...album.mediaIds, ...selected])] }
      : album))
    setSelected([])
  }

  const removeFromActiveAlbum = async (mediaId: GalleryID) => {
    if (!activeAlbum) return
    if (connected) await galleryApi.removeMedia(activeAlbum.id, mediaId)
    setAlbums((current) => current.map((album) => album.id === activeAlbum.id
      ? { ...album, mediaIds: album.mediaIds.filter((id) => id !== mediaId), coverMediaId: album.coverMediaId === mediaId ? undefined : album.coverMediaId }
      : album))
  }

  const setAlbumCover = async (mediaId: GalleryID) => {
    if (!activeAlbum) return
    if (connected) await galleryApi.setAlbumCover(activeAlbum.id, mediaId)
    setAlbums((current) => current.map((album) => album.id === activeAlbum.id ? { ...album, coverMediaId: mediaId } : album))
  }

  const setMediaFavorite = async (mediaId: GalleryID, favorite: boolean) => {
    if (connected) await galleryApi.setMediaFavorite(mediaId, favorite)
    setMedia((current) => current.map((item) => item.id === mediaId ? { ...item, favorite } : item))
  }

  const trashMedia = async (mediaId: GalleryID) => {
    if (connected) await galleryApi.trashMedia(mediaId)
    setMedia((current) => current.map((item) => item.id === mediaId ? { ...item, deletedAt: new Date().toISOString() } : item))
    setSelected((current) => current.filter((id) => id !== mediaId))
  }

  const restoreMedia = async (mediaId: GalleryID) => {
    if (connected) await galleryApi.restoreMedia(mediaId)
    setMedia((current) => current.map((item) => item.id === mediaId ? { ...item, deletedAt: undefined } : item))
  }

  const openShare = (mediaId: GalleryID) => {
    setShareMediaId(mediaId)
    setShareUsername('')
    setSharePermission('read')
    setShareError(undefined)
  }

  const shareMedia = async () => {
    if (!shareMediaId || !shareUsername.trim() || sharing) return
    setSharing(true)
    setShareError(undefined)
    try {
      if (connected) await galleryApi.shareMedia(shareMediaId, shareUsername.trim(), sharePermission)
      setShareMediaId(null)
    } catch (error: unknown) {
      setShareError(sharingErrorMessage(error))
    } finally {
      setSharing(false)
    }
  }

  const deleteMedia = async (mediaId: GalleryID) => {
    const item = media.find((candidate) => candidate.id === mediaId)
    if (!item || !window.confirm(`Permanently delete ${item.title}? This cannot be undone.`)) return
    if (connected) await galleryApi.deleteMedia(mediaId)
    setMedia((current) => current.filter((candidate) => candidate.id !== mediaId))
    setSelected((current) => current.filter((id) => id !== mediaId))
  }

  const openEditAlbum = (adding = false) => {
    if (!activeAlbum) return
    setEditAlbumName(activeAlbum.title)
    setEditAlbumDescription(activeAlbum.description ?? '')
    setEditCoverMediaId(activeAlbum.coverMediaId ?? activeAlbum.mediaIds.find((id) => media.find((item) => item.id === id)?.kind === 'photo'))
  setEditAlbumAdding(adding)
    setShowEditAlbum(true)
  }

  const addMediaToActiveAlbum = async (mediaIds: GalleryID[]) => {
    if (!activeAlbum) return
    if (connected) await galleryApi.addMedia(activeAlbum.id, mediaIds)
    setAlbums((current) => current.map((album) => album.id === activeAlbum.id
      ? { ...album, mediaIds: [...new Set([...album.mediaIds, ...mediaIds])], itemCount: new Set([...album.mediaIds, ...mediaIds]).size }
      : album))
  }

  const saveAlbumEdits = async () => {
    if (!activeAlbum) return
    const title = editAlbumName.trim()
    const description = editAlbumDescription.trim()
    if (!title) return
    if (connected && (title !== activeAlbum.title || description !== (activeAlbum.description ?? ''))) await galleryApi.updateAlbum(activeAlbum.id, title, description)
    if (connected && editCoverMediaId && editCoverMediaId !== activeAlbum.coverMediaId) await galleryApi.setAlbumCover(activeAlbum.id, editCoverMediaId)
    setAlbums((current) => current.map((album) => album.id === activeAlbum.id
      ? { ...album, title, description, coverMediaId: editCoverMediaId }
      : album))
    setShowEditAlbum(false)
  }

  const suggestAlbumMetadata = async () => {
    if (!activeAlbum || albumAiPending) return
    setAlbumAiPending(true)
    try {
      const suggestion = await galleryApi.suggestAlbumMetadata(activeAlbum.id)
      setEditAlbumName(suggestion.title)
      setEditAlbumDescription(suggestion.description)
    } finally {
      setAlbumAiPending(false)
    }
  }

  const viewerIndex = visibleMedia.findIndex((item) => item.id === viewerMediaId)
  const viewerItem = viewerIndex >= 0 ? visibleMedia[viewerIndex] : undefined
  const showViewerItem = (offset: number) => {
    if (viewerIndex < 0 || visibleMedia.length === 0) return
    const nextIndex = (viewerIndex + offset + visibleMedia.length) % visibleMedia.length
    setViewerMediaId(visibleMedia[nextIndex].id)
  }

  if (initializing) return <div className="auth-loading"><span>Loading gallery</span></div>

  return (
    <div className="app-shell">
      <AppHeader view={view} showingAlbum={Boolean(activeAlbum)} albumTitle={activeAlbum?.title} onShowAlbums={showAlbums} onShowMoments={() => void showMoments()} onShowMedia={showAllMedia} onShowFavorites={showFavorites} onShowTrash={showTrash} onShowMap={showMap} onShowStorage={() => void showStorage()} onCreateAlbum={() => setShowCreate(true)} onCreateMoment={() => { setView('moments'); setActiveMoment(undefined); setCreatingMoment(true) }} onFindMoments={() => void generateMoments()} aiEnabled={aiEnabled} generatingMoments={generatingMoments} canUpload={connected && !demoMode} onUploaded={loadAllMedia} onLogout={onLogout} />
      <main>
        {view !== 'moments' && <PageHeading view={view} album={activeAlbum} albumCount={albums.length} mediaCount={view === 'favorites' || view === 'trash' ? visibleMedia.length : media.length} favoriteCount={media.filter((item) => item.favorite && !item.deletedAt).length} onBack={showAlbums} onEdit={() => openEditAlbum()} onAddImages={() => openEditAlbum(true)} />}
        {!['storage', 'moments'].includes(view) && !activeAlbum && <LibraryToolbar view={view} query={query} filter={filter} sort={sort} grouping={grouping} onQueryChange={setQuery} onFilterChange={setFilter} onSortChange={setSort} onGroupingChange={setGrouping} />}
        {view !== 'moments' && <SelectionBar count={selected.length} albums={albums} targetAlbumId={targetAlbumId} onTargetChange={setTargetAlbumId} onAdd={addSelectedToAlbum} onClear={() => setSelected([])} />}
        {view === 'moments' ? <MomentsView moments={moments} activeMoment={activeMoment} loading={momentsLoading} onOpen={(id) => void openMoment(id)} onBack={() => setActiveMoment(undefined)} onViewMedia={setViewerMediaId} media={media} aiEnabled={aiEnabled} onCreate={createMoment} onUpdate={updateMoment} onDelete={deleteMoment} onAddMedia={addMomentMedia} onRemoveMedia={removeMomentMedia} onSuggestMetadata={suggestMomentMetadata} creating={creatingMoment} onCreatingChange={setCreatingMoment} />
          : view === 'albums'
          ? <AlbumGrid albums={visibleAlbums} onOpen={(id) => void openAlbum(id)} onMove={moveAlbum} />
          : view === 'map' ? <MediaMap media={visibleMedia} onView={setViewerMediaId} />
          : view === 'storage' ? storageStats && <StorageDashboard stats={storageStats} />
          : activeAlbum ? <AlbumDetailGallery album={activeAlbum} media={visibleMedia} selected={selected} onToggle={toggleSelection} onRemove={removeFromActiveAlbum} onSetCover={setAlbumCover} onFavorite={setMediaFavorite} onTrash={trashMedia} onDelete={deleteMedia} onRestore={restoreMedia} onShare={openShare} onView={setViewerMediaId} />
          : <MediaTimeline media={visibleMedia} grouping={grouping} selected={selected} canRemove={false} canDelete={view === 'media'} inTrash={view === 'trash'} coverMediaId={undefined} onToggle={toggleSelection} onRemove={removeFromActiveAlbum} onSetCover={setAlbumCover} onFavorite={setMediaFavorite} onTrash={trashMedia} onDelete={deleteMedia} onRestore={restoreMedia} onShare={openShare} onView={setViewerMediaId} />}
        {!['map', 'storage', 'moments'].includes(view) && (view === 'albums' ? visibleAlbums.length : visibleMedia.length) === 0 && (
          <section className="empty-state"><Search size={28} /><h2>No memories found</h2><p>Try a different title, tag, date, or filter.</p></section>
        )}
      </main>
      <CreateAlbumDialog open={showCreate} name={newAlbumName} onNameChange={setNewAlbumName} onClose={() => setShowCreate(false)} onCreate={createAlbum} />
      <ShareMediaDialog open={Boolean(shareMediaId)} username={shareUsername} permission={sharePermission} error={shareError} pending={sharing} onUsernameChange={(value) => { setShareUsername(value); setShareError(undefined) }} onPermissionChange={(value) => { setSharePermission(value); setShareError(undefined) }} onClose={() => { if (!sharing) setShareMediaId(null) }} onShare={() => void shareMedia()} />
      {activeAlbum && <EditAlbumDialog open={showEditAlbum} album={activeAlbum} media={media} name={editAlbumName} description={editAlbumDescription} coverMediaId={editCoverMediaId} onNameChange={setEditAlbumName} onDescriptionChange={setEditAlbumDescription} onCoverChange={setEditCoverMediaId} onClose={() => setShowEditAlbum(false)} onSave={saveAlbumEdits} aiEnabled={aiEnabled} aiPending={albumAiPending} onSuggestMetadata={() => void suggestAlbumMetadata()} startAdding={editAlbumAdding} onAddMedia={addMediaToActiveAlbum} />}
      {viewerItem && <MediaViewer item={viewerItem} hasMultiple={visibleMedia.length > 1} onClose={() => setViewerMediaId(null)} onPrevious={() => showViewerItem(-1)} onNext={() => showViewerItem(1)} />}
    </div>
  )
}

export default App
