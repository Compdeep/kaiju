<template>
  <div id="kaiju-app">
    <transition name="fade" mode="out-in">
      <router-view />
    </transition>
  </div>
</template>

<script setup>
/**
 * desc: App shell. No full-width header any more — the layout is three
 * full-height columns, each with its own header, owned by ChatPage. App only
 * applies the persisted theme and enforces the auth redirect.
 */
import { onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from './stores/auth'
import { useSettingsStore } from './stores/settings'

const auth = useAuthStore()
const settings = useSettingsStore()
const router = useRouter()

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
