<template>
  <div class="login-page">
    <div class="login-card" :class="{ shake: shaking }">
      <div class="login-mark">
        <svg viewBox="0 0 100 100" width="48" height="48" fill="none" stroke-width="4" stroke-linecap="round" stroke-linejoin="round">
          <g stroke="#818cf8">
            <g transform="translate(50,44) rotate(180)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(29,57) rotate(90)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(71,57) rotate(-90)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(50,68)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(50,79)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
          </g>
          <line x1="42" y1="52" x2="42" y2="60" stroke="#f472b6" stroke-width="2.5"/>
          <line x1="58" y1="52" x2="58" y2="60" stroke="#f472b6" stroke-width="2.5"/>
        </svg>
      </div>
      <h1>kaiju</h1>
      <p class="subtitle">sign in to continue</p>
      <form @submit.prevent="doLogin">
        <div class="form-group">
          <label>username</label>
          <input v-model="username" type="text" autocomplete="username" required autofocus />
        </div>
        <div class="form-group">
          <label>password</label>
          <input v-model="password" type="password" autocomplete="current-password" required />
        </div>
        <p v-if="error" class="error-msg">{{ error }}</p>
        <button type="submit" class="btn btn-primary login-btn" :disabled="loading">
          {{ loading ? 'signing in...' : 'sign in' }}
        </button>
      </form>
    </div>
  </div>
</template>

<script setup>
/**
 * desc: Login page with username/password form, error display, and shake animation on failed attempts
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const username = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)
const shaking = ref(false)

/**
 * desc: Attempt login with the entered credentials, redirecting to /chat on success or showing an error on failure
 * @returns {Promise<void>}
 */
async function doLogin() {
  error.value = ''
  loading.value = true
  try {
    await auth.login(username.value, password.value)
    router.push('/chat')
  } catch (err) {
    error.value = err.message
    shaking.value = true
    setTimeout(() => shaking.value = false, 300)
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page {
  display: flex; align-items: center; justify-content: center;
  min-height: 100vh; padding: 24px;
  background: var(--bg);
}
.login-card {
  width: 100%; max-width: 340px;
  padding: 40px 32px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
.login-mark { margin-bottom: 20px; color: var(--text); }
h1 { font-size: 20px; font-weight: 700; font-family: var(--mono); letter-spacing: -0.03em; margin-bottom: 4px; }
.subtitle { color: var(--text-muted); font-size: 13px; margin-bottom: 28px; }
.error-msg { color: var(--signal-red); font-size: 12px; margin-top: 6px; }
.login-btn { width: 100%; margin-top: 16px; justify-content: center; }
.shake { animation: shake 0.3s ease; }
</style>
