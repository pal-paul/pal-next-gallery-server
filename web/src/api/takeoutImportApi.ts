import { ApiError, resolveApiUrl } from './apiClient'

export type TakeoutImportResult = {
  imported: number
  duplicates: number
  skipped: number
  albums: number
  momentsEnabled: boolean
}

export type TakeoutUploadProgress = {
  uploadedBytes: number
  totalBytes: number
}

export const takeoutImportApi = {
  importArchive(
    archive: File,
    momentsEnabled: boolean,
    onProgress: (progress: TakeoutUploadProgress) => void,
    signal?: AbortSignal,
  ): Promise<TakeoutImportResult> {
    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest()
      const removeAbortListener = () => signal?.removeEventListener('abort', abortRequest)
      const abortRequest = () => request.abort()

      request.open('POST', resolveApiUrl('/media/import/google-takeout'))
      request.withCredentials = true
      request.responseType = 'json'
      request.upload.addEventListener('progress', (event) => {
        if (event.lengthComputable) {
          onProgress({ uploadedBytes: event.loaded, totalBytes: event.total })
        }
      })
      request.upload.addEventListener('load', () => {
        onProgress({ uploadedBytes: archive.size, totalBytes: archive.size })
      })
      request.addEventListener('load', () => {
        removeAbortListener()
        if (request.status >= 200 && request.status < 300) {
          resolve(request.response as TakeoutImportResult)
          return
        }
        reject(new ApiError(request.status))
      })
      request.addEventListener('error', () => {
        removeAbortListener()
        reject(new Error('Takeout import request failed'))
      })
      request.addEventListener('abort', () => {
        removeAbortListener()
        reject(new DOMException('Takeout import canceled', 'AbortError'))
      })

      if (signal?.aborted) {
        reject(new DOMException('Takeout import canceled', 'AbortError'))
        return
      }
      signal?.addEventListener('abort', abortRequest, { once: true })

      const body = new FormData()
      body.append('archive', archive)
      body.append('momentsEnabled', String(momentsEnabled))
      request.send(body)
    })
  },
}
