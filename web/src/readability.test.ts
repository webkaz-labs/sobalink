import { describe, expect, it } from 'vitest'

// Keep this test-only built-in out of the browser app's type dependencies.
const fileSystemModule = 'node:fs'
const { readFileSync } = await import(fileSystemModule) as { readFileSync(path: string, encoding: 'utf8'): string }
const styles = readFileSync('src/styles.css', 'utf8')

// Test the shipped theme values, rather than a second palette in test fixtures.
// Browser acceptance covers the cascade and the surfaces rendered by components.
function tokens(block: string) {
  return Object.fromEntries([...block.matchAll(/(--[\w-]+)\s*:\s*(#[\da-f]{3,8})\s*;/gi)].map(match => [match[1], match[2]]))
}
const light = tokens(styles.match(/:root\s*\{([^}]+)\}/)![1])
const darkBlocks = [...styles.matchAll(/html\[data-theme="(?:dark|system)"\]\s*\{([^}]+)\}/g)].map(match => tokens(match[1]))
const themes = { light, dark: { ...light, ...darkBlocks[0] }, systemDark: { ...light, ...darkBlocks[1] } }

function luminance(hex: string) {
  expect(hex, 'Every tested semantic color must resolve to a theme token').toMatch(/^#(?:[\da-f]{3}|[\da-f]{6})$/i)
  const full = hex.length === 4 ? hex.slice(1).split('').map(char => char + char).join('') : hex.slice(1)
  return full.match(/../g)!.map(channel => parseInt(channel, 16) / 255)
    .map(channel => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4)
    .reduce((sum, channel, index) => sum + channel * [.2126, .7152, .0722][index], 0)
}
function contrast(foreground: string, background: string) {
  const values = [luminance(foreground), luminance(background)].sort((a, b) => a - b)
  return (values[1] + .05) / (values[0] + .05)
}

describe('readable semantic colors in all themes', () => {
  it('keeps both parts of a focused invalid field contour in the validation color', () => {
    const invalidFocus = styles.match(/input\[aria-invalid="true"\]:focus\s*\{([^}]+)\}/)!
    // A grouped focus rule can acquire the composer's higher specificity when
    // optimized. Restore both colors together after that rule, not just outline.
    expect(invalidFocus.index).toBeGreaterThan(styles.indexOf('.composer:has(> textarea:focus)'))
    for (const property of ['border-color', 'outline-color']) {
      expect(invalidFocus[1]).toMatch(new RegExp(`${property}:\\s*var\\(--red\\)\\s*;`))
    }
  })

  it('defines a complete explicit dark theme and matching system-dark colors', () => {
    expect(darkBlocks).toHaveLength(2)
    for (const name of ['--text', '--muted', '--control-border', '--on-primary', '--disabled-text', '--surface', '--surface-hover']) {
      expect(darkBlocks[0][name], `${name} is explicit in dark mode`).toBeDefined()
      expect(darkBlocks[1][name], `${name} follows the same system-dark theme`).toBe(darkBlocks[0][name])
    }
  })

  for (const [theme, palette] of Object.entries(themes)) {
    it(`${theme}: text and control outlines survive neutral, hover, selected and feedback surfaces`, () => {
      for (const surface of ['--surface', '--surface-subtle', '--surface-hover', '--primary-soft', '--green-soft', '--amber-soft', '--red-soft']) {
        for (const text of ['--text', '--muted']) {
          expect(contrast(palette[text], palette[surface]), `${text} on ${surface}`).toBeGreaterThanOrEqual(4.5)
        }
        expect(contrast(palette['--control-border'], palette[surface]), `Control outline on ${surface}`).toBeGreaterThanOrEqual(3)
      }
    })

    it(`${theme}: primary actions, semantic labels, icons and focus rings retain contrast`, () => {
      for (const background of ['--primary', '--primary-hover']) {
        expect(contrast(palette['--on-primary'], palette[background]), `Primary label on ${background}`).toBeGreaterThanOrEqual(4.5)
      }
      for (const tone of ['primary', 'green', 'amber', 'red']) {
        const foreground = tone === 'primary' ? '--primary-text' : `--${tone}`
        expect(contrast(palette[foreground], palette[`--${tone}-soft`]), `${tone} status text`).toBeGreaterThanOrEqual(4.5)
      }
      for (const surface of ['--surface', '--surface-subtle', '--surface-hover', '--primary-soft']) {
        expect(contrast(palette['--primary'], palette[surface]), `Focus indicator on ${surface}`).toBeGreaterThanOrEqual(3)
        expect(contrast(palette['--muted'], palette[surface]), `Meaningful secondary icon on ${surface}`).toBeGreaterThanOrEqual(3)
      }
    })

    it(`${theme}: unavailable actions remain readable as a product usability choice`, () => {
      // WCAG exempts inactive controls from its contrast threshold. This stronger
      // product check prevents the previous opacity washout from returning.
      expect(contrast(palette['--disabled-text'], palette['--surface-hover'])).toBeGreaterThanOrEqual(4.5)
    })
  }
})
