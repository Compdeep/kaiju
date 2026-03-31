import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '../api/client'

/** Reactive state for authentication. Manages JWT token and user profile. */
export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem('kaiju_token') || '')
  const user = ref(null)
  const isAuthenticated = computed(() => !!token.value)

  /**
   * desc: Authenticate a user with username and password, storing the JWT token
   * @param {string} username - The username to log in with
   * @param {string} password - The password to log in with
   * @returns {Promise<void>}
   */
  async function login(username, password) {
    const data = await api.post('/api/v1/auth/login', { username, password })
    token.value = data.token
    localStorage.setItem('kaiju_token', data.token)
    user.value = { username: data.username, max_intent: data.max_intent }
  }

  /**
   * desc: Clear the token and user data, removing the JWT from localStorage
   * @returns {void}
   */
  function logout() {
    token.value = ''
    user.value = null
    localStorage.removeItem('kaiju_token')
  }

  /**
   * desc: Fetch the current user's profile from the server; logs out on failure
   * @returns {Promise<void>}
   */
  async function fetchMe() {
    try {
      user.value = await api.get('/api/v1/auth/me')
    } catch { logout() }
  }

  return { token, user, isAuthenticated, login, logout, fetchMe }
})
