import React from 'react'
import ReactDOM from 'react-dom/client'
import { App } from './App'
import './styles.css'
import markURL from '../../internal/uibrand/mark.svg'
import { bootstrapLocalAccess } from './ui-bootstrap'

const favicon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
if (favicon) favicon.href = markURL

const root = document.getElementById('root')!
root.textContent = navigator.language.startsWith('ja') ? 'ローカル画面を開いています…' : 'Opening the local UI…'
void bootstrapLocalAccess().then(() => {
  ReactDOM.createRoot(root).render(<React.StrictMode><App /></React.StrictMode>)
})
