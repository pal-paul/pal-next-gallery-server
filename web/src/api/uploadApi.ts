import { request } from './apiClient'

type UploadSession = {
  id: string
  chunk_size: number
  chunks: number
}

export type UploadProgress = {
  fileName: string
  fileIndex: number
  fileCount: number
  uploadedBytes: number
  totalBytes: number
}

const uploadFile = async (file: File, report: (uploadedBytes: number) => void) => {
  const session = await request<UploadSession>('/media/upload', {
    method: 'POST',
    body: JSON.stringify({
      filename: file.name,
      mime_type: file.type || 'application/octet-stream',
      size: file.size,
    }),
  })

  try {
    for (let part = 0; part < session.chunks; part += 1) {
      const start = part * session.chunk_size
      const end = Math.min(start + session.chunk_size, file.size)
      await request(`/media/upload/${session.id}/parts/${part}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/octet-stream' },
        body: file.slice(start, end),
      })
      report(end)
    }
    await request(`/media/upload/${session.id}/complete`, { method: 'POST' })
  } catch (error) {
    await request(`/media/upload/${session.id}`, { method: 'DELETE' }).catch(() => undefined)
    throw error
  }
}

export const uploadApi = {
  async upload(files: File[], onProgress: (progress: UploadProgress) => void) {
    const totalBytes = files.reduce((total, file) => total + file.size, 0)
    let completedBytes = 0
    for (const [fileIndex, file] of files.entries()) {
      await uploadFile(file, (fileBytes) => onProgress({
        fileName: file.name,
        fileIndex,
        fileCount: files.length,
        uploadedBytes: completedBytes + fileBytes,
        totalBytes,
      }))
      completedBytes += file.size
    }
  },
}
