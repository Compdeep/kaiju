<template>
  <div>
    <div class="tab-explainer">
      Groups let you assign the same set of scopes to multiple users at once. Add a user to a group instead of configuring scopes individually.
    </div>

    <div class="tab-header">
      <span class="tab-count">{{ groups.length }} groups</span>
      <button class="btn btn-sm btn-primary" @click="showForm = !showForm">{{ showForm ? 'cancel' : '+ new' }}</button>
    </div>

    <transition name="slide">
      <div v-if="showForm" class="form-card">
        <div class="form-row">
          <div class="form-group"><label>name</label><input v-model="form.name" :disabled="editing" /></div>
          <div class="form-group"><label>description</label><input v-model="form.description" /></div>
        </div>
        <div class="form-group"><label>scopes</label><input v-model="scopesInput" placeholder="admin, standard" /></div>
        <button class="btn btn-sm btn-primary" @click="save">{{ editing ? 'update' : 'create' }}</button>
      </div>
    </transition>

    <table v-if="groups.length">
      <thead><tr><th>name</th><th>description</th><th>scopes</th><th></th></tr></thead>
      <tbody>
        <tr v-for="g in groups" :key="g.name">
          <td><code>{{ g.name }}</code></td>
          <td class="dim">{{ g.description }}</td>
          <td><code class="dim">{{ (g.scopes||[]).join(', ') || '—' }}</code></td>
          <td class="actions"><button class="btn-icon" @click="edit(g)"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button><button class="btn-icon del" @click="remove(g.name)"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup>
/**
 * desc: Groups management tab for creating, editing, and deleting groups that bundle scopes for multiple users
 */
import { ref, onMounted } from 'vue'
import api from '../../api/client'

const groups = ref([])
const showForm = ref(false)
const editing = ref(false)
const form = ref({ name: '', description: '' })
const scopesInput = ref('')

/**
 * desc: Parse a comma-separated string into a trimmed, non-empty array of strings
 * @param {string} s - Comma-separated input string
 * @returns {Array<string>} Parsed array of trimmed values
 */
function p(s) { return s.split(',').map(x=>x.trim()).filter(Boolean) }

/**
 * desc: Fetch all groups from the server and update local state
 * @returns {Promise<void>}
 */
async function load() { try { groups.value = await api.get('/api/v1/groups') } catch {} }

/**
 * desc: Populate the form with an existing group's data for editing
 * @param {Object} g - The group object to edit
 * @returns {void}
 */
function edit(g) { form.value = { ...g }; scopesInput.value = (g.scopes||[]).join(', '); editing.value = true; showForm.value = true }

/**
 * desc: Create or update a group on the server and reset the form
 * @returns {Promise<void>}
 */
async function save() { const d = { ...form.value, scopes: p(scopesInput.value) }; try { editing.value ? await api.put(`/api/v1/groups/${form.value.name}`,d) : await api.post('/api/v1/groups',d); showForm.value=false; editing.value=false; await load() } catch(e) { alert(e.message) } }

/**
 * desc: Delete a group by name after user confirmation
 * @param {string} n - The group name to delete
 * @returns {Promise<void>}
 */
async function remove(n) { if(!confirm(`Delete "${n}"?`)) return; try { await api.del(`/api/v1/groups/${n}`); await load() } catch(e) { alert(e.message) } }
onMounted(load)
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
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
code { font-family: var(--mono); font-size: 12px; }
.dim { color: var(--text-secondary); font-size: 11px; }
.actions { display: flex; gap: 2px; }
.del { color: var(--signal-red) !important; }
</style>
