import React from 'react'
import ReactDOM from 'react-dom/client'
import { App } from './App'
import './styles.css'
import { bootstrapLocalAccess } from './ui-bootstrap'

const root = document.getElementById('root')!
root.textContent = navigator.language.startsWith('ja') ? 'ローカル画面を開いています…' : 'Opening the local UI…'
void bootstrapLocalAccess().then(() => {
  ReactDOM.createRoot(root).render(<React.StrictMode><App /></React.StrictMode>)
})
