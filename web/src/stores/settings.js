import { defineStore } from 'pinia'
import { ref } from 'vue'

/** Reactive state for application settings (theme, display preferences). */
export const useSettingsStore = defineStore('settings', () => {
  const theme = ref(localStorage.getItem('kaiju_theme') || '')

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
