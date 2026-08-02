<template>
  <!--
    The movable utility cluster. Exactly one instance is mounted at a time: it
    lives in the chat header while the files panel is closed, and hops into the
    panel header (via ComposablePanel's #header-actions slot) while it is open.
    Tools / Admin / Advanced / logout are emitted up to ChatPage (which owns the
    modals + popover-driven state); theme + the settings popover are handled here.
  -->
  <div class="header-tools">
    <button class="hdr-btn" title="Tools &amp; Skills" @click="$emit('open-tools')">
      <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/></svg>
    </button>
    <button class="hdr-btn" title="Users &amp; Scopes" @click="$emit('open-admin')">
      <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
    </button>
    <div class="popover-wrap">
      <button class="hdr-btn" :class="{ active: settingsOpen }" @click.stop="settingsOpen = !settingsOpen" title="Settings">
        <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
      </button>
      <Transition name="menu">
        <SettingsPopover v-if="settingsOpen" @open-advanced="onAdvanced" />
      </Transition>
    </div>
    <button class="hdr-btn" :title="settings.theme === 'dark' ? 'Light mode' : 'Dark mode'" @click="settings.toggleTheme()">
      <svg v-if="settings.theme !== 'dark'" viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
      <svg v-else viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/></svg>
    </button>
    <button class="hdr-btn" title="Sign out" @click="$emit('logout')">
      <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
    </button>
  </div>
</template>

<script setup>
/**
 * desc: Movable utility cluster (Tools / Admin / settings gear / theme / logout).
 * Rendered in the chat header when the panel is closed, and in the panel header
 * when it is open — a single instance either way.
 */
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useSettingsStore } from '../stores/settings'
import SettingsPopover from './SettingsPopover.vue'

const emit = defineEmits(['open-tools', 'open-admin', 'open-advanced', 'logout'])
const settings = useSettingsStore()
const settingsOpen = ref(false)

/** desc: Close the popover and ask ChatPage to open the full Settings modal. */
function onAdvanced() { settingsOpen.value = false; emit('open-advanced') }

/** desc: Close the popover on any outside click (the gear + popover stop it). */
function closePopover() { settingsOpen.value = false }
onMounted(() => document.addEventListener('click', closePopover))
onBeforeUnmount(() => document.removeEventListener('click', closePopover))
</script>

<style scoped>
.header-tools { display: flex; align-items: center; gap: 3px; }
.popover-wrap { position: relative; display: flex; }

/* Settings popover slide-in (targets the SettingsPopover root, which inherits
   this component's scope id). */
.menu-enter-active, .menu-leave-active { transition: all 0.12s ease; }
.menu-enter-from, .menu-leave-to { opacity: 0; transform: translateY(6px); }
.menu-enter-to, .menu-leave-from { opacity: 1; transform: translateY(0); }
</style>
