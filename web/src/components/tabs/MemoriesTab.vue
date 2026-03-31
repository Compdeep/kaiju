<template>
  <div>
    <div class="tab-explainer">
      Long-term memories persist across conversations. Semantic memories store facts, episodic memories store experiences. The agent recalls relevant memories automatically during queries.
    </div>

    <div class="tab-header">
      <div class="search-row">
        <input v-model="searchQuery" placeholder="Search memories..." class="search-input" @input="search" />
        <select v-model="typeFilter" @change="search" class="type-filter">
          <option value="">all types</option>
          <option value="semantic">semantic</option>
          <option value="episodic">episodic</option>
        </select>
      </div>
      <button class="btn btn-sm btn-primary" @click="showForm = !showForm">{{ showForm ? 'cancel' : '+ new' }}</button>
    </div>

    <transition name="slide">
      <div v-if="showForm" class="form-card">
        <div class="form-row">
          <div class="form-group"><label>key</label><input v-model="form.key" placeholder="e.g. user-preference" /></div>
          <div class="form-group"><label>type</label>
            <select v-model="form.type"><option value="semantic">semantic (fact)</option><option value="episodic">episodic (experience)</option></select>
          </div>
        </div>
        <div class="form-group"><label>content</label><textarea v-model="form.content" rows="3" placeholder="The fact or experience to remember..."></textarea></div>
        <div class="form-group"><label>tags</label><input v-model="tagsInput" placeholder="comma-separated tags" /></div>
        <button class="btn btn-sm btn-primary" @click="store">store</button>
      </div>
    </transition>

    <div class="memory-list">
      <div v-for="m in memories" :key="m.id" class="memory-item">
        <div class="memory-top">
          <code class="memory-key">{{ m.key }}</code>
          <span :class="['badge', m.type === 'semantic' ? 'badge-observe' : 'badge-operate']">{{ m.type }}</span>
          <button class="btn-icon del" @click="forget(m.id)" title="Forget">
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
          </button>
        </div>
        <div class="memory-content">{{ m.content }}</div>
        <div v-if="m.tags && m.tags.length" class="memory-tags">
          <span v-for="t in m.tags" :key="t" class="tag">{{ t }}</span>
        </div>
      </div>
    </div>
    <p v-if="!memories.length" class="empty">no memories stored — the agent will remember facts across conversations</p>
  </div>
</template>

<script setup>
/**
 * desc: Memories management tab for searching, creating, and deleting semantic and episodic long-term memories
 */
import { ref, onMounted } from 'vue'
import api from '../../api/client'

const memories = ref([])
const searchQuery = ref('')
const typeFilter = ref('')
const showForm = ref(false)
const form = ref({ key: '', content: '', type: 'semantic' })
const tagsInput = ref('')

/**
 * desc: Search memories on the server by query text and type filter, updating the local list
 * @returns {Promise<void>}
 */
async function search() {
  const params = new URLSearchParams()
  if (searchQuery.value) params.set('q', searchQuery.value)
  if (typeFilter.value) params.set('type', typeFilter.value)
  try { memories.value = await api.get(`/api/v1/memories?${params}`) } catch {}
}

/**
 * desc: Store a new memory on the server with the form data and tags, then refresh the list
 * @returns {Promise<void>}
 */
async function store() {
  const tags = tagsInput.value.split(',').map(t => t.trim()).filter(Boolean)
  try {
    await api.post('/api/v1/memories', { ...form.value, tags })
    showForm.value = false
    form.value = { key: '', content: '', type: 'semantic' }
    tagsInput.value = ''
    await search()
  } catch (err) { alert(err.message) }
}

/**
 * desc: Delete a memory by ID after user confirmation and refresh the list
 * @param {string} id - The memory ID to forget
 * @returns {Promise<void>}
 */
async function forget(id) {
  if (!confirm('Forget this memory?')) return
  try { await api.del(`/api/v1/memories/${id}`); await search() } catch (err) { alert(err.message) }
}

onMounted(search)
</script>

<style scoped>
.tab-explainer {
  font-size: 11px; color: var(--text-muted); line-height: 1.5;
  padding: 8px 10px; margin-bottom: 12px;
  background: var(--surface-hover); border-radius: var(--radius-sm);
  font-family: var(--font);
}
.tab-header { display: flex; justify-content: space-between; align-items: flex-start; gap: 10px; margin-bottom: 14px; }
.search-row { display: flex; gap: 8px; flex: 1; }
.search-input { flex: 1; font-size: 12px; padding: 6px 10px; }
.type-filter { font-size: 12px; padding: 6px 8px; width: 120px; }
.form-card { background: var(--surface-hover); border-radius: var(--radius); padding: 16px; margin-bottom: 14px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.memory-list { display: flex; flex-direction: column; gap: 8px; }
.memory-item {
  padding: 10px 12px; border-radius: var(--radius-sm);
  border: 1px solid var(--border-subtle);
  transition: background var(--transition);
}
.memory-item:hover { background: var(--surface-hover); }
.memory-top { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.memory-key { font-family: var(--mono); font-size: 12px; font-weight: 600; }
.memory-content { font-size: 13px; color: var(--text-secondary); line-height: 1.5; }
.memory-tags { display: flex; gap: 4px; margin-top: 6px; }
.tag { font-size: 10px; font-family: var(--mono); padding: 1px 6px; border-radius: 3px; background: var(--surface-hover); color: var(--text-muted); }
.del { margin-left: auto; color: var(--signal-red) !important; }
.empty { color: var(--text-muted); font-size: 13px; }
</style>
