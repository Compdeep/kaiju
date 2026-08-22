<template>
  <div class="chat-page" @mousemove="onDrag" @mouseup="onDragEnd" @mouseleave="onDragEnd">
    <!-- Col 1: Session sidebar -->
    <div class="sidebar" :style="{ width: sidebarW + 'px' }" :class="{ collapsed: sidebarCollapsed }">
      <router-link to="/chat" class="col-header sidebar-header">
        <!-- A supplied logo replaces the drawn mark. The drawn one recolours
             itself by mode; an image cannot, so an application supplying one
             supplies a mark that reads in both. -->
        <img v-if="brandLogo()" :src="brandLogo()" :alt="brandName()" width="38" height="38" class="brand-logo" />
        <svg v-else viewBox="0 11 100 100" width="38" height="38" fill="none" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" class="kaiju-logo" :class="{ dark: settings.theme === 'dark' }">
          <g class="k-body">
            <g transform="translate(50,44) rotate(180)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(29,57) rotate(90)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(71,57) rotate(-90)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(50,68)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            <g transform="translate(50,79)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
          </g>
          <line x1="42" y1="52" x2="42" y2="60" class="k-eye" stroke-width="3"/>
          <line x1="58" y1="52" x2="58" y2="60" class="k-eye" stroke-width="3"/>
        </svg>
      </router-link>
      <template v-if="!sidebarCollapsed">
        <button class="sidebar-new" @click="chat.createSession()">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
          <span>New chat</span>
        </button>
        <div class="session-list">
          <div
            v-for="s in sessions.sessions" :key="s.id"
            :class="['session-item', { active: s.id === sessions.sessionId }]"
            @click="chat.switchSession(s.id)"
          >
            <span class="session-title">{{ s.title || 'Untitled' }}</span>
            <button class="session-del" @click.stop="chat.deleteSession(s.id)" title="Delete">
              <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            </button>
          </div>
        </div>
      </template>
      <button class="collapse-btn sidebar-collapse" @click="toggleSidebar" :title="sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
          <polyline v-if="sidebarCollapsed" points="9 18 15 12 9 6"/>
          <polyline v-else points="15 18 9 12 15 6"/>
        </svg>
      </button>
    </div>

    <!-- Gutter 1: sidebar ↔ chat -->
    <div
      class="gutter"
      :class="{ active: dragging === 'sidebar' }"
      @mousedown.prevent="startDrag('sidebar')"
    ></div>

    <!-- Col 2: Chat panel -->
    <div class="chat-panel">
      <div class="col-header chat-header">
        <div class="chat-title" :title="currentTitle">{{ currentTitle }}</div>
        <div class="chat-header-actions">
          <IntentSelector ref="intentSelectorRef" />
          <ModelSelector ref="modelSelectorRef" />
          <!-- Only where the workspace section exists: its routes are not
               registered when it does not, and the panel would open empty. -->
          <button v-if="sectionOn('workspace')" class="hdr-btn" :class="{ active: panel.open }" @click="panel.toggle()" :title="panel.open ? 'Close panel' : 'Open panel'">
            <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="15" y1="3" x2="15" y2="21"/></svg>
          </button>
          <!-- Utility cluster: lives here only while the panel is CLOSED; when the
               panel is open it renders in the panel header (see ComposablePanel
               slot below). Exactly one instance is mounted at a time. -->
          <template v-if="!panel.open">
            <div class="hdr-sep"></div>
            <HeaderTools
              @open-tools="showTools = true"
              @open-admin="adminTab = 'scopes'; showAdmin = true"
              @open-advanced="showSettings = true"
              @logout="doLogout"
            />
          </template>
        </div>
      </div>

      <div class="chat-messages" ref="messagesEl">
        <div v-if="!sessions.messages.length" class="empty-state">
          <svg viewBox="0 0 100 100" width="40" height="40" fill="none" stroke="var(--text-muted)" stroke-width="4" stroke-linecap="round" stroke-linejoin="round" style="margin-bottom:12px;opacity:0.5">
            <g>
              <g transform="translate(50,44) rotate(180)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
              <g transform="translate(29,57) rotate(90)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
              <g transform="translate(71,57) rotate(-90)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
              <g transform="translate(50,68)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
              <g transform="translate(50,79)"><polyline points="-16,0 -8,14 0,0 8,14 16,0"/></g>
            </g>
            <line x1="42" y1="52" x2="42" y2="60" stroke-width="2.5"/>
            <line x1="58" y1="52" x2="58" y2="60" stroke-width="2.5"/>
          </svg>
          <p>Start a conversation</p>
        </div>

        <template v-for="(msg, i) in sessions.messages" :key="i">
          <!-- Show saved trace above its assistant message -->
          <DAGTrace
            v-if="msg.role === 'assistant' && msg.trace && msg.trace.length"
            :nodes="msg.trace"
            :running="false"
          />
          <div v-if="msg.gaps && msg.gaps.length" class="gaps-strip">
            <span class="gaps-icon">!</span>
            <span v-for="(gap, gi) in msg.gaps" :key="gi" class="gap-tag">{{ gap }}</span>
          </div>
          <div :class="['msg', msg.role]">
            <div class="msg-meta">
              <span class="msg-author">{{ msg.role === 'user' ? 'you' : brandName() }}</span>
              <span class="msg-tools" v-if="editing !== i">
                <button v-if="msg.id && !sessions.loading" class="msg-tool" title="Edit this message" @click="startEdit(i, msg)">✎</button>
                <button v-if="msg.role === 'assistant' && i === lastAssistantIndex && !sessions.loading" class="msg-tool" title="Regenerate reply" @click="chat.regenerate()">↻</button>
                <button v-if="msg.id" class="msg-tool" title="Delete this message (and everything after)" @click="deleteMsg(msg)">🗑</button>
              </span>
            </div>
            <div v-if="editing === i" class="msg-edit">
              <textarea v-model="editBuf" class="msg-edit-area" rows="4"></textarea>
              <div class="msg-edit-actions">
                <button class="msg-edit-save" @click="saveEdit(msg)">save</button>
                <button class="msg-edit-cancel" @click="editing = null">cancel</button>
              </div>
            </div>
            <div v-else class="msg-content md" v-html="renderMd(msg.content)"></div>
          </div>
        </template>

        <!-- Show trace live while still thinking (no response yet) -->
        <div v-if="sessions.loading && dag.nodes.length" class="trace-click" @click="enableInterject">
          <DAGTrace
            :nodes="dag.nodes"
            :running="dag.running"
          />
        </div>

        <div v-if="sessions.loading" class="msg assistant">
          <div class="msg-meta">
            <span class="msg-author">{{ brandName() }}</span>
            <span v-if="!dag.streamingVerdict" class="thinking-scan"></span>
          </div>
          <details v-if="dag.streamingReasoning" class="thinking-panel" :open="!dag.streamingVerdict">
            <summary><span class="think-dot"></span> thinking</summary>
            <div class="thinking-body">{{ dag.streamingReasoning }}</div>
          </details>
          <div v-if="dag.streamingVerdict" class="msg-content md" v-html="renderMd(dag.streamingVerdict)"></div>
        </div>

        <!-- Breathing room: pushes content up so agent response starts visible -->
        <div class="msg-spacer" :class="{ active: sessions.loading }"></div>
      </div>

      <div class="chat-compose"
        @dragover.prevent="dragOver = true"
        @dragleave.prevent="dragOver = false"
        @drop.prevent="onDrop">
        <div v-if="dragOver" class="drop-overlay">drop files to attach</div>
        <div v-if="sessions.attachments && sessions.attachments.length" class="chip-strip">
          <UploadChip
            v-for="(att, i) in sessions.attachments"
            :key="(att.path || att.filename) + ':' + i"
            :att="att"
            @remove="onRemoveAttachment(att)"
          />
        </div>
        <div class="compose-row">
          <!-- Transient run controls: interject + stop, only live while a run is
               in flight. The five persistent pills (mode/intent/aggregator/
               execution/chat) now live in the chat-header settings popover. -->
          <div class="compose-controls">
            <!-- Interject chip -->
            <Transition name="chip">
              <span v-if="dag.interjectMode" class="interject-chip" title="Interject — send guidance into the running query" @click="dag.interjectMode = false">
                <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><polyline points="9 10 4 15 9 20"/><path d="M20 4v7a4 4 0 01-4 4H4"/></svg>
                <span class="ij-x">&times;</span>
              </span>
            </Transition>
            <!-- Stop button — manual cancel for the running query. Sits right of
                 the interject chip. There is no automatic idle/timeout abort any
                 more, so this is how a genuinely stuck run gets stopped. -->
            <Transition name="chip">
              <button v-if="sessions.loading" class="stop-btn" @click.stop="chat.stop()" title="Stop the running query">
                <svg viewBox="0 0 24 24" width="11" height="11" fill="currentColor" stroke="none"><rect x="5" y="5" width="14" height="14" rx="2.5"/></svg>
              </button>
            </Transition>
          </div>
          <!-- Input -->
          <textarea
            ref="composeInput"
            v-model="input"
            class="compose-input"
            rows="1"
            :placeholder="dag.interjectMode ? 'interject into running query...' : 'ask anything...'"
            @input="autoGrow"
            @keydown.enter.exact.prevent="send"
          ></textarea>
          <!-- Right-side actions: attach + send only. Compact history and the
               panel toggle moved to the chat header; run controls to the gear. -->
          <UploadButton :disabled="sessions.loading" @files="onFilesPicked" />
          <button class="btn-icon send" @click="send" :disabled="!input.trim() || sessions.loading">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
          </button>
        </div>
      </div>
    </div>

    <!-- Gutter 2: chat ↔ panel (only when panel open) -->
    <div
      v-if="panel.open"
      class="gutter"
      :class="{ active: dragging === 'panel' }"
      @mousedown.prevent="startDrag('panel')"
    ></div>

    <!-- Col 3: Composable panel. The utility cluster hops into its header (via
         the #header-actions slot) whenever the panel is open. -->
    <ComposablePanel v-if="panel.open && sectionOn('workspace')">
      <template #header-actions>
        <HeaderTools
          @open-tools="showTools = true"
          @open-admin="adminTab = 'scopes'; showAdmin = true"
          @open-advanced="showSettings = true"
          @logout="doLogout"
        />
      </template>
    </ComposablePanel>

    <!-- Modals (moved here from App.vue with the header removal — opened from the
         chat-header gear and the panel-header Tools / Users&Scopes buttons) -->
    <transition name="modal">
      <AdminModal v-if="showAdmin && sectionOn('users')" :initial-tab="adminTab" @close="showAdmin = false" />
    </transition>
    <transition name="modal">
      <SettingsModal v-if="showSettings" @close="onSettingsClose" />
    </transition>
    <transition name="modal">
      <ToolsModal v-if="showTools" @close="showTools = false" />
    </transition>
  </div>
</template>

<script setup>
/**
 * desc: Main chat page with resizable sidebar, message thread, DAG trace display, composable panel, and interjection support
 */
import { ref, computed, nextTick, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionsStore } from '../stores/sessions'
import { useDagStore } from '../stores/dag'
import { usePanelStore } from '../stores/panel'
import { useAuthStore } from '../stores/auth'
import { useSettingsStore } from '../stores/settings'
import * as chat from '../services/chat'
import * as tools from '../services/tools'
import { marked } from 'marked'
import hljs from 'highlight.js/lib/core'
import hljsJavascript from 'highlight.js/lib/languages/javascript'
import hljsPython from 'highlight.js/lib/languages/python'
import hljsGo from 'highlight.js/lib/languages/go'
import hljsBash from 'highlight.js/lib/languages/bash'
import hljsJson from 'highlight.js/lib/languages/json'
import hljsCss from 'highlight.js/lib/languages/css'
import hljsXml from 'highlight.js/lib/languages/xml'
import hljsYaml from 'highlight.js/lib/languages/yaml'
import hljsSql from 'highlight.js/lib/languages/sql'
import hljsRust from 'highlight.js/lib/languages/rust'
import hljsCpp from 'highlight.js/lib/languages/cpp'
import hljsTypescript from 'highlight.js/lib/languages/typescript'
import DAGTrace from '../components/DAGTrace.vue'
import ComposablePanel from '../components/ComposablePanel.vue'
import UploadButton from '../components/UploadButton.vue'
import UploadChip from '../components/UploadChip.vue'
import IntentSelector from '../components/IntentSelector.vue'
import ModelSelector from '../components/ModelSelector.vue'
import HeaderTools from '../components/HeaderTools.vue'
import AdminModal from '../components/AdminModal.vue'
import { brandName, brandLogo, sectionOn } from '../uiconfig'
import { renderMarkdown } from '../markdown'
import SettingsModal from '../components/SettingsModal.vue'
import ToolsModal from '../components/ToolsModal.vue'
import * as uploads from '../services/uploads'

hljs.registerLanguage('javascript', hljsJavascript)
hljs.registerLanguage('js', hljsJavascript)
hljs.registerLanguage('python', hljsPython)
hljs.registerLanguage('go', hljsGo)
hljs.registerLanguage('bash', hljsBash)
hljs.registerLanguage('sh', hljsBash)
hljs.registerLanguage('json', hljsJson)
hljs.registerLanguage('css', hljsCss)
hljs.registerLanguage('xml', hljsXml)
hljs.registerLanguage('html', hljsXml)
hljs.registerLanguage('yaml', hljsYaml)
hljs.registerLanguage('sql', hljsSql)
hljs.registerLanguage('rust', hljsRust)
hljs.registerLanguage('cpp', hljsCpp)
hljs.registerLanguage('typescript', hljsTypescript)
hljs.registerLanguage('ts', hljsTypescript)

const renderer = new marked.Renderer()
renderer.link = function ({ href, title, text }) {
  const t = title ? ` title="${title}"` : ''
  return `<a href="${href}"${t} target="_blank" rel="noopener">${text}</a>`
}

marked.setOptions({
  breaks: true,
  gfm: true,
  renderer,
  highlight: (code, lang) => {
    if (lang && hljs.getLanguage(lang)) {
      try { return hljs.highlight(code, { language: lang }).value } catch {}
    }
    try { return hljs.highlightAuto(code).value } catch {}
    return code
  },
})

const route = useRoute()
const router = useRouter()
const sessions = useSessionsStore()
const dag = useDagStore()
const panel = usePanelStore()
const auth = useAuthStore()
const settings = useSettingsStore()
const input = ref('')
const composeInput = ref(null)     // <textarea> ref, for auto-grow
const intentSelectorRef = ref(null) // <IntentSelector> ref, to re-sync after Advanced settings
const modelSelectorRef = ref(null) // <ModelSelector> ref, to re-sync after Advanced settings

// ── Chat-header state ──
const showAdmin = ref(false)
const showSettings = ref(false)
const showTools = ref(false)
const adminTab = ref('scopes')

// Active session's title (drives the center-header heading).
const currentTitle = computed(() => {
  const s = sessions.sessions.find(x => x.id === sessions.sessionId)
  return (s && s.title) || 'New chat'
})

/**
 * desc: Grow the compose textarea with its content up to a max, then scroll.
 *       Reset to auto first so it also shrinks back when text is removed.
 * @returns {void}
 */
function autoGrow() {
  const el = composeInput.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 200) + 'px'
}

/**
 * desc: Sign out and return to the login screen.
 * @returns {void}
 */
function doLogout() { auth.logout(); router.push('/login') }

/**
 * desc: Close the Advanced-settings modal and re-sync the header model selector
 *       (the modal can also change the reasoning model).
 * @returns {void}
 */
function onSettingsClose() {
  showSettings.value = false
  modelSelectorRef.value?.reload()
  intentSelectorRef.value?.reload()
}

// ── Inline message editing / regenerate ──
const editing = ref(null)   // index of the message being edited, or null
const editBuf = ref('')     // edit textarea buffer
const lastAssistantIndex = computed(() => {
  const m = sessions.messages
  for (let i = m.length - 1; i >= 0; i--) if (m[i].role === 'assistant') return i
  return -1
})
function startEdit(i, msg) { editing.value = i; editBuf.value = msg.content }
async function deleteMsg(msg) {
  if (!msg.id) return
  try {
    await chat.deleteMessage(sessions.sessionId, msg.id)
  } catch (err) {
    alert('Delete failed: ' + err.message)
  }
}
async function saveEdit(msg) {
  if (!msg.id) { editing.value = null; return }
  try {
    await chat.editMessage(sessions.sessionId, msg.id, editBuf.value)
    msg.content = editBuf.value  // reflect immediately; server now holds it
  } catch (err) {
    alert('Edit failed: ' + err.message)
  }
  editing.value = null
}
const messagesEl = ref(null)
const dragOver = ref(false)

/**
 * desc: Handle files chosen via the + button picker — kick off concurrent
 * uploads, each driving its own chip through pending → ready/error.
 * @param {File[]} files
 */
function onFilesPicked(files) {
  uploads.uploadMany(files)
}

/**
 * desc: Handle a native drag-drop onto the compose area. Picks up dataTransfer
 * files and runs the same upload pipeline as the file picker.
 * @param {DragEvent} e
 */
function onDrop(e) {
  dragOver.value = false
  const files = Array.from(e.dataTransfer?.files || [])
  if (files.length) uploads.uploadMany(files)
}

/**
 * desc: Remove an attachment from the chip strip and the server.
 * @param {Object} att
 */
function onRemoveAttachment(att) {
  uploads.removeAttachment(att)
}

// Load sessions on mount; restore active session if saved
onMounted(async () => {
  await chat.loadSessions()

  // Session from URL takes priority, then localStorage, then most recent
  const urlSessionId = route.params.id
  if (urlSessionId && sessions.sessions.find(s => s.id === urlSessionId)) {
    await chat.switchSession(urlSessionId)
  } else if (sessions.sessionId && sessions.sessions.find(s => s.id === sessions.sessionId)) {
    await chat.switchSession(sessions.sessionId)
    // Sync URL to match the loaded session
    if (sessions.sessionId) router.replace({ name: 'chat', params: { id: sessions.sessionId } })
  } else if (sessions.sessions.length > 0) {
    await chat.switchSession(sessions.sessions[0].id)
    router.replace({ name: 'chat', params: { id: sessions.sessions[0].id } })
  }
  scrollToBottom() // open at the latest turn
  tools.connect()
})

// ── Resize logic ──────────────────────────────────────────────────────────────
const sidebarW = ref(parseInt(localStorage.getItem('kaiju_sidebar_w')) || 220)
const sidebarCollapsed = ref(false)
const dragging = ref(null)  // null | 'sidebar' | 'panel'
const dragStartX = ref(0)
const dragStartW = ref(0)

const SIDEBAR_MIN = 160
const SIDEBAR_MAX = 360
const PANEL_MIN = 320
const PANEL_MAX = 900
const CHAT_MIN = 400

/**
 * desc: Toggle the sidebar between collapsed and expanded states
 * @returns {void}
 */
function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
}

/**
 * desc: Begin a drag-resize operation on the sidebar or panel gutter
 * @param {string} target - Which panel to resize ('sidebar' or 'panel')
 * @returns {void}
 */
function startDrag(target) {
  dragging.value = target
  dragStartX.value = 0 // set on first move
  dragStartW.value = target === 'sidebar' ? sidebarW.value : panel.width
}

/**
 * desc: Handle mousemove during a drag-resize, clamping width within min/max bounds
 * @param {MouseEvent} e - The mouse event
 * @returns {void}
 */
function onDrag(e) {
  if (!dragging.value) return
  if (!dragStartX.value) {
    dragStartX.value = e.clientX
    return
  }

  const dx = e.clientX - dragStartX.value

  if (dragging.value === 'sidebar') {
    const w = Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, dragStartW.value + dx))
    sidebarW.value = w
    localStorage.setItem('kaiju_sidebar_w', String(w))
  } else if (dragging.value === 'panel') {
    // Panel grows to the left, so invert dx
    const w = Math.min(PANEL_MAX, Math.max(PANEL_MIN, dragStartW.value - dx))
    panel.setWidth(w)
  }
}

/**
 * desc: End the current drag-resize operation and reset drag state
 * @returns {void}
 */
function onDragEnd() {
  if (dragging.value) {
    dragging.value = null
    dragStartX.value = 0
  }
}

// ── Chat logic (unchanged) ────────────────────────────────────────────────────

/**
 * desc: Enable interjection mode when a query is loading, allowing the user to inject messages
 * @returns {void}
 */
function enableInterject() {
  if (sessions.loading) dag.interjectMode = true
}

/**
 * desc: Send the current input as a message or interjection, then scroll to the bottom
 * @returns {Promise<void>}
 */
async function send() {
  const text = input.value.trim()
  if (!text) return
  input.value = ''
  nextTick(autoGrow) // shrink the textarea back to one row

  const isInterject = sessions.loading && (dag.interjectMode || dag.running)

  if (isInterject) {
    await chat.interject(text)
  } else {
    // Don't await — send starts loading, spacer expands via CSS
    chat.send(text)
    // Scroll down once after spacer expands so the thinking indicator is visible
    scrollToBottom(260) // after spacer CSS transition (240ms)
  }

}

/**
 * desc: Scroll the message list to the newest message. Used on send and, so a
 *       chat opens at the latest turn, after loading/switching a session.
 * @param {number} delay - ms to wait after render before scrolling
 */
function scrollToBottom(delay = 60) {
  nextTick(() => {
    setTimeout(() => {
      if (messagesEl.value) messagesEl.value.scrollTop = messagesEl.value.scrollHeight
    }, delay)
  })
}



/**
 * desc: Convert markdown-formatted text to HTML using marked + highlight.js.
 * The sanitising lives in ../markdown.js, which explains why it has to.
 * @param {string} text - Raw markdown text
 * @returns {string} HTML string with syntax-highlighted code blocks
 */
function renderMd(text) {
  return renderMarkdown(text)
}

// Watch for route changes (e.g. clicking a session in sidebar or browser back/forward)
watch(() => route.params.id, async (newId) => {
  if (newId && newId !== sessions.sessionId) {
    await chat.switchSession(newId)
    scrollToBottom() // jump to the latest turn when opening a chat
  }
})

// Sync URL when session changes (new session created, sidebar click, etc.)
watch(() => sessions.sessionId, (newId) => {
  if (newId && route.params.id !== newId) {
    router.replace({ name: 'chat', params: { id: newId } })
  }
})
</script>

<style scoped>
.chat-page {
  display: flex; height: 100vh;
  user-select: none;
}
.chat-page * { user-select: text; }

/* ── Column headers ─────────────────────────────────────────────────────────── */
/* Base .col-header (height / border / layout) is in global.css so all three
   columns line up. Per-column tweaks live here. */

/* Sidebar header: logo only */
.sidebar-header {
  gap: 8px; justify-content: flex-start;
  text-decoration: none; cursor: pointer;
}
.sidebar.collapsed .sidebar-header { justify-content: center; padding: 0; }
/* Logo colours — mirror the old AppHeader brand (cyan light / indigo+pink dark) */
.brand-logo { width: 38px; height: 38px; object-fit: contain; flex-shrink: 0; }
.kaiju-logo { transition: filter 0.2s ease, transform 0.2s ease; flex-shrink: 0; }
.kaiju-logo .k-body { stroke: #4FC3F7; }
.kaiju-logo .k-eye { stroke: #4FC3F7; }
.sidebar-header:hover .kaiju-logo { filter: drop-shadow(0 0 8px #4FC3F7); transform: scale(1.06); }
.kaiju-logo.dark .k-body { stroke: #818cf8; }
.kaiju-logo.dark .k-eye { stroke: #f472b6; filter: drop-shadow(0 0 5px #f472b6); }
.sidebar-header:hover .kaiju-logo.dark { filter: drop-shadow(0 0 12px #f472b6); }

/* Chat header: title + action cluster (.hdr-btn / .hdr-sep are shared, in global.css) */
.chat-title {
  font-size: 13px; font-weight: 600; font-family: var(--display);
  letter-spacing: 0.03em; color: var(--text);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  flex: 1; min-width: 0; margin-right: 12px;
}
.chat-header-actions { display: flex; align-items: center; gap: 3px; flex-shrink: 0; }

/* ── Gutter (drag handle) ───────────────────────────────────────────────────── */
.gutter {
  width: 6px; min-width: 6px;
  cursor: col-resize;
  /* Invisible at rest; the divider line only appears on hover / while dragging. */
  background: transparent;
  transition: background var(--transition);
  position: relative;
  z-index: 2;
}
.gutter::before {
  content: ''; position: absolute;
  top: 0; bottom: 0; left: -3px; right: -3px;
}
.gutter:hover, .gutter.active { background: var(--gutter-hover); }
.gutter.active { background: var(--gutter-active); }

/* ── Sidebar ─────────────────────────────────────────────────────────────────── */
.sidebar {
  display: flex; flex-direction: column;
  background: var(--surface);
  /* No visible column line at rest — the gutter reveals the divider on hover. */
  border-right: 1px solid transparent;
  overflow: hidden;
  position: relative;
  min-width: 40px;
  transition: width 0.15s ease;
}
.sidebar.collapsed { width: 40px !important; min-width: 40px; }

.sidebar-new {
  display: inline-flex; align-items: center; gap: 6px;
  margin: 10px; padding: 6px 12px;
  border: none; border-radius: var(--radius-sm);
  background: var(--surface-hover);
  color: var(--text-secondary);
  font-size: 12px; font-family: var(--mono); cursor: pointer;
  transition: all var(--transition);
  width: auto;
}
.sidebar-new:hover { color: var(--accent); background: var(--accent-subtle); }
.session-list { flex: 1; overflow-y: auto; padding: 0 6px 10px; }
.session-item {
  display: flex; align-items: center; justify-content: space-between;
  padding: 7px 10px; border-radius: var(--radius-sm);
  cursor: pointer; transition: all var(--transition);
  margin-bottom: 1px;
}
.session-item:hover { background: var(--surface-hover); }
.session-item.active { background: var(--accent-subtle); }
.session-title {
  font-size: 12px; color: var(--text-secondary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  flex: 1;
}
.session-item.active .session-title { color: var(--accent); font-weight: 500; }
.session-del {
  opacity: 0; background: none; border: none; cursor: pointer;
  color: var(--text-muted); padding: 2px; display: flex;
  transition: opacity var(--transition);
}
.session-item:hover .session-del { opacity: 1; }
.session-del:hover { color: var(--signal-red); }

.collapse-btn {
  display: flex; align-items: center; justify-content: center;
  background: none; border: none; cursor: pointer;
  color: var(--text-muted); padding: 8px;
  transition: color var(--transition);
}
.collapse-btn:hover { color: var(--accent); }
.sidebar.collapsed .collapse-btn { margin: auto; }

/* ── Chat panel ──────────────────────────────────────────────────────────────── */
.chat-panel {
  flex: 1; min-width: 400px;
  display: flex; flex-direction: column;
  position: relative;
  background: var(--surface);
}

.chat-messages {
  flex: 1; overflow-y: auto;
  padding: 28px 32px 28px 36px;
  display: flex; flex-direction: column; gap: 24px;
}

.empty-state {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  flex: 1; color: var(--text-muted); font-size: 14px;
}

.msg { display: flex; flex-direction: column; gap: 4px; max-width: min(740px, 100%); min-width: 0; }
.msg-content { max-width: 100%; overflow-wrap: anywhere; }
.msg-meta { display: flex; align-items: center; gap: 6px; }
.msg-tools { display: inline-flex; gap: 4px; opacity: 0; transition: opacity .12s; }
.msg:hover .msg-tools { opacity: 1; }
.msg-tool { background: none; border: none; color: var(--text-muted); cursor: pointer; font-size: 12px; padding: 1px 4px; border-radius: 4px; line-height: 1; }
.msg-tool:hover { color: var(--accent); background: var(--surface); }
.msg-edit { display: flex; flex-direction: column; gap: 6px; }
.msg-edit-area { width: 100%; resize: vertical; font: inherit; font-size: 14px; line-height: 1.6; background: var(--surface); color: var(--text); border: 1px solid var(--line); border-radius: 6px; padding: 8px; }
.msg-edit-actions { display: flex; gap: 6px; }
.msg-edit-save, .msg-edit-cancel { font-size: 12px; padding: 3px 10px; border-radius: 5px; border: 1px solid var(--line); cursor: pointer; background: var(--surface); color: var(--text); }
.msg-edit-save { background: var(--accent); color: #fff; border-color: var(--accent); }
.msg-author {
  font-size: 11px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.06em;
  color: var(--text-muted);
}
.msg.user .msg-author { color: var(--accent-warm); }
.msg.assistant .msg-author { color: var(--accent); font-family: var(--display); letter-spacing: 0.12em; }
.msg-content { font-size: 14px; line-height: 1.7; color: var(--text); }
.msg.user .msg-content { color: var(--text-secondary); }

.thinking-scan {
  display: inline-block;
  width: 48px;
  height: 2px;
  margin-left: 8px;
  vertical-align: middle;
  background: linear-gradient(90deg, transparent, var(--accent), #f472b6, var(--accent), transparent);
  background-size: 200% 100%;
  border-radius: 1px;
  animation: scan-sweep 1.4s ease-in-out infinite;
  box-shadow: 0 0 6px var(--accent), 0 0 12px rgba(129, 140, 248, 0.3);
}
@keyframes scan-sweep {
  0% { background-position: 100% 0; opacity: 0.4; }
  50% { background-position: 0% 0; opacity: 1; }
  100% { background-position: 100% 0; opacity: 0.4; }
}

/* Reasoning ("thinking") panel — collapsible, shown while a thinking model reasons */
.thinking-panel {
  margin: 4px 0 10px;
  border-left: 2px solid var(--accent);
  background: rgba(129, 140, 248, 0.06);
  border-radius: 0 6px 6px 0;
}
.thinking-panel > summary {
  cursor: pointer;
  list-style: none;
  padding: 5px 10px;
  font-size: 12px;
  color: var(--text-secondary);
  font-family: var(--display);
  letter-spacing: 0.08em;
  user-select: none;
}
.thinking-panel > summary::-webkit-details-marker { display: none; }
.thinking-panel .think-dot {
  display: inline-block; width: 6px; height: 6px; border-radius: 50%;
  background: var(--accent); margin-right: 6px; vertical-align: middle;
  box-shadow: 0 0 6px var(--accent); animation: think-pulse 1.4s ease-in-out infinite;
}
@keyframes think-pulse { 0%,100% { opacity: 0.4; } 50% { opacity: 1; } }
.thinking-body {
  padding: 2px 12px 10px;
  font-size: 12.5px;
  color: var(--text-secondary);
  line-height: 1.6;
  white-space: pre-wrap;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  max-height: 320px;
  overflow-y: auto;
  opacity: 0.85;
}

/* Spacer: creates breathing room below the last message when loading */
.msg-spacer {
  height: 0;
  transition: height 240ms ease-out;
  flex-shrink: 0;
}
.msg-spacer.active {
  height: 60vh;
}

/* DAGTrace */
.trace-click { cursor: pointer; }

/* Capability gaps */
.gaps-strip {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  padding: 4px 8px; margin: 2px 0;
  font-size: 11px; font-family: var(--mono);
}
.gaps-icon {
  width: 16px; height: 16px; border-radius: 50%;
  background: var(--signal-amber, #f59e0b); color: #000;
  display: flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 10px; flex-shrink: 0;
}
.gap-tag {
  padding: 1px 6px; border-radius: 3px;
  background: var(--signal-amber-bg, rgba(245,158,11,0.1));
  color: var(--signal-amber, #f59e0b);
  font-weight: 500;
}

/* Interjection chip — the "return" affordance shown while a run is live. Green
   (signal-green) so it stays distinct from Send (accent-blue) and Stop (red).
   Palette-token backgrounds so it reads intentional in BOTH themes. Sized to
   the action-button height so the whole compose row bottom-aligns cleanly. */
.interject-chip {
  display: inline-flex; align-items: center; justify-content: center; gap: 3px;
  height: 30px; padding: 0 9px; flex-shrink: 0;
  border-radius: 7px;
  border: 1px solid color-mix(in srgb, var(--signal-green) 45%, transparent);
  background: var(--signal-green-bg);
  color: var(--signal-green);
  cursor: pointer;
  transition: background var(--transition), border-color var(--transition), transform var(--transition);
}
.interject-chip:hover {
  background: color-mix(in srgb, var(--signal-green) 22%, transparent);
  border-color: var(--signal-green);
}
.interject-chip:active { transform: scale(0.94); }
.ij-x { font-size: 14px; line-height: 1; opacity: 0.7; }
.interject-chip:hover .ij-x { opacity: 1; }

/* Stop button — square glyph, sits by the interject chip while a run is live.
   Red (signal-red) so it reads unambiguously as stop/cancel; palette-token
   background keeps it clean in dark mode. Soft pulse signals it's actionable.
   Matched to the action-button height so the row stays balanced. */
.stop-btn {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; padding: 0; flex-shrink: 0;
  border: 1px solid color-mix(in srgb, var(--signal-red) 45%, transparent);
  border-radius: 7px;
  background: var(--signal-red-bg);
  color: var(--signal-red); cursor: pointer;
  transition: background var(--transition), border-color var(--transition), transform var(--transition);
  animation: stop-pulse 1.8s ease-in-out infinite;
}
.stop-btn:hover {
  background: color-mix(in srgb, var(--signal-red) 22%, transparent);
  border-color: var(--signal-red);
  transform: scale(1.06);
}
.stop-btn:active { transform: scale(0.92); }
@keyframes stop-pulse {
  0%, 100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--signal-red) 40%, transparent); }
  50%      { box-shadow: 0 0 0 3px color-mix(in srgb, var(--signal-red) 0%, transparent); }
}

/* Dark mode: recolor the run controls into a cohesive blue/violet/orange trio
   (Send stays accent-blue). Interject → violet, Stop → orange (warmer than red). */
[data-theme="dark"] .interject-chip {
  color: #a78bfa;
  background: #171130;
  border-color: color-mix(in srgb, #a78bfa 45%, transparent);
}
[data-theme="dark"] .interject-chip:hover {
  background: color-mix(in srgb, #a78bfa 22%, transparent);
  border-color: #a78bfa;
}
[data-theme="dark"] .stop-btn {
  color: var(--accent-warm);
  background: var(--accent-warm-subtle);
  border-color: color-mix(in srgb, var(--accent-warm) 45%, transparent);
  animation: stop-pulse-warm 1.8s ease-in-out infinite;
}
[data-theme="dark"] .stop-btn:hover {
  background: color-mix(in srgb, var(--accent-warm) 22%, transparent);
  border-color: var(--accent-warm);
}
@keyframes stop-pulse-warm {
  0%, 100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent-warm) 40%, transparent); }
  50%      { box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent-warm) 0%, transparent); }
}

/* Compose — sits open on the messages surface, no separator line above it. */
.chat-compose {
  padding: 12px 32px 16px 36px;
  position: relative;
}
.chip-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding-bottom: 8px;
}
.drop-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(129, 140, 248, 0.12);
  border: 2px dashed #818cf8;
  border-radius: var(--radius);
  font-family: var(--mono);
  font-size: 13px;
  color: #818cf8;
  pointer-events: none;
  z-index: 5;
}
.compose-row {
  display: flex; gap: 4px; align-items: flex-end;
  background:
    linear-gradient(180deg,
      rgba(245,245,245,0.90) 0%,
      rgba(255,255,255,0.98) 49%,
      rgba(240,240,240,0.85) 50%,
      rgba(255,255,255,0.95) 100%
    );
  border: 1px solid rgba(0,0,0,0.08);
  border-radius: var(--radius);
  padding: 6px 8px;
  box-shadow: 0 2px 8px rgba(0,0,0,0.06), 0 1px 0 rgba(255,255,255,0.9) inset;
  transition: box-shadow var(--transition);
  flex-wrap: wrap;
}
.compose-row:focus-within { box-shadow: 0 2px 12px rgba(0,0,0,0.08), 0 1px 0 rgba(255,255,255,0.9) inset; }
/* Dark mode: a deep "dark-moon" surface — near-black with a faint moonlight
   glow up top, and a soft accent glow ring on focus. */
[data-theme="dark"] .compose-row {
  background:
    radial-gradient(120% 150% at 50% 0%, rgba(56,189,248,0.06) 0%, transparent 58%),
    linear-gradient(180deg, #0b0e14 0%, #060809 100%);
  border-color: rgba(120,160,220,0.10);
  box-shadow: 0 6px 22px rgba(0,0,0,0.5), 0 1px 0 rgba(120,160,220,0.05) inset;
}
[data-theme="dark"] .compose-row:focus-within {
  border-color: rgba(56,189,248,0.38);
  box-shadow: 0 6px 26px rgba(0,0,0,0.55), 0 0 0 3px rgba(56,189,248,0.10), 0 0 26px rgba(56,189,248,0.13);
}
.compose-controls {
  display: flex; align-items: flex-end; gap: 4px;
  flex-shrink: 0;
}
.compose-input {
  flex: 1; border: none;
  background: rgba(250,250,250,0.95);
  border-radius: 4px;
  resize: none; font-size: 14px; padding: 6px 8px;
  min-height: 24px; max-height: 200px; overflow-y: auto;
  font-family: var(--font); color: var(--text);
  min-width: 0;
}
[data-theme="dark"] .compose-input {
  background: transparent;
}
.compose-input:focus { outline: none; }
.compose-input::placeholder { color: var(--text-muted); }
/* Chip transition */
.chip-enter-active, .chip-leave-active { transition: all 0.2s ease; }
.chip-enter-from, .chip-leave-to { opacity: 0; transform: scale(0.8); }
/* Responsive: stack controls above input on small screens */
@media (max-width: 768px) {
  .compose-row { flex-wrap: wrap; }
  .compose-controls { width: 100%; padding-bottom: 4px; border-bottom: 1px solid var(--border-subtle); margin-bottom: 2px; }
  .compose-input { width: 100%; }
}
/* Send — the primary action. Accent-coloured at rest (so it reads as live, not
   greyed out, in dark mode), with a clear hover glow + press feedback. Disabled
   (empty input) falls back to muted so it still reads as inactive. */
.send {
  padding: 6px; border-radius: 7px;
  color: var(--accent);
  transition: color var(--transition), background var(--transition), filter var(--transition), transform var(--transition);
}
.send:not(:disabled):hover {
  color: var(--accent-hover);
  background: var(--accent-subtle);
  filter: drop-shadow(0 0 5px color-mix(in srgb, var(--accent) 55%, transparent));
}
.send:not(:disabled):active { transform: scale(0.9); }
.send:disabled { color: var(--text-muted); opacity: 0.4; cursor: default; }


@keyframes blink {
  0%, 80%, 100% { opacity: 0.3; }
  40% { opacity: 1; }
}
</style>
