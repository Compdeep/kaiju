<template>
  <div id="kaiju-app">
    <template v-if="auth.isAuthenticated">
      <AppHeader
        @open-admin="adminTab = $event; showAdmin = true"
        @open-settings="showSettings = true"
        @open-tools="showTools = true"
      />
      <transition name="fade" mode="out-in">
        <router-view />
      </transition>
      <transition name="modal">
        <AdminModal v-if="showAdmin" :initial-tab="adminTab" @close="showAdmin = false" />
      </transition>
      <transition name="modal">
        <SettingsModal v-if="showSettings" @close="showSettings = false" />
      </transition>
      <transition name="modal">
        <ToolsModal v-if="showTools" @close="showTools = false" />
      </transition>
    </template>
    <template v-else>
      <router-view />
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from './stores/auth'
import { useSettingsStore } from './stores/settings'
import AppHeader from './components/AppHeader.vue'
import AdminModal from './components/AdminModal.vue'
import SettingsModal from './components/SettingsModal.vue'
import ToolsModal from './components/ToolsModal.vue'

const auth = useAuthStore()
const settings = useSettingsStore()
const router = useRouter()
const showAdmin = ref(false)
const showSettings = ref(false)
const showTools = ref(false)
const adminTab = ref('scopes')

onMounted(() => {
  settings.loadTheme()
  if (auth.isAuthenticated) {
    auth.fetchMe()
  } else {
    router.replace('/login')
  }
})

// Watch for logout — redirect immediately
watch(() => auth.isAuthenticated, (authed) => {
  if (!authed) router.replace('/login')
})
</script>
