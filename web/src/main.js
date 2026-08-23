import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './styles/variables.css'
import './styles/global.css'
import { loadUIConfig } from './uiconfig'

// The configuration is fetched before the app mounts, not after. A component
// that rendered first would show kaiju's name and kaiju's colours for one frame
// and then replace them, and the sections would draw buttons before anything
// knew whether they exist.
loadUIConfig().finally(() => {
  const app = createApp(App)
  app.use(createPinia())
  app.use(router)
  app.mount('#app')
})
