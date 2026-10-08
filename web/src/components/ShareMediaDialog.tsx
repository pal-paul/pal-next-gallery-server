import { Share2, X } from 'lucide-react'

export type SharePermission = 'read' | 'write'

type Props = {
  open: boolean
  username: string
  permission: SharePermission
  onUsernameChange: (value: string) => void
  onPermissionChange: (value: SharePermission) => void
  onClose: () => void
  onShare: () => void
}

export function ShareMediaDialog({ open, username, permission, onUsernameChange, onPermissionChange, onClose, onShare }: Props) {
  if (!open) return null
  return (
    <div className="modal-backdrop" onMouseDown={onClose}>
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="share-title" onMouseDown={(event) => event.stopPropagation()}>
        <header><div className="modal-icon"><Share2 size={21} /></div><button className="icon-button" onClick={onClose} title="Close"><X size={19} /></button></header>
        <h2 id="share-title">Share your library</h2>
        <p>This grants access to your current and future media.</p>
        <label className="field-label">Gallery username<input autoFocus value={username} onChange={(event) => onUsernameChange(event.target.value)} placeholder="Username" /></label>
        <fieldset className="permission-options">
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
        <footer><button className="secondary-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={!username.trim()} onClick={onShare}><Share2 size={17} /> Share library</button></footer>
      </section>
    </div>
  )
}
