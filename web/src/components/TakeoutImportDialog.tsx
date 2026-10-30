import { ArchiveRestore, CircleCheck, LoaderCircle, X } from 'lucide-react'
import { useRef, useState, type ChangeEvent } from 'react'
import { ApiError } from '../api/apiClient'
import { takeoutImportApi, type TakeoutImportResult, type TakeoutUploadProgress } from '../api/takeoutImportApi'

type Props = {
  onClose: () => void
  onImported: () => Promise<void>
}

type ImportPhase = 'idle' | 'uploading' | 'importing' | 'done'

const importError = (error: unknown) => {
  if (!(error instanceof ApiError)) return 'Import failed. Check your connection and try again.'
  if (error.status === 400) return 'This file is not a valid Google Takeout ZIP.'
  if (error.status === 403) return 'Imports require a regular user with an upload folder.'
  if (error.status === 413) return 'This archive is larger than the configured import limit.'
  if (error.status === 429) return 'Your upload or storage quota has been reached.'
  return `Import failed (HTTP ${error.status}).`
}

export function TakeoutImportDialog({ onClose, onImported }: Props) {
  const abortRef = useRef<AbortController>(null)
  const [archive, setArchive] = useState<File>()
  const [momentsEnabled, setMomentsEnabled] = useState(true)
  const [phase, setPhase] = useState<ImportPhase>('idle')
  const [progress, setProgress] = useState<TakeoutUploadProgress>()
  const [result, setResult] = useState<TakeoutImportResult>()
  const [error, setError] = useState<string>()
  const active = phase === 'uploading' || phase === 'importing'
  const percent = progress?.totalBytes ? Math.round(progress.uploadedBytes / progress.totalBytes * 100) : 0

  const selectArchive = (event: ChangeEvent<HTMLInputElement>) => {
    const selected = event.target.files?.[0]
    setArchive(selected)
    setResult(undefined)
    setError(selected && !selected.name.toLowerCase().endsWith('.zip') ? 'Choose a Google Takeout ZIP file.' : undefined)
  }

  const startImport = async () => {
    if (!archive || active || !archive.name.toLowerCase().endsWith('.zip')) return
    const controller = new AbortController()
    abortRef.current = controller
    setError(undefined)
    setResult(undefined)
    setProgress({ uploadedBytes: 0, totalBytes: archive.size })
    setPhase('uploading')
    try {
      const imported = await takeoutImportApi.importArchive(archive, momentsEnabled, (nextProgress) => {
        setProgress(nextProgress)
        setPhase(nextProgress.uploadedBytes >= nextProgress.totalBytes ? 'importing' : 'uploading')
      }, controller.signal)
      setResult(imported)
      setPhase('done')
      await onImported().catch(() => undefined)
    } catch (caught) {
      if (!controller.signal.aborted) setError(importError(caught))
      setPhase('idle')
    } finally {
      abortRef.current = null
    }
  }

  const close = () => {
    abortRef.current?.abort()
    onClose()
  }

  return (
    <div className="modal-backdrop" onMouseDown={() => !active && close()}>
      <section className="modal takeout-import-modal" role="dialog" aria-modal="true" aria-labelledby="takeout-import-title" aria-busy={active} onMouseDown={(event) => event.stopPropagation()}>
        <header>
          <div className="modal-icon"><ArchiveRestore size={21} /></div>
          <button className="icon-button" onClick={close} title={active ? 'Cancel import' : 'Close'}><X size={19} /></button>
        </header>
        <h2 id="takeout-import-title">Import Google Takeout</h2>
        <p>Restore photos, videos, metadata, and albums from one Takeout ZIP.</p>

        <label className="field-label takeout-file-label">
          Google Takeout ZIP
          <input type="file" accept=".zip,application/zip" disabled={active} onChange={selectArchive} />
        </label>

        <label className="takeout-moments-option">
          <input type="checkbox" checked={momentsEnabled} disabled={active} onChange={(event) => setMomentsEnabled(event.target.checked)} />
          <span><strong>Automatic Moments</strong><small>Include imported media when finding Moments</small></span>
        </label>

        {active && (
          <div className="takeout-progress" role="status">
            <div><span>{phase === 'uploading' ? 'Uploading archive' : 'Importing archive'}</span><strong>{phase === 'uploading' ? `${percent}%` : 'Processing'}</strong></div>
            <progress value={phase === 'uploading' ? progress?.uploadedBytes : undefined} max={progress?.totalBytes ?? 1} aria-label={phase === 'uploading' ? `${percent}% uploaded` : 'Import in progress'} />
            <small>{phase === 'uploading' ? archive?.name : 'Checking media, metadata, and albums...'}</small>
          </div>
        )}

        {result && (
          <div className="takeout-result" role="status">
            <CircleCheck size={20} />
            <strong>Import complete</strong>
            <span>{result.imported} imported</span><span>{result.duplicates} duplicates</span><span>{result.albums} albums</span><span>{result.skipped} skipped</span>
          </div>
        )}
        {error && <p className="dialog-error" role="alert">{error}</p>}

        <footer>
          <button className="secondary-button" onClick={close}>{active ? 'Cancel import' : result ? 'Close' : 'Cancel'}</button>
          {!result && <button className="primary-button" disabled={!archive || active || Boolean(error)} onClick={() => void startImport()}>{active ? <><LoaderCircle className="spin" size={17} /> Importing</> : 'Start import'}</button>}
        </footer>
      </section>
    </div>
  )
}
