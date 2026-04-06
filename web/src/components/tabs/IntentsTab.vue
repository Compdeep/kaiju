<template>
  <div>
    <div class="tab-explainer">
      Intents define the safety levels the agent operates under. Ships with three default tiers loaded from kaiju.json; add more with sparse ranks (e.g. 50, 300). Each tool can be pinned to an intent — DB assignments win over the tool's compiled default. <strong>Restart kaiju after changes.</strong>
    </div>

    <!-- Intents section -->
    <div class="section-title">Intents</div>
    <div class="tab-header">
      <span class="tab-count">{{ intents.length }} intents</span>
      <button class="btn btn-sm btn-primary" @click="showForm = !showForm">{{ showForm ? 'cancel' : '+ new' }}</button>
    </div>

    <transition name="slide">
      <div v-if="showForm" class="form-card">
        <div v-if="editingBuiltin" class="builtin-warning">
          Editing a built-in intent. Name and rank are locked — only descriptions can be changed.
        </div>
        <div class="form-row">
          <div class="form-group"><label>name</label><input v-model="form.name" placeholder="e.g. diagnostic" :disabled="editing" /></div>
          <div class="form-group"><label>rank</label><input v-model.number="form.rank" type="number" placeholder="50" :disabled="editingBuiltin" /></div>
        </div>
        <div class="form-group"><label>description (UI)</label><input v-model="form.description" placeholder="short human-readable summary" /></div>
        <div class="form-group"><label>prompt description (shown to LLM)</label><textarea v-model="form.prompt_description" rows="3" placeholder="what this intent level allows — shown to the planner to help it pick the right level" /></div>
        <button class="btn btn-sm btn-primary" @click="saveIntent">{{ editing ? 'update' : 'create' }}</button>
      </div>
    </transition>

    <table v-if="intents.length" class="intent-table">
      <thead><tr><th>name</th><th>rank</th><th>description</th><th></th></tr></thead>
      <tbody>
        <tr v-for="i in intents" :key="i.name">
          <td>
            <code>{{ i.name }}</code>
            <span v-if="i.is_builtin" class="badge-builtin">builtin</span>
          </td>
          <td><code class="dim">{{ i.rank }}</code></td>
          <td><span class="dim">{{ i.description }}</span></td>
          <td class="actions">
            <button class="btn-icon" @click="editIntent(i)" title="Edit"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>
            <button class="btn-icon del" :disabled="i.is_builtin" @click="removeIntent(i.name)" :title="i.is_builtin ? 'Cannot delete builtin' : 'Delete'"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- Tool assignments section -->
    <div class="section-title" style="margin-top: 24px;">Tool assignments</div>
    <div class="sub-explainer">
      Set a maximum intent rank for a tool. The assignment acts as a ceiling — the tool's per-invocation impact is still checked, but cannot exceed the assigned rank. Tools without an assignment use their compiled default.
    </div>
    <table v-if="toolIntents.length" class="intent-table">
      <thead><tr><th>tool</th><th>intent</th><th>default</th><th></th></tr></thead>
      <tbody>
        <tr v-for="t in toolIntents" :key="t.tool_name">
          <td><code>{{ t.tool_name }}</code></td>
          <td>
            <select :value="t.intent_name" @change="assignTool(t.tool_name, $event.target.value)" class="intent-select">
              <option v-for="i in intents" :key="i.name" :value="i.name">{{ i.name }}</option>
            </select>
          </td>
          <td>
            <code class="dim">{{ t.default_intent }}</code>
            <span v-if="t.has_override" class="badge-pinned">overridden</span>
          </td>
          <td class="actions">
            <button v-if="t.has_override" class="btn-icon" @click="resetTool(t.tool_name)" title="Reset to default"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg></button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup>
/**
 * desc: Intents management tab for creating, editing, and deleting configurable
 *       IBE levels plus per-tool intent overrides. Changes require kaiju restart.
 */
import { ref, onMounted } from 'vue'
import api from '../../api/client'

const intents = ref([])
const toolIntents = ref([])
const showForm = ref(false)
const editing = ref(false)
const editingBuiltin = ref(false)
const form = ref({ name: '', rank: 0, description: '', prompt_description: '' })

async function loadIntents() {
  try { intents.value = await api.get('/api/v1/intents') } catch {}
}

async function loadToolIntents() {
  try { toolIntents.value = await api.get('/api/v1/tool-intents') } catch {}
}

function editIntent(i) {
  // Mutate fields in place rather than replacing the ref object — more
  // reliable reactivity and keeps existing template bindings stable.
  form.value.name = i.name
  form.value.rank = i.rank
  form.value.description = i.description || ''
  form.value.prompt_description = i.prompt_description || ''
  editing.value = true
  editingBuiltin.value = !!i.is_builtin
  showForm.value = true
}

async function saveIntent() {
  try {
    editing.value
      ? await api.put(`/api/v1/intents/${form.value.name}`, form.value)
      : await api.post('/api/v1/intents', form.value)
    showForm.value = false
    editing.value = false
    editingBuiltin.value = false
    form.value.name = ''
    form.value.rank = 0
    form.value.description = ''
    form.value.prompt_description = ''
    await loadIntents()
  } catch (e) { alert(e.message) }
}

async function removeIntent(name) {
  if (!confirm(`Delete intent "${name}"?`)) return
  try { await api.del(`/api/v1/intents/${name}`); await loadIntents(); await loadToolIntents() } catch (e) { alert(e.message) }
}

async function assignTool(toolName, intentName) {
  try {
    await api.put(`/api/v1/tool-intents/${toolName}`, { intent_name: intentName })
    await loadToolIntents()
  } catch (e) { alert(e.message) }
}

async function resetTool(toolName) {
  try {
    await api.del(`/api/v1/tool-intents/${toolName}`)
    await loadToolIntents()
  } catch (e) { alert(e.message) }
}

onMounted(() => { loadIntents(); loadToolIntents() })
</script>

<style scoped>
.tab-explainer {
  font-size: 11px; color: var(--text-muted); line-height: 1.5;
  padding: 8px 10px; margin-bottom: 12px;
  background: var(--surface-hover); border-radius: var(--radius-sm);
  font-family: var(--font);
}
.sub-explainer {
  font-size: 10px; color: var(--text-muted); margin-bottom: 8px;
}
.section-title {
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em;
  color: var(--text-muted); font-weight: 600; margin-bottom: 8px;
}
.tab-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.tab-count { font-size: 12px; color: var(--text-muted); font-family: var(--mono); }
.form-card { background: var(--surface-hover); border-radius: var(--radius); padding: 16px; margin-bottom: 14px; }
.builtin-warning {
  font-size: 11px;
  color: var(--signal-amber);
  background: var(--signal-amber-bg);
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  margin-bottom: 10px;
}
.form-row { display: grid; grid-template-columns: 2fr 1fr; gap: 10px; margin-bottom: 10px; }
.form-group textarea { width: 100%; font-family: var(--mono); font-size: 12px; resize: vertical; }
code { font-family: var(--mono); font-size: 12px; }
.dim { color: var(--text-secondary); font-size: 11px; }
.actions { display: flex; gap: 2px; }
.del { color: var(--signal-red) !important; }
.del:disabled { opacity: 0.3; cursor: not-allowed; }
.badge-builtin {
  font-size: 9px; padding: 1px 5px; margin-left: 6px;
  background: var(--accent-bg, rgba(100, 150, 200, 0.15));
  color: var(--accent, #6ea8d8);
  border-radius: 3px; font-weight: 600; text-transform: uppercase;
}
.badge-pinned {
  font-size: 9px; padding: 1px 5px; margin-left: 6px;
  background: var(--signal-amber-bg);
  color: var(--signal-amber);
  border-radius: 3px; font-weight: 600; text-transform: uppercase;
}
.intent-table { width: 100%; border-collapse: collapse; }
.intent-table th { text-align: left; font-size: 10px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); padding: 6px 8px; border-bottom: 1px solid var(--border); }
.intent-table td { padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
.intent-select {
  font-size: 11px; font-family: var(--mono);
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 3px; padding: 2px 6px; color: var(--text);
}
</style>
