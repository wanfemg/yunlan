import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './ToolsApp'
import './tools-theme.css'
import { UI_PREVIEW_ONLY } from './api'
import { ToastProvider } from './components/Toast'

if (new URLSearchParams(window.location.search).get('next') === 'finance') {
  window.location.replace(UI_PREVIEW_ONLY ? '/money' : '/finance/')
} else {
  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <ToastProvider><App /></ToastProvider>
    </React.StrictMode>,
  )
}
