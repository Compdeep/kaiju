import { defineStore } from 'pinia'
import axios from 'axios'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    user: null,
    token: null,
    loading: false,
    error: null,
  }),
  actions: {
    async login(email, password) {
      this.loading = true
      this.error = null
      try {
        const res = await axios.post('/api/auth/login', { email, password })
        this.user = res.data.user
        this.token = res.data.token
      } catch (e) {
        this.error = e.response?.data?.error || 'login failed'
        throw e
      } finally {
        this.loading = false
      }
    },
    logout() {
      this.user = null
      this.token = null
    },
  },
})
