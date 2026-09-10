<template>
  <!--
    Effort picker for the chat header. Effort is how hard the model is asked to
    think when it is thinking — a different question from the reasoning switch
    in Settings, which is whether to think at all.

    Standing config rather than a per-message field, which is why it sits beside
    the intent and the model rather than beside the send button. Wired to the
    same field Settings writes: GET /api/v1/config for llm.reasoning_effort,
    GET /api/v1/models for which values the configured models act on. Selecting
    one PATCHes llm.reasoning_effort.

    Not rendered at all when no configured model has been measured to act on an
    effort. Every provider accepts the parameter and none errors on it, so a
    control shown regardless would save a value, show it back, and change
    nothing.

    Same shape as IntentSelector and ModelSelector beside it: a compact trigger
    opening a panel teleported to <body>, so it cannot be clipped by the header.
  -->
  <button
    v-if="options.length"
    ref="triggerEl"
    type="button"
    class="es-trigger"
    :class="{ open }"
    :disabled="loading"
    title="Effort — how hard to think, where the model acts on it"
    aria-haspopup="listbox"
    :aria-expanded="open"
    @click.stop="toggle"
  >
    <svg class="es-icon" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
      <path d="M4 18a8 8 0 1 1 16 0"/>
      <line x1="12" y1="18" x2="16" y2="11"/>
    </svg>
    <span class="es-name">{{ triggerLabel }}</span>
    <svg class="es-caret" :class="{ up: open }" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2">
      <polyline points="6 9 12 15 18 9"/>
    </svg>
  </button>

  <Teleport to="body">
    <Transition name="es-fade">
      <div v-if="open" class="es-backdrop" @click="closePanel"></div>
    </Transition>
    <Transition name="es-pop">
      <div
        v-if="open"
        ref="panelRef"
        class="es-panel"
        role="listbox"
        aria-label="Effort"
        :style="{ top: panelPos.top + 'px', left: panelPos.left + 'px', width: panelPos.width + 'px' }"
        @click.stop
      >
        <div class="es-head">
          <span class="es-head-label">Effort</span>
          <span class="es-head-note">how hard to think, when thinking</span>
        </div>

        <div class="es-list">
          <button
            v-for="o in rows"
            :key="o.value"
            type="button"
            class="es-row"
            :class="{ sel: o.value === current }"
            role="option"
            :aria-selected="o.value === current"
            @click="select(o.value)"
          >
            <span class="es-check">{{ o.value === current ? '●' : '' }}</span>
            <span class="es-row-name">{{ o.label }}</span>
          </button>
        </div>

        <!-- Said here because the switch that governs it is in another panel:
             an effort is asked for only on a lane that is thinking, and the
             reasoning lane's switch can be off. -->
        <div v-if="reasoningOff" class="es-foot">
          reasoning is switched off on the reasoning lane, so this reaches only
          the lanes that take the model's default
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
/**
 * EffortSelector — header picker for llm.reasoning_effort.
 *
 * Custom dropdown rather than a native select, to match the two controls it
 * sits beside.
 */
import { ref, computed, nextTick, onBeforeUnmount } from 'vue'
import api from '../api/client'
import { effortOptions } from '../services/reasoning'

const loading = ref(true)
const current = ref('')      // the live effort, "" for none asked
const options = ref([])      // accepted values for the configured models
const reasoningOff = ref(false)

const open = ref(false)
const triggerEl = ref(null)
const panelRef = ref(null)
const panelPos = ref({ top: 0, left: 0, width: 240 })
const PANEL_W = 240

// "" is a real choice, not the absence of one, so it gets a row of its own.
const rows = computed(() => [
  { value: '', label: 'default' },
  ...options.value.map(v => ({ value: v, label: v })),
])

/** desc: Trigger text — the effort, or "effort" when none is asked for. */
const triggerLabel = computed(() => {
  if (loading.value) return 'loading…'
  return current.value || 'effort'
})

/**
 * desc: Load the live effort and work out which values are worth offering.
 * @returns {Promise<void>}
 */
async function load() {
  loading.value = true
  try {
    const [cfg, models] = await Promise.all([
      api.get('/api/v1/config'),
      api.get('/api/v1/models'),
    ])
    current.value = cfg?.llm?.reasoning_effort || ''
    reasoningOff.value = cfg?.llm?.reasoning === 'off'
    options.value = effortOptions(cfg, models)
  } catch (e) {
    console.error('[effort-selector] load failed:', e)
    options.value = []
  }
  loading.value = false
}

/**
 * desc: Set the effort — PATCH llm.reasoning_effort. Rolls back on failure, so
 * the header never shows a value the server did not accept.
 * @param {string} v - one of the offered efforts, or "" to ask nothing
 * @returns {Promise<void>}
 */
async function onChange(v) {
  if (v === current.value) return
  const prev = current.value
  current.value = v
  try {
    await api.patch('/api/v1/config', { llm: { reasoning_effort: v } })
  } catch (e) {
    console.error('[effort-selector] patch failed:', e)
    current.value = prev
    alert('Failed to set effort: ' + e.message)
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
    ;(p?.querySelector('.es-row.sel') || p?.querySelector('.es-row'))?.focus()
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

/** desc: Choose an effort, then close. */
async function select(v) {
  closePanel()
  await onChange(v)
}

onBeforeUnmount(() => {
  window.removeEventListener('resize', positionPanel)
  window.removeEventListener('scroll', positionPanel, true)
  document.removeEventListener('keydown', onKey)
})

load()

// Exposed so the chat header can re-sync after Settings, which writes the same
// field and can also change the models that decide whether this is shown.
defineExpose({ reload: load })
</script>

<style scoped>
/* Deliberately the same visual language as IntentSelector and ModelSelector,
   since the three sit side by side and are the same kind of control. */

/* ── Trigger ─────────────────────────────────────────────── */
.es-trigger {
  display: inline-flex; align-items: center; gap: 6px;
  height: 30px; max-width: 150px; padding: 0 8px 0 10px;
  border: 1px solid var(--border); border-radius: 7px;
  background: var(--surface); color: var(--text-secondary);
  font-family: var(--mono); font-size: 12px; cursor: pointer;
  transition: color var(--transition), border-color var(--transition),
              background var(--transition), box-shadow var(--transition);
}
.es-trigger:hover:not(:disabled), .es-trigger.open {
  color: var(--accent); border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-subtle),
              0 0 10px color-mix(in srgb, var(--accent) 22%, transparent);
}
.es-trigger:disabled { opacity: 0.6; cursor: default; }
.es-icon { flex-shrink: 0; }
.es-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 90px; }
.es-caret { flex-shrink: 0; opacity: 0.6; transition: transform var(--transition); }
.es-caret.up { transform: rotate(180deg); }

/* ── Backdrop ────────────────────────────────────────────── */
.es-backdrop {
  position: fixed; inset: 0; z-index: 299;
  background: color-mix(in srgb, #000 10%, transparent);
  backdrop-filter: blur(1.5px);
  -webkit-backdrop-filter: blur(1.5px);
}

/* ── Panel ───────────────────────────────────────────────── */
.es-panel {
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
[data-theme="dark"] .es-panel {
  background: linear-gradient(180deg, #0d0d13 0%, #0a0a0e 100%);
  border-color: rgba(255,255,255,0.08);
  box-shadow: 0 14px 42px rgba(0,0,0,0.6),
              0 0 0 1px rgba(255,255,255,0.03),
              0 0 26px color-mix(in srgb, var(--accent) 16%, transparent);
}

.es-head {
  display: flex; flex-direction: column; gap: 2px;
  padding: 9px 12px; border-bottom: 1px solid var(--border-subtle);
}
.es-head-label {
  font-size: 10px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.08em; color: var(--text-muted);
}
.es-head-note { font-size: 11px; font-family: var(--mono); color: var(--text-muted); }

.es-list { max-height: 320px; overflow-y: auto; padding: 4px; }
.es-row {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 7px 8px; border-radius: var(--radius-sm);
  background: none; border: 1px solid transparent; cursor: pointer;
  text-align: left;
  transition: background var(--transition), border-color var(--transition);
}
.es-row:hover { background: var(--surface-hover); }
.es-row:focus-visible { outline: none; background: var(--surface-hover); border-color: var(--accent); }
.es-row.sel { background: var(--accent-subtle); border-color: color-mix(in srgb, var(--accent) 30%, transparent); }
.es-check { width: 14px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; color: var(--accent); font-size: 10px; }
.es-row-name {
  flex: 1; min-width: 0; font-family: var(--mono); font-size: 12px; color: var(--text);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.es-row.sel .es-row-name { color: var(--accent); font-weight: 600; }

.es-foot {
  padding: 8px 12px; border-top: 1px solid var(--border-subtle);
  font-family: var(--mono); font-size: 10px; line-height: 1.45;
  color: var(--text-muted);
}

/* ── Transitions ─────────────────────────────────────────── */
.es-fade-enter-active, .es-fade-leave-active { transition: opacity var(--transition); }
.es-fade-enter-from, .es-fade-leave-to { opacity: 0; }
.es-pop-enter-active { transition: opacity 120ms ease, transform 120ms cubic-bezier(0.2, 0.9, 0.3, 1.2); }
.es-pop-leave-active { transition: opacity 90ms ease, transform 90ms ease; }
.es-pop-enter-from, .es-pop-leave-to { opacity: 0; transform: scale(0.96) translateY(-4px); }
</style>
