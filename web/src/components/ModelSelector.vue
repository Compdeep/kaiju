<template>
  <!--
    Reasoning-model picker for the chat header. Wired to the SAME server-side
    config the Settings modal drives: GET /api/v1/config (current llm.provider +
    llm.model) and GET /api/v1/models (the catalog). Selecting a model PATCHes
    llm.model, which kaiju live-applies to the reasoning lane. No per-request
    field is needed — the reasoning model is global config, not per-message.

    The UI is a custom modal-style dropdown (no native <select>): a compact
    header trigger opens an elevated, animated panel teleported to <body> so it
    can never be clipped and can clamp within the viewport.
  -->
  <button
    ref="triggerEl"
    type="button"
    class="ms-trigger"
    :class="{ open }"
    :disabled="loading"
    :title="'Reasoning model' + (provider ? ' · ' + provider : '')"
    aria-haspopup="listbox"
    :aria-expanded="open"
    @click.stop="toggle"
  >
    <svg class="ms-icon" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
      <circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/>
    </svg>
    <span class="ms-name">{{ loading ? 'loading…' : triggerLabel }}</span>
    <svg class="ms-caret" :class="{ up: open }" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2">
      <polyline points="6 9 12 15 18 9"/>
    </svg>
  </button>

  <Teleport to="body">
    <Transition name="ms-fade">
      <div v-if="open" class="ms-backdrop" @click="closePanel"></div>
    </Transition>
    <Transition name="ms-pop">
      <div
        v-if="open"
        ref="panelRef"
        class="ms-panel"
        role="listbox"
        aria-label="Reasoning model"
        :style="{ top: panelPos.top + 'px', left: panelPos.left + 'px', width: panelPos.width + 'px' }"
        @click.stop
      >
        <div class="ms-head">
          <span class="ms-head-label">Reasoning model</span>
          <span v-if="provider" class="ms-head-provider">{{ provider }}</span>
        </div>

        <div v-if="options.length > 8" class="ms-filter">
          <input
            v-model="filter"
            class="ms-filter-input"
            type="text"
            placeholder="filter models…"
            spellcheck="false"
            autocomplete="off"
            @keydown.stop
          />
        </div>

        <div class="ms-list">
          <button
            v-for="m in filtered"
            :key="m.id"
            type="button"
            class="ms-row"
            :class="{ sel: m.id === currentModel }"
            role="option"
            :aria-selected="m.id === currentModel"
            @click="select(m.id)"
          >
            <span class="ms-check">
              <svg v-if="m.id === currentModel" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.6"><polyline points="20 6 9 17 4 12"/></svg>
            </span>
            <span class="ms-row-name">{{ m.name || m.id }}</span>
            <span class="ms-badges">
              <span v-if="m.params && m.params !== '?'" class="ms-badge">{{ m.params }}</span>
              <span v-if="m.thinking" class="ms-badge">thinking</span>
            </span>
          </button>
          <div v-if="!filtered.length" class="ms-empty">{{ loading ? 'loading…' : 'no models' }}</div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
/**
 * desc: Header reasoning-model selector. Reads/writes the server-side reasoning
 * model (llm.model) via the existing /api/v1/config + /api/v1/models endpoints.
 * Custom modal-style dropdown UI (no native select).
 */
import { ref, computed, nextTick, onBeforeUnmount } from 'vue'
import api from '../api/client'

const loading = ref(true)
const provider = ref('')
const currentModel = ref('')
const allModels = ref([])

// ── Dropdown UI state ──
const open = ref(false)
const filter = ref('')
const triggerEl = ref(null)
const panelRef = ref(null)
const panelPos = ref({ top: 0, left: 0, width: 280 })
const PANEL_W = 280

/**
 * desc: Reasoning-capable models for the current provider. Mirrors the Settings
 * modal's reasoning filter (provider match + can call tools — the planner forces
 * a plan() tool call). The live model is always kept present so the header
 * reflects config even if that model falls outside the filter.
 * @returns {Array<Object>}
 */
const options = computed(() => {
  const list = allModels.value.filter(m => m.provider === provider.value && m.tools)
  if (currentModel.value && !list.some(m => m.id === currentModel.value)) {
    const cur = allModels.value.find(m => m.id === currentModel.value)
    return [cur || { id: currentModel.value, name: currentModel.value }, ...list]
  }
  return list
})

/** desc: options narrowed by the filter box (by name/id, case-insensitive). */
const filtered = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return options.value
  return options.value.filter(m => (m.name || m.id).toLowerCase().includes(q))
})

/** desc: The currently-selected model object (for the trigger label). */
const currentModelObj = computed(() => allModels.value.find(m => m.id === currentModel.value) || null)

/** desc: Trigger text — the current model's legible label, or a fallback. */
const triggerLabel = computed(() => currentModelObj.value ? modelLabel(currentModelObj.value) : (currentModel.value || 'select model'))

/**
 * desc: Compact, legible option label — name plus param-count / thinking badge.
 * @param {Object} m
 * @returns {string}
 */
function modelLabel(m) {
  const t = []
  if (m.params && m.params !== '?') t.push(m.params)
  if (m.thinking) t.push('thinking')
  return t.length ? `${m.name} · ${t.join(' · ')}` : m.name
}

/**
 * desc: Load current config + catalog from the server.
 * @returns {Promise<void>}
 */
async function load() {
  loading.value = true
  try {
    const [cfg, models] = await Promise.all([
      api.get('/api/v1/config'),
      api.get('/api/v1/models'),
    ])
    provider.value = cfg?.llm?.provider || ''
    currentModel.value = cfg?.llm?.model || ''
    allModels.value = Array.isArray(models) ? models : []
  } catch (e) {
    console.error('[model-selector] load failed:', e)
  }
  loading.value = false
}

/**
 * desc: Switch the reasoning model — PATCH llm.model (with provider so the
 * server keeps the pair consistent). Rolls back on failure.
 * @param {string} id
 * @returns {Promise<void>}
 */
async function onChange(id) {
  if (!id || id === currentModel.value) return
  const prev = currentModel.value
  currentModel.value = id
  try {
    await api.patch('/api/v1/config', { llm: { provider: provider.value, model: id } })
  } catch (e) {
    console.error('[model-selector] patch failed:', e)
    currentModel.value = prev
    alert('Failed to switch model: ' + e.message)
  }
}

// ── Panel open/close, positioning, keyboard ──

/** desc: Anchor the panel below the trigger, right-aligned, clamped on-screen. */
function positionPanel() {
  const el = triggerEl.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const w = PANEL_W
  let left = r.right - w                                   // right-align to trigger
  left = Math.min(left, window.innerWidth - w - 8)
  left = Math.max(8, left)
  const top = Math.min(r.bottom + 6, window.innerHeight - 80)
  panelPos.value = { top, left, width: w }
}

function openPanel() {
  open.value = true
  filter.value = ''
  nextTick(() => {
    positionPanel()
    window.addEventListener('resize', positionPanel)
    window.addEventListener('scroll', positionPanel, true)
    document.addEventListener('keydown', onKey)
    // Reasonable focus: the filter if present, else the selected/first row.
    const p = panelRef.value
    const target = p?.querySelector('.ms-filter-input')
      || p?.querySelector('.ms-row.sel')
      || p?.querySelector('.ms-row')
    target?.focus()
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

/** desc: Select a model (PATCH via onChange) then close the panel. */
async function select(id) {
  closePanel()
  await onChange(id)
}

onBeforeUnmount(() => {
  window.removeEventListener('resize', positionPanel)
  window.removeEventListener('scroll', positionPanel, true)
  document.removeEventListener('keydown', onKey)
})

load()

// Exposed so the chat header can re-sync after the Advanced settings modal
// (which can also change the reasoning model) closes.
defineExpose({ reload: load })
</script>

<style scoped>
/* ── Trigger ─────────────────────────────────────────────── */
.ms-trigger {
  display: inline-flex; align-items: center; gap: 6px;
  height: 30px; max-width: 210px; padding: 0 8px 0 10px;
  border: 1px solid var(--border); border-radius: 7px;
  background: var(--surface); color: var(--text-secondary);
  font-family: var(--mono); font-size: 12px; cursor: pointer;
  transition: color var(--transition), border-color var(--transition),
              background var(--transition), box-shadow var(--transition);
}
.ms-trigger:hover:not(:disabled), .ms-trigger.open {
  color: var(--accent); border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-subtle),
              0 0 10px color-mix(in srgb, var(--accent) 22%, transparent);
}
.ms-trigger:disabled { opacity: 0.6; cursor: default; }
.ms-icon { flex-shrink: 0; }
.ms-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 130px; }
.ms-caret { flex-shrink: 0; opacity: 0.6; transition: transform var(--transition); }
.ms-caret.up { transform: rotate(180deg); }

/* ── Backdrop (subtle dim + blur, catches outside-clicks) ── */
.ms-backdrop {
  position: fixed; inset: 0; z-index: 299;
  background: color-mix(in srgb, #000 10%, transparent);
  backdrop-filter: blur(1.5px);
  -webkit-backdrop-filter: blur(1.5px);
}

/* ── Panel ───────────────────────────────────────────────── */
.ms-panel {
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
/* Dark: "dark-moon" — deep dark surface, faint accent edge + glow. */
[data-theme="dark"] .ms-panel {
  background: linear-gradient(180deg, #0d0d13 0%, #0a0a0e 100%);
  border-color: rgba(255,255,255,0.08);
  box-shadow: 0 14px 42px rgba(0,0,0,0.6),
              0 0 0 1px rgba(255,255,255,0.03),
              0 0 26px color-mix(in srgb, var(--accent) 16%, transparent);
}

.ms-head {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 9px 12px; border-bottom: 1px solid var(--border-subtle);
}
.ms-head-label {
  font-size: 10px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.08em; color: var(--text-muted);
}
.ms-head-provider {
  font-size: 11px; font-family: var(--mono); color: var(--text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.ms-filter { padding: 8px 10px 2px; }
.ms-filter-input { width: 100%; font-family: var(--mono); font-size: 12px; padding: 6px 10px; }

.ms-list { max-height: 320px; overflow-y: auto; padding: 4px; }
.ms-row {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 7px 8px; border-radius: var(--radius-sm);
  background: none; border: 1px solid transparent; cursor: pointer;
  text-align: left;
  transition: background var(--transition), border-color var(--transition);
}
.ms-row:hover { background: var(--surface-hover); }
.ms-row:focus-visible { outline: none; background: var(--surface-hover); border-color: var(--accent); }
.ms-row.sel { background: var(--accent-subtle); border-color: color-mix(in srgb, var(--accent) 30%, transparent); }
.ms-check { width: 14px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; color: var(--accent); }
.ms-row-name {
  flex: 1; min-width: 0; font-family: var(--mono); font-size: 12px; color: var(--text);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ms-row.sel .ms-row-name { color: var(--accent); font-weight: 600; }
.ms-badges { display: flex; gap: 4px; flex-shrink: 0; }
.ms-badge {
  font-size: 9px; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.04em;
  padding: 1px 5px; border-radius: 3px;
  background: var(--surface-hover); color: var(--text-muted);
}
.ms-empty { padding: 16px; text-align: center; font-size: 11px; color: var(--text-muted); font-family: var(--mono); }

/* ── Transitions ─────────────────────────────────────────── */
/* Panel: fade + scale-up + slight slide-down from the top-right anchor. */
.ms-pop-enter-active { transition: opacity 0.18s cubic-bezier(0.16,1,0.3,1), transform 0.18s cubic-bezier(0.16,1,0.3,1); }
.ms-pop-leave-active { transition: opacity 0.13s ease-in, transform 0.13s ease-in; }
.ms-pop-enter-from, .ms-pop-leave-to { opacity: 0; transform: scale(0.96) translateY(-6px); }
.ms-pop-enter-to, .ms-pop-leave-from { opacity: 1; transform: scale(1) translateY(0); }

/* Backdrop: plain fade, synced timing. */
.ms-fade-enter-active, .ms-fade-leave-active { transition: opacity 0.18s ease; }
.ms-fade-enter-from, .ms-fade-leave-to { opacity: 0; }
</style>
