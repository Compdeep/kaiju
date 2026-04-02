<template>
  <div class="chat-page" @mousemove="onDrag" @mouseup="onDragEnd" @mouseleave="onDragEnd">
    <!-- Col 1: Session sidebar -->
    <div class="sidebar" :style="{ width: sidebarW + 'px' }" :class="{ collapsed: sidebarCollapsed }">
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
      <div class="chat-messages" ref="messagesEl">
        <div v-if="!sessions.messages.length" class="empty-state">
          <svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.5" style="color:var(--text-muted);margin-bottom:12px"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>
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
              <span class="msg-author">{{ msg.role === 'user' ? 'you' : 'kaiju' }}</span>
            </div>
            <div class="msg-content md" v-html="renderMd(msg.content)"></div>
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
          <div class="msg-meta"><span class="msg-author">kaiju</span></div>
          <div v-if="dag.streamingVerdict" class="msg-content md" v-html="renderMd(dag.streamingVerdict)"></div>
          <div v-else class="msg-content thinking"><span></span><span></span><span></span></div>
        </div>

        <!-- Breathing room: pushes content up so agent response starts visible -->
        <div class="msg-spacer" :class="{ active: sessions.loading }"></div>
      </div>

      <div class="chat-compose">
        <div class="compose-row">
          <!-- Inline controls (left of input) -->
          <div class="compose-controls">
            <!-- Mode picker: triangular M -->
            <div class="ctl-wrap" @click.stop="openMenu = openMenu === 'mode' ? null : 'mode'">
              <div class="ctl-btn" :class="{ active: openMenu === 'mode' }">
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="2,20 7,4 12,14 17,4 22,20"/></svg>
              </div>
              <span class="ctl-tip">Mode: {{ sessions.runMode }}</span>
              <Transition name="menu">
                <div v-if="openMenu === 'mode'" class="ctl-menu">
                  <div v-for="m in ['reflect','nReflect','orchestrator','react']" :key="m"
                    class="ctl-opt" :class="{ sel: sessions.runMode === m }"
                    @click="sessions.setRunMode(m); openMenu = null">{{ m }}</div>
                </div>
              </Transition>
            </div>
            <!-- IGX gate: inverted shield triangle -->
            <div class="ctl-wrap" @click.stop="openMenu = openMenu === 'intent' ? null : 'intent'">
              <div class="ctl-btn" :class="{ active: openMenu === 'intent' }">
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2L3 7v4c0 7 9 11 9 11s9-4 9-11V7z"/><line x1="12" y1="10" x2="12" y2="14"/><circle cx="12" cy="17" r="0.5" fill="currentColor"/></svg>
              </div>
              <span class="ctl-tip">IGX: {{ sessions.intent }}</span>
              <Transition name="menu">
                <div v-if="openMenu === 'intent'" class="ctl-menu">
                  <div v-for="i in ['observe','operate','override']" :key="i"
                    class="ctl-opt" :class="{ sel: sessions.intent === i }"
                    @click="sessions.intent = i; openMenu = null">{{ i }}</div>
                </div>
              </Transition>
            </div>
            <!-- Aggregator: triangular A -->
            <div class="ctl-wrap" @click.stop="openMenu = openMenu === 'agg' ? null : 'agg'">
              <div class="ctl-btn" :class="{ active: openMenu === 'agg' }">
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3,20 12,4 21,20"/><line x1="7.5" y1="14" x2="16.5" y2="14"/></svg>
              </div>
              <span class="ctl-tip">Agg: {{ {'-1':'auto','0':'skip','1':'mini','2':'full'}[sessions.aggMode] }}</span>
              <Transition name="menu">
                <div v-if="openMenu === 'agg'" class="ctl-menu">
                  <div v-for="a in [{v:'-1',l:'auto'},{v:'0',l:'skip'},{v:'1',l:'mini'},{v:'2',l:'full'}]" :key="a.v"
                    class="ctl-opt" :class="{ sel: sessions.aggMode === a.v }"
                    @click="sessions.setAggMode(a.v); openMenu = null">{{ a.l }}</div>
                </div>
              </Transition>
            </div>
            <!-- Interject chip -->
            <Transition name="chip">
              <span v-if="dag.interjectMode" class="interject-chip" @click="dag.interjectMode = false">
                <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><polyline points="9 10 4 15 9 20"/><path d="M20 4v7a4 4 0 01-4 4H4"/></svg>
                <span class="ij-x">&times;</span>
              </span>
            </Transition>
          </div>
          <!-- Input -->
          <textarea
            v-model="input"
            class="compose-input"
            rows="1"
            :placeholder="dag.interjectMode ? 'interject into running query...' : 'ask anything...'"
            @keydown.enter.exact.prevent="send"
          ></textarea>
          <!-- Right-side actions -->
          <button class="btn-icon" @click="chat.compactSession()" title="Compact history" :disabled="sessions.messages.length < 10">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>
          </button>
          <button class="btn-icon" @click="panel.toggle()" :title="panel.open ? 'Close panel' : 'Open panel'" :class="{ 'panel-active': panel.open }">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8">
              <rect x="3" y="3" width="18" height="18" rx="2"/>
              <line x1="15" y1="3" x2="15" y2="21"/>
            </svg>
          </button>
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

    <!-- Col 3: Composable panel -->
    <ComposablePanel v-if="panel.open" />
  </div>
</template>

<script setup>
/**
 * desc: Main chat page with resizable sidebar, message thread, DAG trace display, composable panel, and interjection support
 */
import { ref, nextTick, onMounted, watch } from 'vue'
import { useSessionsStore } from '../stores/sessions'
import { useDagStore } from '../stores/dag'
import { usePanelStore } from '../stores/panel'
import * as chat from '../services/chat'
import * as tools from '../services/tools'
import DAGTrace from '../components/DAGTrace.vue'
import ComposablePanel from '../components/ComposablePanel.vue'

const sessions = useSessionsStore()
const dag = useDagStore()
const panel = usePanelStore()
const input = ref('')
const messagesEl = ref(null)
const openMenu = ref(null)

// Close menus on outside click
if (typeof document !== 'undefined') {
  document.addEventListener('click', () => { openMenu.value = null })
}

// Load sessions on mount; restore active session if saved
onMounted(async () => {
  await chat.loadSessions()
  if (sessions.sessionId && sessions.sessions.find(s => s.id === sessions.sessionId)) {
    await chat.switchSession(sessions.sessionId)
  } else if (sessions.sessions.length > 0) {
    await chat.switchSession(sessions.sessions[0].id)
  }
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

  const isInterject = dag.interjectMode && dag.running

  if (isInterject) {
    await chat.interject(text)
  } else {
    await chat.send(text)
  }

  scrollToBottom()
}

function scrollToBottom() {
  if (!messagesEl.value) return
  messagesEl.value.scrollTo({ top: messagesEl.value.scrollHeight, behavior: 'smooth' })
}

// Auto-scroll when streaming verdict updates
watch(() => dag.streamingVerdict, () => {
  nextTick(scrollToBottom)
})

// Auto-scroll when new messages arrive
watch(() => sessions.messages.length, () => {
  nextTick(scrollToBottom)
})

/**
 * desc: Convert markdown-formatted text to HTML for message rendering
 * @param {string} text - Raw markdown text
 * @returns {string} HTML string
 */
function renderMd(text) {
  if (!text) return ''
  let html = escHtml(text)
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>')
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>')
  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
  html = html.replace(/\*(.+?)\*/g, '<em>$1</em>')
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>')
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>')
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>')
  html = html.replace(/^- (.+)$/gm, '<li>$1</li>')
  html = html.replace(/(<li>.*<\/li>)/gs, '<ul>$1</ul>')
  html = html.replace(/^> (.+)$/gm, '<blockquote>$1</blockquote>')
  html = html.replace(/\n\n/g, '</p><p>')
  html = '<p>' + html + '</p>'
  html = html.replace(/<p><\/p>/g, '')
  return html
}

/**
 * desc: Escape HTML special characters to prevent XSS in rendered content
 * @param {string} s - Raw string to escape
 * @returns {string} Escaped HTML-safe string
 */
function escHtml(s) {
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
}

onMounted(async () => {
  await chat.loadSessions()
  if (sessions.sessionId) {
    await chat.switchSession(sessions.sessionId)
  }
  tools.connect()
})
</script>

<style scoped>
.chat-page {
  display: flex; height: calc(100vh - 82px);
  user-select: none;
}
.chat-page * { user-select: text; }

/* ── Gutter (drag handle) ───────────────────────────────────────────────────── */
.gutter {
  width: 6px; min-width: 6px;
  cursor: col-resize;
  background: var(--gutter-idle);
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
  overflow: hidden;
  position: relative;
  min-width: 40px;
  transition: width 0.15s ease;
}
.sidebar.collapsed { width: 40px !important; min-width: 40px; }

.sidebar-new {
  display: flex; align-items: center; gap: 6px;
  margin: 10px; padding: 8px 12px;
  border: 1px dashed var(--border); border-radius: var(--radius-sm);
  background: transparent; color: var(--text-secondary);
  font-size: 12px; font-family: var(--mono); cursor: pointer;
  transition: all var(--transition);
}
.sidebar-new:hover { border-color: var(--accent); color: var(--accent); }
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

.msg { display: flex; flex-direction: column; gap: 4px; max-width: 740px; }
.msg-meta { display: flex; align-items: center; gap: 6px; }
.msg-author {
  font-size: 11px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.06em;
  color: var(--text-muted);
}
.msg.user .msg-author { color: var(--accent-warm); }
.msg.assistant .msg-author { color: var(--accent); }
.msg-content { font-size: 14px; line-height: 1.7; color: var(--text); }
.msg.user .msg-content { color: var(--text-secondary); }

.thinking { display: flex; gap: 4px; padding: 4px 0; }
.thinking span {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--accent); opacity: 0.3;
  animation: blink 1.4s infinite both;
}
.thinking span:nth-child(2) { animation-delay: 0.2s; }
.thinking span:nth-child(3) { animation-delay: 0.4s; }

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

/* Interjection */
.interject-chip {
  display: flex; align-items: center; gap: 4px;
  padding: 2px 8px; border-radius: 4px;
  background: var(--accent-warm-subtle); color: var(--accent-warm);
  font-size: 10px; font-weight: 600; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.05em;
  cursor: pointer; flex-shrink: 0;
  transition: all var(--transition);
}
.interject-chip:hover { opacity: 0.8; }
.ij-x { font-size: 12px; font-weight: 400; opacity: 0.6; }
.ij-x:hover { opacity: 1; }

/* Compose */
.chat-compose { padding: 12px 32px 16px 36px; border-top: 1px solid var(--border-subtle); }
.compose-row {
  display: flex; gap: 4px; align-items: flex-end;
  background: var(--surface);
  border: 1px solid var(--border); border-radius: var(--radius);
  padding: 6px 8px; box-shadow: var(--shadow-sm);
  transition: border-color var(--transition);
  flex-wrap: wrap;
}
.compose-row:focus-within { border-color: var(--accent); }
.compose-controls {
  display: flex; align-items: center; gap: 1px;
  flex-shrink: 0;
}
.ctl-wrap {
  position: relative; user-select: none;
}
.ctl-btn {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 6px;
  color: var(--text-muted); cursor: pointer;
  transition: all 0.12s ease;
}
.ctl-btn:hover, .ctl-btn.active { color: var(--accent); background: var(--bg-soft); }
/* Tooltip */
.ctl-tip {
  position: absolute; bottom: calc(100% + 6px); left: 50%; transform: translateX(-50%);
  padding: 3px 8px; border-radius: 4px;
  background: var(--text); color: var(--surface);
  font-size: 10px; font-family: var(--mono); white-space: nowrap;
  pointer-events: none; opacity: 0;
  transition: opacity 0.12s ease;
}
.ctl-wrap:hover .ctl-tip { opacity: 1; }
/* Dropdown menu */
.ctl-menu {
  position: absolute; bottom: calc(100% + 4px); left: 0;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 8px; padding: 4px;
  box-shadow: 0 4px 16px rgba(0,0,0,0.12);
  min-width: 110px; z-index: 100;
}
.ctl-opt {
  padding: 5px 10px; border-radius: 5px;
  font-size: 11px; font-family: var(--mono);
  color: var(--text-secondary); cursor: pointer;
  transition: all 0.08s ease;
}
.ctl-opt:hover { background: var(--bg-soft); color: var(--text); }
.ctl-opt.sel { color: var(--accent); font-weight: 600; }
/* Menu transition: slide up + fade */
.menu-enter-active, .menu-leave-active { transition: all 0.12s ease; }
.menu-enter-from, .menu-leave-to { opacity: 0; transform: translateY(6px); }
.menu-enter-to, .menu-leave-from { opacity: 1; transform: translateY(0); }
.compose-input {
  flex: 1; border: none; background: transparent;
  resize: none; font-size: 14px; padding: 6px 4px;
  min-height: 24px; max-height: 140px;
  font-family: var(--font); color: var(--text);
  min-width: 0;
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
.send { padding: 6px; }
.send:not(:disabled):hover { color: var(--accent) !important; }
.send:disabled { opacity: 0.2; cursor: default; }

/* Panel toggle button active state */
.panel-active { color: var(--accent) !important; }

@keyframes blink {
  0%, 80%, 100% { opacity: 0.3; }
  40% { opacity: 1; }
}
</style>
