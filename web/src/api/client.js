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
async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' }
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })

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
  /** @param {string} path @param {Object} body @returns {Promise<Object>} */
  post: (path, body) => request('POST', path, body),
  /** @param {string} path @param {Object} body @returns {Promise<Object>} */
  put: (path, body) => request('PUT', path, body),
  /** @param {string} path @returns {Promise<Object>} */
  del: (path) => request('DELETE', path),
  /** @param {string} path @param {Object} body @returns {Promise<Object>} */
  patch: (path, body) => request('PATCH', path, body),
}

export default api
