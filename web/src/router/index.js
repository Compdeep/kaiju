import { createRouter, createWebHashHistory } from 'vue-router'
import { signInEnabled } from '../uiconfig'

const routes = [
  { path: '/login', name: 'login', component: () => import('../views/LoginPage.vue') },
  { path: '/', redirect: '/chat' },
  { path: '/chat/:id?', name: 'chat', component: () => import('../views/ChatPage.vue'), meta: { auth: true } },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

// A route marked auth needs a token only where signing in is a thing. On a
// mount without an authenticator there is no token to get and no route to get
// one from, so requiring one here would leave the page circling its own
// sign-in. The configuration is fetched before the app mounts, so this is
// answered by the time the first navigation is guarded.
router.beforeEach((to) => {
  if (to.meta.auth && signInEnabled() && !localStorage.getItem('kaiju_token')) {
    return { name: 'login' }
  }
})

export default router
