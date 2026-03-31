<template>
  <header class="header">
    <router-link to="/chat" class="brand">
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.5"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>
    </router-link>

    <div class="header-right">
      <button class="nav-icon" title="Tools & Skills" @click="$emit('open-tools')">
        <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/></svg>
      </button>
      <button class="nav-icon" title="Users & Scopes" @click="$emit('open-admin', 'scopes')">
        <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
      </button>
      <button class="nav-icon" title="Settings" @click="$emit('open-settings')">
        <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8"><line x1="4" y1="21" x2="4" y2="14"/><line x1="4" y1="10" x2="4" y2="3"/><line x1="12" y1="21" x2="12" y2="12"/><line x1="12" y1="8" x2="12" y2="3"/><line x1="20" y1="21" x2="20" y2="16"/><line x1="20" y1="12" x2="20" y2="3"/><line x1="1" y1="14" x2="7" y2="14"/><line x1="9" y1="8" x2="15" y2="8"/><line x1="17" y1="16" x2="23" y2="16"/></svg>
      </button>
      <div class="sep"></div>
      <button class="nav-icon" :title="settings.theme === 'dark' ? 'Light mode' : 'Dark mode'" @click="settings.toggleTheme()">
        <svg v-if="settings.theme !== 'dark'" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
        <svg v-else viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/></svg>
      </button>
      <div class="info-wrap">
        <button class="nav-icon" title="Info" @click="showInfo = !showInfo">
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
        </button>
        <transition name="fade">
          <div v-if="showInfo" class="info-dropdown">
            <a href="/paper.html" target="_blank" class="info-link">
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
              <span>Academic Paper</span>
            </a>
            <a href="/intent-based-dag-execution-layer.html" target="_blank" class="info-link">
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>
              <span>Technical Overview</span>
            </a>
            <a href="https://github.com/cormacguerin/kaiju" target="_blank" class="info-link">
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M9 19c-5 1.5-5-2.5-7-3m14 6v-3.87a3.37 3.37 0 0 0-.94-2.61c3.14-.35 6.44-1.54 6.44-7A5.44 5.44 0 0 0 20 4.77 5.07 5.07 0 0 0 19.91 1S18.73.65 16 2.48a13.38 13.38 0 0 0-7 0C6.27.65 5.09 1 5.09 1A5.07 5.07 0 0 0 5 4.77a5.44 5.44 0 0 0-1.5 3.78c0 5.42 3.3 6.61 6.44 7A3.37 3.37 0 0 0 9 18.13V22"/></svg>
              <span>GitHub</span>
            </a>
          </div>
        </transition>
      </div>
      <button class="nav-icon" title="Sign out" @click="doLogout">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
      </button>
    </div>
  </header>
</template>

<script setup>
/**
 * desc: Application header bar with navigation icons for tools, admin, settings, theme toggle, info dropdown, and sign-out
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { useSettingsStore } from '../stores/settings'

defineEmits(['open-admin', 'open-settings', 'open-tools'])

const auth = useAuthStore()
const settings = useSettingsStore()
const router = useRouter()
const showInfo = ref(false)

// Close info dropdown on outside click
if (typeof window !== 'undefined') {
  window.addEventListener('click', (e) => {
    if (showInfo.value && !e.target.closest('.info-wrap')) showInfo.value = false
  })
}

/**
 * desc: Log the user out and redirect to the login page
 * @returns {void}
 */
function doLogout() { auth.logout(); router.push('/login') }
</script>

<style scoped>
.header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0 12px; height: 44px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.brand {
  color: var(--accent); display: flex; align-items: center; padding: 4px;
}
.header-right { display: flex; gap: 2px; align-items: center; }
.nav-icon {
  display: flex; align-items: center; justify-content: center;
  width: 34px; height: 34px; border-radius: var(--radius-sm);
  color: var(--text-muted); background: none; border: none;
  cursor: pointer; transition: all var(--transition);
}
.nav-icon:hover { color: var(--text); background: var(--surface-hover); }
.sep { width: 1px; height: 18px; background: var(--border); margin: 0 4px; }

/* Info dropdown */
.info-wrap { position: relative; }
.info-dropdown {
  position: absolute; top: 38px; right: 0; z-index: 50;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: var(--radius); box-shadow: var(--shadow-md);
  padding: 6px; min-width: 180px;
}
.info-link {
  display: flex; align-items: center; gap: 8px;
  padding: 7px 10px; border-radius: var(--radius-sm);
  font-size: 12px; font-family: var(--mono);
  color: var(--text-secondary); text-decoration: none;
  transition: all var(--transition);
}
.info-link:hover { background: var(--surface-hover); color: var(--text); }
.info-link.disabled { opacity: 0.4; pointer-events: none; }
.soon {
  font-size: 9px; font-weight: 600; text-transform: uppercase;
  color: var(--text-muted); letter-spacing: 0.05em;
  background: var(--surface-hover); padding: 1px 4px; border-radius: 3px;
}
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
