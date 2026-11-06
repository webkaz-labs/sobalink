import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import palette from '../../internal/uibrand/palette.json'
import paths from '../../internal/uibrand/icons.json'
import { Icon, Logo, type IconName } from './components/ui'

const fileSystemModule = 'node:fs'
const { readFileSync } = await import(fileSystemModule) as { readFileSync(path: string, encoding: 'utf8'): string }

describe('shared brand sources', () => {
  it('ships the shared palette in every CSS theme', () => {
    const css = readFileSync('src/brand-tokens.css', 'utf8')
    const blocks = [...css.matchAll(/(?:\:root|html\[data-theme="(?:dark|system)"\])\s*\{([^}]+)\}/g)]
    expect(blocks).toHaveLength(3)
    for (const [index, theme] of [palette.light, palette.dark, palette.dark].entries()) {
      const tokens = Object.fromEntries([...blocks[index][1].matchAll(/--([\w-]+):\s*(#[a-f0-9]+);/g)].map(match => [match[1], match[2]]))
      expect(tokens).toEqual(theme)
    }
    expect(readFileSync('src/styles.css', 'utf8')).toContain('@import "./brand-tokens.css"')
  })
  it('renders each shared icon with the same stroke and geometry', () => {
    for (const [name, path] of Object.entries(paths)) {
      const { container, unmount } = render(<Icon name={name as IconName} />)
      expect(container.querySelector('svg')).toHaveAttribute('viewBox', '0 0 24 24')
      expect(container.querySelector('svg')).toHaveAttribute('stroke-width', '1.7')
      expect(container.querySelector('path')).toHaveAttribute('d', path)
      unmount()
    }
  })
  it('uses the shared mark for the decorative logo and favicon', () => {
    const { container } = render(<Logo />)
    expect(container.querySelector('.brand-mark')).toHaveAttribute('aria-hidden', 'true')
    expect(container.textContent).toBe('sobalink.')
    expect(readFileSync('src/styles.css', 'utf8')).toContain('../../internal/uibrand/mark.svg')
    expect(readFileSync('src/styles.css', 'utf8')).toContain('.brand-mark::before { background: CanvasText; forced-color-adjust: none; }')
    expect(readFileSync('src/main.tsx', 'utf8')).toContain('../../internal/uibrand/mark.svg')
    expect(readFileSync('index.html', 'utf8')).toContain('rel="icon"')
  })
})
