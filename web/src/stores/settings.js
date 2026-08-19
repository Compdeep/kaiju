import { defineStore } from 'pinia'
import { ref } from 'vue'
import { defaultThemeMode } from '../uiconfig'

/** Reactive state for application settings (theme, display preferences). */
export const useSettingsStore = defineStore('settings', () => {
  // A visitor's own choice outranks the configured default, and choosing the
  // light mode stores an empty string — which is a choice, not an absent one,
  // so the test is against null rather than against emptiness.
  const stored = localStorage.getItem('kaiju_theme')
  const theme = ref(stored !== null ? stored : defaultThemeMode())

  /**
   * desc: Toggle between dark and default theme, persisting the choice to localStorage
   * @returns {void}
   */
  function toggleTheme() {
    theme.value = theme.value === 'dark' ? '' : 'dark'
    document.documentElement.dataset.theme = theme.value
    localStorage.setItem('kaiju_theme', theme.value)
  }

  /**
   * desc: Apply the persisted theme to the document element on startup
   * @returns {void}
   */
  function loadTheme() {
    if (theme.value) {
      document.documentElement.dataset.theme = theme.value
    }
  }

  return { theme, toggleTheme, loadTheme }
})
