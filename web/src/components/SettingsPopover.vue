<template>
  <!--
    Compact settings popover launched from the chat-header gear. Holds the five
    run controls that used to live as pills in the compose bar. Every control
    reuses the EXACT sessions-store state/methods the pills used — this is just a
    relocated, labelled surface, not new state.
  -->
  <div class="settings-popover" @click.stop>
    <!-- Mode -->
    <div class="pop-row">
      <div class="pop-label">Mode</div>
      <div class="pop-desc">How the planner reflects on its work.</div>
      <div class="seg">
        <button
          v-for="m in modes" :key="m"
          class="seg-btn" :class="{ active: sessions.runMode === m }"
          @click="sessions.setRunMode(m)"
        >{{ m }}</button>
      </div>
    </div>

    <!-- Intent -->
    <div class="pop-row">
      <div class="pop-label">Intent</div>
      <div class="pop-desc">Safety / guidance profile applied to the query.</div>
      <select class="pop-select" v-model="sessions.intent">
        <option value="">auto</option>
        <option v-for="i in intents" :key="i.name" :value="i.name" :title="i.description">{{ i.name }}</option>
      </select>
    </div>

    <!-- Aggregator -->
    <div class="pop-row">
      <div class="pop-label">Aggregator</div>
      <div class="pop-desc">How the final answer is assembled.</div>
      <div class="seg">
        <button
          v-for="a in aggs" :key="a.v"
          class="seg-btn" :class="{ active: sessions.aggMode === a.v }"
          @click="sessions.setAggMode(a.v)"
        >{{ a.l }}</button>
      </div>
    </div>

    <!-- Execution -->
    <div class="pop-row">
      <div class="pop-label">Execution</div>
      <div class="pop-opt" :class="{ active: sessions.executionMode === 'interactive' }" @click="sessions.setExecutionMode('interactive')">
        <span class="pop-opt-name">Interactive</span>
        <span class="pop-opt-desc">Checks in as it goes.</span>
      </div>
      <div class="pop-opt" :class="{ active: sessions.executionMode === 'autonomous' }" @click="sessions.setExecutionMode('autonomous')">
        <span class="pop-opt-name">Autonomous</span>
        <span class="pop-opt-desc">Runs to completion.</span>
      </div>
    </div>

    <!-- Chat mode -->
    <div class="pop-row">
      <div class="pop-label">Chat mode</div>
      <div class="pop-opt" :class="{ active: !sessions.chatMode }" @click="setChat(false)">
        <span class="pop-opt-name">Agent</span>
        <span class="pop-opt-desc">Plans &amp; uses tools.</span>
      </div>
      <div class="pop-opt" :class="{ active: sessions.chatMode }" @click="setChat(true)">
        <span class="pop-opt-name">Direct</span>
        <span class="pop-opt-desc">Reply only, for chat models.</span>
      </div>
    </div>

    <div class="pop-divider"></div>
    <button class="pop-advanced" @click="$emit('open-advanced')">
      <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
      <span>Advanced settings…</span>
    </button>
  </div>
</template>

<script setup>
/**
 * desc: Chat-header settings popover — relocated run controls (mode, intent,
 * aggregator, execution, chat mode) plus a link to the full Settings modal.
 */
import { ref, onMounted } from 'vue'
import { useSessionsStore } from '../stores/sessions'
import api from '../api/client'

defineEmits(['open-advanced'])

const sessions = useSessionsStore()

const modes = ['reflect', 'nReflect', 'orchestrator', 'react']
const aggs = [
  { v: '-1', l: 'auto' },
  { v: '0', l: 'skip' },
  { v: '1', l: 'mini' },
  { v: '2', l: 'full' },
]

// Intent registry — the sole source of truth (same endpoint the compose pill and
// Settings modal use). On failure the list is empty and only "auto" shows.
const intents = ref([])
onMounted(async () => {
  try {
    const list = await api.get('/api/v1/intents')
    if (Array.isArray(list)) intents.value = list.map(i => ({ name: i.name, description: i.description }))
  } catch (e) {
    console.error('[settings-popover] failed to load intents:', e)
  }
})

/**
 * desc: Set chat mode explicitly. The store only exposes toggleChatMode(), so we
 * flip only when the desired value differs — reusing the existing method rather
 * than inventing new store state.
 * @param {boolean} val - true = Direct (chat lane), false = Agent
 */
function setChat(val) {
  if (sessions.chatMode !== val) sessions.toggleChatMode()
}
</script>

<style scoped>
.settings-popover {
  position: absolute; top: calc(100% + 8px); right: 0; z-index: 120;
  width: 300px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  padding: 6px;
}
.pop-row { padding: 8px 8px 10px; }
.pop-row + .pop-row { border-top: 1px solid var(--border-subtle); }
.pop-label {
  font-size: 11px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.06em;
  color: var(--text); margin-bottom: 2px;
}
.pop-desc { font-size: 11px; color: var(--text-muted); margin-bottom: 8px; line-height: 1.4; }

/* Segmented control (mode + aggregator) */
.seg { display: flex; gap: 4px; flex-wrap: wrap; }
.seg-btn {
  padding: 4px 10px; border-radius: var(--radius-sm);
  font-size: 11px; font-family: var(--mono);
  color: var(--text-secondary); cursor: pointer;
  background: var(--surface-hover); border: 1px solid transparent;
  transition: all var(--transition);
}
.seg-btn:hover { color: var(--text); border-color: var(--border); }
.seg-btn.active { color: var(--accent); background: var(--accent-subtle); border-color: var(--accent); font-weight: 600; }

/* Native select (intent) */
.pop-select { width: 100%; font-size: 12px; padding: 6px 10px; }

/* Two-line option rows (execution + chat mode) */
.pop-opt {
  display: flex; flex-direction: column; gap: 1px;
  padding: 6px 10px; border-radius: var(--radius-sm);
  border: 1px solid transparent; cursor: pointer;
  transition: all var(--transition);
}
.pop-opt + .pop-opt { margin-top: 4px; }
.pop-opt:hover { background: var(--surface-hover); }
.pop-opt.active { background: var(--accent-subtle); border-color: var(--accent); }
.pop-opt-name { font-size: 12px; font-weight: 600; color: var(--text); }
.pop-opt.active .pop-opt-name { color: var(--accent); }
.pop-opt-desc { font-size: 11px; color: var(--text-muted); }

.pop-divider { border-top: 1px solid var(--border-subtle); margin: 6px 0; }
.pop-advanced {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 8px 10px; border-radius: var(--radius-sm);
  background: none; border: none; cursor: pointer;
  font-size: 12px; font-family: var(--mono); color: var(--text-secondary);
  transition: all var(--transition);
}
.pop-advanced:hover { color: var(--accent); background: var(--surface-hover); }
</style>
