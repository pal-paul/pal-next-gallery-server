import { CircleCheck, LoaderCircle, TriangleAlert, Upload } from 'lucide-react'
import { useRef, useState, type ChangeEvent } from 'react'
import { ApiError } from '../api/apiClient'
import { uploadApi, type UploadProgress } from '../api/uploadApi'

type Props = {
  disabled: boolean
  onUploaded: () => Promise<void>
}

type Notice = { kind: 'success' | 'error'; message: string }

const uploadError = (error: unknown) => {
  if (!(error instanceof ApiError)) return 'Upload failed. Please try again.'
  if (error.status === 403) return 'Uploads require a regular user with an upload folder.'
  if (error.status === 409) return 'One of these files is already in your gallery.'
  if (error.status === 429) return 'Your upload or storage quota has been reached.'
  return `Upload failed (HTTP ${error.status}).`
}

export function UploadButton({ disabled, onUploaded }: Props) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [progress, setProgress] = useState<UploadProgress>()
  const [notice, setNotice] = useState<Notice>()
  const uploading = Boolean(progress)

  const selectFiles = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ''
    if (files.length === 0) return
    setNotice(undefined)
    setProgress({ fileName: files[0].name, fileIndex: 0, fileCount: files.length, uploadedBytes: 0, totalBytes: files.reduce((total, file) => total + file.size, 0) })
    try {
      await uploadApi.upload(files, setProgress)
      await onUploaded().catch(() => undefined)
      setNotice({ kind: 'success', message: `${files.length} ${files.length === 1 ? 'file' : 'files'} uploaded` })
    } catch (error) {
      setNotice({ kind: 'error', message: uploadError(error) })
    } finally {
      setProgress(undefined)
    }
  }

  const percent = progress?.totalBytes ? Math.round(progress.uploadedBytes / progress.totalBytes * 100) : 0

  return (
    <>
      <input ref={inputRef} className="visually-hidden" type="file" accept="image/*,video/*" multiple onChange={(event) => void selectFiles(event)} />
      <button className="header-action" disabled={disabled || uploading} onClick={() => inputRef.current?.click()} aria-label={uploading ? `Uploading ${progress?.fileName}, ${percent}%` : 'Upload media'} title={uploading ? `Uploading ${percent}%` : 'Upload media'}>
        {uploading ? <LoaderCircle className="spin" size={21} /> : <Upload size={21} />}
      </button>
      {uploading && progress && (
        <div className="upload-notice" role="status">
          <LoaderCircle className="spin" size={17} />
          <div><strong>Uploading {progress.fileIndex + 1} of {progress.fileCount}</strong><span>{progress.fileName}</span></div>
          <progress value={progress.uploadedBytes} max={progress.totalBytes} aria-label={`${percent}% uploaded`} />
        </div>
      )}
      {notice && (
        <button className={`upload-notice ${notice.kind}`} onClick={() => setNotice(undefined)} aria-label="Dismiss upload notification">
          {notice.kind === 'success' ? <CircleCheck size={17} /> : <TriangleAlert size={17} />}
          <strong>{notice.message}</strong>
        </button>
      )}
    </>
  )
}
