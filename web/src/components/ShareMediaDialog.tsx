import { Share2, X } from 'lucide-react'

export type SharePermission = 'read' | 'write'

type Props = {
  open: boolean
  username: string
  permission: SharePermission
  error?: string
  pending: boolean
  onUsernameChange: (value: string) => void
  onPermissionChange: (value: SharePermission) => void
  onClose: () => void
  onShare: () => void
}

export function ShareMediaDialog({ open, username, permission, error, pending, onUsernameChange, onPermissionChange, onClose, onShare }: Props) {
  if (!open) return null
  return (
    <div className="modal-backdrop" onMouseDown={onClose}>
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="share-title" aria-busy={pending} onMouseDown={(event) => event.stopPropagation()}>
        <header><div className="modal-icon"><Share2 size={21} /></div><button className="icon-button" disabled={pending} onClick={onClose} title="Close"><X size={19} /></button></header>
        <h2 id="share-title">Share your library</h2>
        <p>This grants access to your current and future media.</p>
        <label className="field-label">Gallery username<input autoFocus disabled={pending} value={username} onChange={(event) => onUsernameChange(event.target.value)} placeholder="Username" /></label>
        <fieldset className="permission-options" disabled={pending}>
          <legend>Permission</legend>
          <label className={permission === 'read' ? 'selected' : ''}>
            <input type="radio" name="permission" checked={permission === 'read'} onChange={() => onPermissionChange('read')} />
            <span><strong>Read</strong><small>View, download, and favorite shared media.</small></span>
          </label>
          <label className={permission === 'write' ? 'selected' : ''}>
            <input type="radio" name="permission" checked={permission === 'write'} onChange={() => onPermissionChange('write')} />
            <span><strong>Write</strong><small>Includes read access and permission to move media to trash.</small></span>
          </label>
        </fieldset>
        {error && <p className="dialog-error" role="alert">{error}</p>}
        <footer><button className="secondary-button" disabled={pending} onClick={onClose}>Cancel</button><button className="primary-button" disabled={pending || !username.trim()} onClick={onShare}><Share2 size={17} /> {pending ? 'Sharing…' : 'Share library'}</button></footer>
      </section>
    </div>
  )
}
