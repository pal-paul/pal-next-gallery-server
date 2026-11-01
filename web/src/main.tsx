import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { AuthGate } from './components/AuthGate.tsx'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AuthGate>{(logout, applianceOwner) => <App onLogout={logout} applianceOwner={applianceOwner} />}</AuthGate>
  </StrictMode>,
)
