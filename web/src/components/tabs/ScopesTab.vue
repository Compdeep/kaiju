<template>
  <div>
    <div class="tab-explainer">
      Scopes define which tools a user can access and how destructive those tools can be. Assign scopes to users to control what the agent can do on their behalf. Applies to both chat and API requests.
    </div>

    <div class="tab-header">
      <span class="tab-count">{{ scopes.length }} scopes</span>
      <button class="btn btn-sm btn-primary" @click="showForm = !showForm">{{ showForm ? 'cancel' : '+ new' }}</button>
    </div>

    <transition name="slide">
      <div v-if="showForm" class="form-card">
        <div class="form-row">
          <div class="form-group"><label>name</label><input v-model="form.name" placeholder="operator" :disabled="editing" /></div>
          <div class="form-group"><label>description</label><input v-model="form.description" placeholder="what this scope allows" /></div>
        </div>

        <!-- Tool selector -->
        <div class="form-group">
          <label>tools</label>
          <div class="tool-toggle">
            <label class="toggle-row">
              <input type="checkbox" :checked="allTools" @change="toggleAll" />
              <span class="toggle-label">All tools</span>
            </label>
          </div>
          <div v-if="!allTools" class="tool-grid">
            <label v-for="t in availableTools" :key="t.name" class="tool-check" :title="t.description">
              <input type="checkbox" :value="t.name" v-model="selectedTools" />
              <span class="tool-name">{{ t.name }}</span>
              <span :class="['tool-badge', 'impact-' + t.impact]">{{ impactLabel(t.impact) }}</span>
            </label>
          </div>
        </div>

        <!-- Per-tool caps -->
        <div class="form-group">
          <label>impact caps <span class="hint-inline">(optional — limit max impact per tool)</span></label>
          <div class="cap-grid">
            <div v-for="t in cappableTools" :key="t" class="cap-row">
              <code>{{ t }}</code>
              <select v-model="caps[t]" class="cap-select">
                <option :value="undefined">no cap</option>
                <option :value="0">observe (0)</option>
                <option :value="1">operate (1)</option>
                <option :value="2">override (2)</option>
              </select>
            </div>
          </div>
          <div v-if="!cappableTools.length" class="hint">Select tools above to set caps</div>
        </div>

        <button class="btn btn-sm btn-primary" @click="save">{{ editing ? 'update' : 'create' }}</button>
      </div>
    </transition>

    <table v-if="scopes.length">
      <thead><tr><th>name</th><th>tools</th><th>caps</th><th></th></tr></thead>
      <tbody>
        <tr v-for="p in scopes" :key="p.name">
          <td><code>{{ p.name }}</code><br><span class="dim">{{ p.description }}</span></td>
          <td><code class="dim">{{ fmtTools(p.tools) }}</code></td>
          <td><code class="dim">{{ fmtCap(p.cap) }}</code></td>
          <td class="actions">
            <button class="btn-icon" @click="edit(p)" title="Edit"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>
            <button class="btn-icon del" @click="remove(p.name)" title="Delete"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup>
/**
 * desc: Scopes management tab for creating, editing, and deleting tool-access scopes with per-tool impact caps
 */
import { ref, computed, onMounted } from 'vue'
import api from '../../api/client'

const scopes = ref([])
const availableTools = ref([])
const showForm = ref(false)
const editing = ref(false)
const form = ref({ name: '', description: '' })
const selectedTools = ref([])
const allTools = ref(false)
const caps = ref({})

/**
 * desc: Fetch all scopes from the server and update local state
 * @returns {Promise<void>}
 */
async function load() {
  try { scopes.value = await api.get('/api/v1/scopes') } catch {}
}

/**
 * desc: Fetch all available tools from the server for the scope tool selector
 * @returns {Promise<void>}
 */
async function loadTools() {
  try {
    availableTools.value = await api.get('/api/v1/tools')
  } catch (e) {
    console.error('[scopes] failed to load tools:', e)
  }
}

/**
 * desc: Compute the list of selected tools that have impact > 0 and can have caps applied
 * @returns {Array<string>} Tool names eligible for impact caps
 */
const cappableTools = computed(() => {
  if (allTools.value) {
    return availableTools.value.filter(t => t.impact > 0).map(t => t.name)
  }
  return selectedTools.value.filter(name => {
    const t = availableTools.value.find(x => x.name === name)
    return t && t.impact > 0
  })
})

/**
 * desc: Toggle between all-tools mode and individual tool selection
 * @param {Event} e - The checkbox change event
 * @returns {void}
 */
function toggleAll(e) {
  allTools.value = e.target.checked
  if (allTools.value) {
    selectedTools.value = []
  }
}

/**
 * desc: Map an impact level number to its human-readable label
 * @param {number} i - Impact level (0=observe, 1=operate, 2=override)
 * @returns {string} Impact label string
 */
function impactLabel(i) {
  return ['observe', 'operate', 'override'][i] || '?'
}

/**
 * desc: Populate the form with an existing scope's data for editing
 * @param {Object} p - The scope object to edit
 * @returns {Promise<void>}
 */
async function edit(p) {
  if (!availableTools.value.length) await loadTools()
  form.value = { name: p.name, description: p.description }
  if (p.tools && p.tools.length === 1 && p.tools[0] === '*') {
    allTools.value = true
    selectedTools.value = []
  } else {
    allTools.value = false
    selectedTools.value = [...(p.tools || [])]
  }
  caps.value = { ...(p.cap || {}) }
  editing.value = true
  showForm.value = true
}

/**
 * desc: Build the tools array for the scope payload, returning ['*'] if all tools are selected
 * @returns {Array<string>} Tool names or wildcard array
 */
function buildTools() {
  if (allTools.value) return ['*']
  return selectedTools.value
}

/**
 * desc: Build the cap object from the current caps state, filtering out unset values
 * @returns {Object} Map of tool names to impact cap levels
 */
function buildCap() {
  const c = {}
  for (const [k, v] of Object.entries(caps.value)) {
    if (v !== undefined && v !== null) c[k] = v
  }
  return c
}

/**
 * desc: Create or update a scope on the server and reset the form
 * @returns {Promise<void>}
 */
async function save() {
  const d = { ...form.value, tools: buildTools(), cap: buildCap() }
  try {
    editing.value
      ? await api.put(`/api/v1/scopes/${form.value.name}`, d)
      : await api.post('/api/v1/scopes', d)
    showForm.value = false
    editing.value = false
    form.value = { name: '', description: '' }
    selectedTools.value = []
    allTools.value = false
    caps.value = {}
    await load()
  } catch (e) { alert(e.message) }
}

/**
 * desc: Delete a scope by name after user confirmation
 * @param {string} n - The scope name to delete
 * @returns {Promise<void>}
 */
async function remove(n) {
  if (!confirm(`Delete "${n}"?`)) return
  try { await api.del(`/api/v1/scopes/${n}`); await load() } catch (e) { alert(e.message) }
}

/**
 * desc: Format a tools array into a compact display string
 * @param {Array<string>} s - Array of tool names
 * @returns {string} Formatted tools summary
 */
function fmtTools(s) {
  if (!s || !s.length) return '\u2014'
  if (s.length === 1 && s[0] === '*') return 'all tools'
  return s.length > 4 ? `${s.slice(0, 4).join(', ')} +${s.length - 4}` : s.join(', ')
}

/**
 * desc: Format a cap object into a compact display string of tool:impact pairs
 * @param {Object} c - Map of tool names to impact levels
 * @returns {string} Formatted caps summary
 */
function fmtCap(c) {
  if (!c || !Object.keys(c).length) return '\u2014'
  return Object.entries(c).map(([k, v]) => `${k}: ${impactLabel(v)}`).join(', ')
}

onMounted(() => { load(); loadTools() })
</script>

<style scoped>
.tab-explainer {
  font-size: 11px; color: var(--text-muted); line-height: 1.5;
  padding: 8px 10px; margin-bottom: 12px;
  background: var(--surface-hover); border-radius: var(--radius-sm);
  font-family: var(--font);
}
.tab-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.tab-count { font-size: 12px; color: var(--text-muted); font-family: var(--mono); }
.form-card { background: var(--surface-hover); border-radius: var(--radius); padding: 16px; margin-bottom: 14px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 10px; }
.hint { font-size: 10px; color: var(--text-muted); margin-top: 4px; }
.hint-inline { font-size: 10px; color: var(--text-muted); font-weight: 400; }
code { font-family: var(--mono); font-size: 12px; }
.dim { color: var(--text-secondary); font-size: 11px; }
.actions { display: flex; gap: 2px; }
.del { color: var(--signal-red) !important; }

/* Tool toggle */
.tool-toggle { margin-bottom: 8px; }
.toggle-row {
  display: flex; align-items: center; gap: 8px;
  cursor: pointer; font-size: 12px; font-family: var(--mono);
  color: var(--text-secondary);
}
.toggle-row input { accent-color: var(--accent); }

/* Tool grid */
.tool-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 4px; max-height: 240px; overflow-y: auto;
  padding: 8px; border: 1px solid var(--border); border-radius: var(--radius-sm);
  background: var(--surface);
}
.tool-check {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px; font-family: var(--mono);
  color: var(--text-secondary); cursor: pointer;
  padding: 3px 4px; border-radius: 3px;
  transition: background var(--transition);
}
.tool-check:hover { background: var(--surface-hover); }
.tool-check input { accent-color: var(--accent); flex-shrink: 0; }
.tool-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tool-badge {
  font-size: 9px; padding: 1px 4px; border-radius: 3px;
  font-weight: 600; text-transform: uppercase; letter-spacing: 0.03em;
  flex-shrink: 0;
}
.impact-0 { background: var(--signal-green-bg); color: var(--signal-green); }
.impact-1 { background: var(--signal-amber-bg); color: var(--signal-amber); }
.impact-2 { background: var(--signal-red-bg); color: var(--signal-red); }

/* Cap grid */
.cap-grid {
  display: flex; flex-direction: column; gap: 4px;
  max-height: 160px; overflow-y: auto;
}
.cap-row {
  display: flex; align-items: center; gap: 8px;
  font-size: 11px; font-family: var(--mono);
}
.cap-row code { min-width: 120px; color: var(--text-secondary); }
.cap-select {
  font-size: 11px; font-family: var(--mono);
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 3px; padding: 2px 6px; color: var(--text);
}
</style>
