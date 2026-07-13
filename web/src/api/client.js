const BASE = ''

/**
 * desc: Retrieve the JWT token from localStorage for authenticated requests
 * @returns {string|null} The stored token or null if not set
 */
function getToken() {
  return localStorage.getItem('kaiju_token')
}

/**
 * desc: Send an authenticated HTTP request and handle 401 redirects and JSON parsing
 * @param {string} method - HTTP method (GET, POST, PUT, DELETE, PATCH)
 * @param {string} path - API endpoint path
 * @param {Object} [body] - Request body to serialize as JSON
 * @returns {Promise<Object>} Parsed JSON response
 */
// Default per-request timeout. Bounds every call so a stalled/hung request
// (e.g. a long agent run whose connection drops) can never leave the UI stuck
// in a loading state forever — the fetch aborts, the caller's finally runs, and
// the input unlocks. Generous enough for legitimate long agent runs.
const DEFAULT_TIMEOUT_MS = 300000 // 5 min

async function request(method, path, body, { timeoutMs = DEFAULT_TIMEOUT_MS } = {}) {
  const headers = { 'Content-Type': 'application/json' }
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  const controller = new AbortController()
  const timer = timeoutMs ? setTimeout(() => controller.abort(), timeoutMs) : null
  let res
  try {
    res = await fetch(BASE + path, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
      signal: controller.signal,
    })
  } catch (err) {
    if (err?.name === 'AbortError') throw new Error('request timed out')
    throw err
  } finally {
    if (timer) clearTimeout(timer)
  }

  if (res.status === 401) {
    localStorage.removeItem('kaiju_token')
    window.location.hash = '#/login'
    throw new Error('Unauthorized')
  }

  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
  return data
}

export const api = {
  /** @param {string} path @returns {Promise<Object>} */
  get: (path) => request('GET', path),
  /** @param {string} path @param {Object} body @param {Object} [opts] @returns {Promise<Object>} */
  post: (path, body, opts) => request('POST', path, body, opts),
  /** @param {string} path @param {Object} body @returns {Promise<Object>} */
  put: (path, body) => request('PUT', path, body),
  /** @param {string} path @returns {Promise<Object>} */
  del: (path) => request('DELETE', path),
  /** @param {string} path @param {Object} body @returns {Promise<Object>} */
  patch: (path, body) => request('PATCH', path, body),
  /**
   * desc: POST a non-JSON body (FormData, Blob, etc) — for multipart
   * uploads. Browser sets Content-Type with the right boundary.
   * @param {string} path
   * @param {FormData|Blob} body
   * @returns {Promise<Object>}
   */
  postRaw: async (path, body) => {
    const headers = {}
    const token = getToken()
    if (token) headers['Authorization'] = `Bearer ${token}`
    const res = await fetch(BASE + path, { method: 'POST', headers, body })
    if (res.status === 401) {
      localStorage.removeItem('kaiju_token')
      window.location.hash = '#/login'
      throw new Error('Unauthorized')
    }
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
    return data
  },
}

export default api
