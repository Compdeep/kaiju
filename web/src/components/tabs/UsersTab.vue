<template>
  <div>
    <div class="tab-explainer">
      Users authenticate via JWT for both chat and API access. Each user has a max intent level and one or more scopes that control which tools they can use. The first user created is automatically assigned the admin scope.
    </div>

    <div class="tab-header">
      <span class="tab-count">{{ users.length }} users</span>
      <button class="btn btn-sm btn-primary" @click="showForm = !showForm">{{ showForm ? 'cancel' : '+ new' }}</button>
    </div>

    <transition name="slide">
      <div v-if="showForm" class="form-card">
        <div class="form-row">
          <div class="form-group"><label>username</label><input v-model="form.username" :disabled="editing" /></div>
          <div v-if="!editing" class="form-group"><label>password</label><input v-model="form.password" type="password" /></div>
        </div>
        <div class="form-row">
          <div class="form-group"><label>max intent</label>
            <select v-model.number="form.max_intent">
              <option v-for="i in intentOptions" :key="i.name" :value="i.rank">{{ i.name }} ({{ i.rank }})</option>
            </select>
          </div>
          <div class="form-group"><label>scopes</label><input v-model="scopesInput" placeholder="admin, standard, readonly" /></div>
        </div>
        <div class="form-group"><label>groups</label><input v-model="groupsInput" placeholder="engineering" /></div>
        <button class="btn btn-sm btn-primary" @click="save">{{ editing ? 'update' : 'create' }}</button>
      </div>
    </transition>

    <table v-if="users.length">
      <thead><tr><th>user</th><th>intent</th><th>scopes</th><th>groups</th><th></th></tr></thead>
      <tbody>
        <tr v-for="u in users" :key="u.username">
          <td><code>{{ u.username }}</code></td>
          <td><span class="intent-tag">{{ intentName(u.max_intent) }}</span></td>
          <td><code class="dim">{{ (u.scopes||[]).join(', ') || '—' }}</code></td>
          <td><code class="dim">{{ (u.groups||[]).join(', ') || '—' }}</code></td>
          <td class="actions"><button class="btn-icon" @click="edit(u)"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button><button class="btn-icon del" @click="remove(u.username)"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup>
/**
 * desc: Users management tab for creating, editing, and deleting users with intent levels, scopes, and groups
 */
import { ref, onMounted } from 'vue'
import api from '../../api/client'

const users = ref([])
const showForm = ref(false)
const editing = ref(false)
const form = ref({ username: '', password: '', max_intent: 0 })
const scopesInput = ref('')
const groupsInput = ref('')
const intentOptions = ref([])

/**
 * desc: Resolve a stored max_intent rank to its registry name.
 * @param {number} r - Rank from the user record
 * @returns {string} Intent name, or "rank(N)" if not in the registry
 */
function intentName(r) {
  const match = intentOptions.value.find(o => o.rank === r)
  return match ? match.name : `rank(${r})`
}

/**
 * desc: Load the intent registry. The registry is the sole source of truth —
 *       no hardcoded fallback. On failure, intentOptions stays empty.
 * @returns {Promise<void>}
 */
async function loadIntents() {
  try {
    const list = await api.get('/api/v1/intents')
    if (Array.isArray(list)) {
      intentOptions.value = list.map(i => ({ name: i.name, rank: i.rank }))
    }
  } catch (e) {
    console.error('[users] failed to load intents registry:', e)
    intentOptions.value = []
  }
}

/**
 * desc: Parse a comma-separated string into a trimmed, non-empty array of strings
 * @param {string} s - Comma-separated input string
 * @returns {Array<string>} Parsed array of trimmed values
 */
function p(s) { return s.split(',').map(x=>x.trim()).filter(Boolean) }

/**
 * desc: Fetch all users from the server and update local state
 * @returns {Promise<void>}
 */
async function load() { try { users.value = await api.get('/api/v1/users') } catch {} }

/**
 * desc: Populate the form with an existing user's data for editing
 * @param {Object} u - The user object to edit
 * @returns {void}
 */
function edit(u) { form.value = { username: u.username, password: '', max_intent: u.max_intent }; scopesInput.value = (u.scopes||[]).join(', '); groupsInput.value = (u.groups||[]).join(', '); editing.value = true; showForm.value = true }

/**
 * desc: Create or update a user on the server and reset the form
 * @returns {Promise<void>}
 */
async function save() { try { editing.value ? await api.put(`/api/v1/users/${form.value.username}`, { max_intent: form.value.max_intent, scopes: p(scopesInput.value), groups: p(groupsInput.value) }) : await api.post('/api/v1/users', { ...form.value, scopes: p(scopesInput.value) }); showForm.value=false; editing.value=false; await load() } catch(e) { alert(e.message) } }

/**
 * desc: Delete a user by username after confirmation
 * @param {string} u - The username to delete
 * @returns {Promise<void>}
 */
async function remove(u) { if(!confirm(`Delete "${u}"?`)) return; try { await api.del(`/api/v1/users/${u}`); await load() } catch(e) { alert(e.message) } }
onMounted(() => { load(); loadIntents() })
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
.intent-tag { font-size: 11px; font-family: var(--mono); color: var(--text-muted); }
.actions { display: flex; gap: 2px; }
.del { color: var(--signal-red) !important; }
</style>
