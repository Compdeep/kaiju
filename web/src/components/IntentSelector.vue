<template>
  <!--
    Intent picker for the chat header. Intent is one of the three the gate
    compares — a call runs when its impact is within intent, clearance and the
    scope cap — so a run refused by the gate is usually refused on this, and
    until now the only place to see or change it was the Settings modal.

    Wired to the same field Settings writes: GET /api/v1/config for the current
    agent.safety_level, GET /api/v1/intents for the registry that names each
    rank. Selecting one PATCHes agent.safety_level. Standing config, not a
    per-message field, which is why it sits beside the model rather than beside
    the send button.

    Same shape as ModelSelector: a compact header trigger opening a panel
    teleported to <body>, so it cannot be clipped by the header.
  -->
  <button
    ref="triggerEl"
    type="button"
    class="is-trigger"
    :class="{ open }"
    :disabled="loading"
    title="Intent — the ceiling on what a run may do"
    aria-haspopup="listbox"
    :aria-expanded="open"
    @click.stop="toggle"
  >
    <svg class="is-icon" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
      <path d="M12 3l7 3v6c0 4.4-3 8.1-7 9-4-0.9-7-4.6-7-9V6z"/>
    </svg>
    <span class="is-name">{{ loading ? 'loading…' : triggerLabel }}</span>
    <svg class="is-caret" :class="{ up: open }" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2">
      <polyline points="6 9 12 15 18 9"/>
    </svg>
  </button>

  <Teleport to="body">
    <Transition name="is-fade">
      <div v-if="open" class="is-backdrop" @click="closePanel"></div>
    </Transition>
    <Transition name="is-pop">
      <div
        v-if="open"
        ref="panelRef"
        class="is-panel"
        role="listbox"
        aria-label="Intent"
        :style="{ top: panelPos.top + 'px', left: panelPos.left + 'px', width: panelPos.width + 'px' }"
        @click.stop
      >
        <div class="is-head">
          <span class="is-head-label">Intent</span>
          <span class="is-head-note">the ceiling on what a run may do</span>
        </div>

        <div class="is-list">
          <button
            v-for="i in options"
            :key="i.rank"
            type="button"
            class="is-row"
            :class="{ sel: i.rank === current }"
            role="option"
            :aria-selected="i.rank === current"
            @click="select(i.rank)"
          >
            <span class="is-check">{{ i.rank === current ? '●' : '' }}</span>
            <span class="is-row-name">{{ i.name }}</span>
            <span class="is-row-rank">{{ i.rank }}</span>
          </button>
          <div v-if="!options.length" class="is-empty">no intents registered</div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
/**
 * IntentSelector — header picker for agent.safety_level.
 *
 * Custom dropdown rather than a native select, to match ModelSelector beside it.
 */
import { ref, computed, nextTick, onBeforeUnmount } from 'vue'
import api from '../api/client'

const loading = ref(true)
const current = ref(null)     // the live rank
const options = ref([])       // [{ name, rank }] from the registry, low rank first

const open = ref(false)
const triggerEl = ref(null)
const panelRef = ref(null)
const panelPos = ref({ top: 0, left: 0, width: 240 })
const PANEL_W = 240

/** desc: The registry entry for the live rank, for the trigger label. */
const currentObj = computed(() => options.value.find(i => i.rank === current.value) || null)

/** desc: Trigger text — the intent's name, or the bare rank when unregistered. */
const triggerLabel = computed(() => {
  if (currentObj.value) return currentObj.value.name
  return current.value == null ? 'intent' : String(current.value)
})

/**
 * desc: Load the live rank and the registry that names each one.
 * @returns {Promise<void>}
 */
async function load() {
  loading.value = true
  try {
    const [cfg, intents] = await Promise.all([
      api.get('/api/v1/config'),
      api.get('/api/v1/intents'),
    ])
    const lvl = cfg?.agent?.safety_level
    current.value = typeof lvl === 'number' ? lvl : null
    options.value = Array.isArray(intents)
      ? intents.map(i => ({ name: i.name, rank: i.rank })).sort((a, b) => a.rank - b.rank)
      : []
  } catch (e) {
    console.error('[intent-selector] load failed:', e)
  }
  loading.value = false
}

/**
 * desc: Set the intent — PATCH agent.safety_level. Rolls back on failure, so the
 * header never shows a ceiling the server did not accept.
 * @param {number} rank
 * @returns {Promise<void>}
 */
async function onChange(rank) {
  if (rank == null || rank === current.value) return
  const prev = current.value
  current.value = rank
  try {
    await api.patch('/api/v1/config', { agent: { safety_level: rank } })
  } catch (e) {
    console.error('[intent-selector] patch failed:', e)
    current.value = prev
    alert('Failed to set intent: ' + e.message)
  }
}

/** desc: Anchor the panel below the trigger, right-aligned, clamped on-screen. */
function positionPanel() {
  const el = triggerEl.value
  if (!el) return
  const r = el.getBoundingClientRect()
  let left = r.right - PANEL_W
  left = Math.min(left, window.innerWidth - PANEL_W - 8)
  left = Math.max(8, left)
  const top = Math.min(r.bottom + 6, window.innerHeight - 80)
  panelPos.value = { top, left, width: PANEL_W }
}

function openPanel() {
  open.value = true
  nextTick(() => {
    positionPanel()
    window.addEventListener('resize', positionPanel)
    window.addEventListener('scroll', positionPanel, true)
    document.addEventListener('keydown', onKey)
    const p = panelRef.value
    ;(p?.querySelector('.is-row.sel') || p?.querySelector('.is-row'))?.focus()
  })
}

function closePanel() {
  if (!open.value) return
  open.value = false
  window.removeEventListener('resize', positionPanel)
  window.removeEventListener('scroll', positionPanel, true)
  document.removeEventListener('keydown', onKey)
  triggerEl.value?.focus()
}

function toggle() { open.value ? closePanel() : openPanel() }
function onKey(e) { if (e.key === 'Escape') closePanel() }

/** desc: Choose an intent, then close. */
async function select(rank) {
  closePanel()
  await onChange(rank)
}

onBeforeUnmount(() => {
  window.removeEventListener('resize', positionPanel)
  window.removeEventListener('scroll', positionPanel, true)
  document.removeEventListener('keydown', onKey)
})

load()

// Exposed so the chat header can re-sync after Settings, which writes the same field.
defineExpose({ reload: load })
</script>

<style scoped>
/* Deliberately the same visual language as ModelSelector, since the two sit
   side by side in the header and are the same kind of control. */

/* ── Trigger ─────────────────────────────────────────────── */
.is-trigger {
  display: inline-flex; align-items: center; gap: 6px;
  height: 30px; max-width: 180px; padding: 0 8px 0 10px;
  border: 1px solid var(--border); border-radius: 7px;
  background: var(--surface); color: var(--text-secondary);
  font-family: var(--mono); font-size: 12px; cursor: pointer;
  transition: color var(--transition), border-color var(--transition),
              background var(--transition), box-shadow var(--transition);
}
.is-trigger:hover:not(:disabled), .is-trigger.open {
  color: var(--accent); border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-subtle),
              0 0 10px color-mix(in srgb, var(--accent) 22%, transparent);
}
.is-trigger:disabled { opacity: 0.6; cursor: default; }
.is-icon { flex-shrink: 0; }
.is-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 110px; }
.is-caret { flex-shrink: 0; opacity: 0.6; transition: transform var(--transition); }
.is-caret.up { transform: rotate(180deg); }

/* ── Backdrop ────────────────────────────────────────────── */
.is-backdrop {
  position: fixed; inset: 0; z-index: 299;
  background: color-mix(in srgb, #000 10%, transparent);
  backdrop-filter: blur(1.5px);
  -webkit-backdrop-filter: blur(1.5px);
}

/* ── Panel ───────────────────────────────────────────────── */
.is-panel {
  position: fixed; z-index: 300;
  display: flex; flex-direction: column;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg),
              0 0 22px color-mix(in srgb, var(--accent) 10%, transparent);
  overflow: hidden;
  transform-origin: top right;
}
[data-theme="dark"] .is-panel {
  background: linear-gradient(180deg, #0d0d13 0%, #0a0a0e 100%);
  border-color: rgba(255,255,255,0.08);
  box-shadow: 0 14px 42px rgba(0,0,0,0.6),
              0 0 0 1px rgba(255,255,255,0.03),
              0 0 26px color-mix(in srgb, var(--accent) 16%, transparent);
}

.is-head {
  display: flex; flex-direction: column; gap: 2px;
  padding: 9px 12px; border-bottom: 1px solid var(--border-subtle);
}
.is-head-label {
  font-size: 10px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.08em; color: var(--text-muted);
}
.is-head-note { font-size: 11px; font-family: var(--mono); color: var(--text-muted); }

.is-list { max-height: 320px; overflow-y: auto; padding: 4px; }
.is-row {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 7px 8px; border-radius: var(--radius-sm);
  background: none; border: 1px solid transparent; cursor: pointer;
  text-align: left;
  transition: background var(--transition), border-color var(--transition);
}
.is-row:hover { background: var(--surface-hover); }
.is-row:focus-visible { outline: none; background: var(--surface-hover); border-color: var(--accent); }
.is-row.sel { background: var(--accent-subtle); border-color: color-mix(in srgb, var(--accent) 30%, transparent); }
.is-check { width: 14px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; color: var(--accent); font-size: 10px; }
.is-row-name {
  flex: 1; min-width: 0; font-family: var(--mono); font-size: 12px; color: var(--text);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.is-row.sel .is-row-name { color: var(--accent); font-weight: 600; }
/* The rank is shown because the gate compares ranks — a run refused for intent
   is refused on this number, so it is the thing to raise. */
.is-row-rank {
  flex-shrink: 0; font-family: var(--mono); font-size: 10px;
  color: var(--text-muted); padding: 1px 5px; border-radius: 3px;
  background: var(--surface-hover);
}
.is-empty { padding: 10px 12px; font-family: var(--mono); font-size: 11px; color: var(--text-muted); }

/* ── Transitions ─────────────────────────────────────────── */
.is-fade-enter-active, .is-fade-leave-active { transition: opacity var(--transition); }
.is-fade-enter-from, .is-fade-leave-to { opacity: 0; }
.is-pop-enter-active { transition: opacity 120ms ease, transform 120ms cubic-bezier(0.2, 0.9, 0.3, 1.2); }
.is-pop-leave-active { transition: opacity 90ms ease, transform 90ms ease; }
.is-pop-enter-from, .is-pop-leave-to { opacity: 0; transform: scale(0.96) translateY(-4px); }
</style>
