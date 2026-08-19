/**
 * desc: What the interface is told about itself, fetched once before it draws
 * anything. The server answers /api/v1/ui with a brand, a theme and a set of
 * sections; see the ui package on the Go side for what each means and why the
 * sections default the way they do.
 *
 * A plain module rather than a Pinia store because it is read while the page is
 * still starting, before any component exists, and because nothing changes it
 * afterwards — a configuration that a running page could alter would be a
 * stylesheet a running page could alter.
 */

// What a page falls back to. Brand empty means kaiju's own name and mark;
// sections empty means every optional part is off, which is the same fail-safe
// the Go side applies. A failed fetch therefore hides buttons rather than
// showing ones whose routes may not be there.
const FALLBACK = { brand: {}, theme: {}, sections: {} }

let config = FALLBACK

/** desc: The whole configuration. @returns {Object} */
export function uiConfig() { return config }

/** desc: The name the interface calls itself. @returns {string} */
export function brandName() { return config.brand?.name || 'kaiju' }

/** desc: The logo to show in place of kaiju's mark, or ''. @returns {string} */
export function brandLogo() { return config.brand?.logo_url || '' }

/** desc: Whether to show the "powered by Kaiju" line. @returns {boolean} */
export function brandAttribution() { return !!config.brand?.attribution }

/**
 * desc: Whether an optional part of the interface exists.
 * @param {string} name - the section, e.g. 'users' or 'workspace'
 * @returns {boolean}
 */
export function sectionOn(name) { return !!config.sections?.[name] }

/** desc: The mode a visitor who has never chosen one sees. @returns {string} */
export function defaultThemeMode() { return config.theme?.default || '' }

// A CSS custom property name, and the characters a value may not contain.
// The Go side rejects both before serving, and this is the second lock: these
// values are written into a stylesheet, and a page should not depend on a
// server-side guarantee to avoid installing a rule nobody wrote.
const TOKEN_NAME = /^--[a-z0-9-]+$/
const FORBIDDEN = /[;{}<>\\"']/

/**
 * desc: Turn a token map into declarations, dropping anything malformed.
 * @param {Object} tokens - custom property name → value
 * @returns {string}
 */
function declarations(tokens) {
  return Object.entries(tokens || {})
    .filter(([name, value]) => TOKEN_NAME.test(name) && value && !FORBIDDEN.test(value))
    .map(([name, value]) => `${name}:${value};`)
    .join('')
}

/**
 * desc: Install the theme overrides as a stylesheet after the interface's own.
 * Later in the document and at the same specificity, so these values win
 * without any rule needing !important.
 * @param {Object} theme - the theme block of the configuration
 * @returns {void}
 */
function applyTheme(theme = {}) {
  const rules = []
  const light = declarations(theme.light)
  const dark = declarations(theme.dark)
  if (light) rules.push(`:root{${light}}`)
  if (dark) rules.push(`[data-theme="dark"]{${dark}}`)
  if (!rules.length) return

  const el = document.createElement('style')
  el.id = 'kaiju-theme-overrides'
  el.textContent = rules.join('\n')
  document.head.appendChild(el)
}

/**
 * desc: Apply the parts of the brand that live outside any component.
 * @param {Object} brand - the brand block of the configuration
 * @returns {void}
 */
function applyBrand(brand = {}) {
  if (brand.name) document.title = brand.name
}

/**
 * desc: Fetch the configuration and apply what does not belong to a component.
 * Awaited by main.js before the app mounts, so no component ever renders with
 * the wrong name or an unstyled first frame.
 * @returns {Promise<Object>} the configuration in force
 */
export async function loadUIConfig() {
  try {
    const res = await fetch('/api/v1/ui', { cache: 'no-store' })
    if (res.ok) {
      const body = await res.json()
      config = { ...FALLBACK, ...body }
    }
  } catch {
    // Left on the fallback. The interface is still usable under its own name.
  }
  applyTheme(config.theme)
  applyBrand(config.brand)
  return config
}
