import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from '@/app/App'
import { applyTheme } from '@/ui'
import './index.css'

// Apply the saved theme before the first render. No inline script: the
// server's CSP allows same-origin scripts only.
applyTheme()

const root = document.getElementById('root')
if (!root) throw new Error('missing #root element')

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
