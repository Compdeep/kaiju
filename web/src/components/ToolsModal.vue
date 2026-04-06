<template>
  <div class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-panel">
      <div class="modal-header">
        <h2>tools</h2>
        <button class="btn-icon" @click="$emit('close')">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
      </div>
      <div class="modal-tabs">
        <button :class="['modal-tab', { active: tab === 'tools' }]" @click="tab = 'tools'">tools ({{ tools.length }})</button>
        <button :class="['modal-tab', { active: tab === 'skills' }]" @click="tab = 'skills'">skills</button>
      </div>
      <div class="modal-body">
        <transition name="slide" mode="out-in">

          <div v-if="tab === 'tools'" key="tools">
            <template v-for="group in toolGroups" :key="group.label">
              <div v-if="group.tools.length" class="tool-group">
                <div class="tool-group-label">{{ group.label }}</div>
                <div class="tool-list">
                  <div v-for="t in group.tools" :key="t.name" class="tool-row">
                    <div class="tool-left">
                      <code class="tool-name">{{ t.name }}</code>
                      <span class="tool-desc">{{ t.description }}</span>
                    </div>
                    <span :class="['badge', impactClass(t.default_impact)]">{{ impactLabel(t.default_impact) }}</span>
                  </div>
                </div>
              </div>
            </template>
            <p v-if="!tools.length" class="empty">no tools loaded</p>
          </div>

          <div v-else key="skills">
            <div class="skills-info">
              <p>Skills are user-defined tools written in markdown (SKILL.md files).</p>
              <p>Drop them into <code>~/.kaiju/skills/&lt;name&gt;/SKILL.md</code> and they'll be hot-reloaded automatically.</p>
              <div class="skill-example">
                <pre><code>---
name: my_skill
description: What this skill does
impact: 1
parameters:
  input: { type: string, description: "..." }
---

Instructions for the agent on how to execute this skill...</code></pre>
              </div>
            </div>
          </div>

        </transition>
      </div>
    </div>
  </div>
</template>

<script setup>
/**
 * desc: Tools and skills modal that displays registered tools grouped by source and shows skill authoring instructions
 */
import { ref, computed, onMounted } from 'vue'
import api from '../api/client'

defineEmits(['close'])
const tab = ref('tools')
const tools = ref([])
const intentOptions = ref([])

/**
 * desc: Group tools by source type (skills, custom, builtin) for categorized display
 * @returns {Array<{label: string, tools: Array<Object>}>} Grouped tool arrays
 */
const toolGroups = computed(() => {
  const skills = tools.value.filter(t => t.source && t.source.startsWith('skillmd'))
  const custom = tools.value.filter(t => t.source === 'custom')
  const builtin = tools.value.filter(t => t.source === 'builtin' || (!t.source))
  const groups = []
  if (skills.length) groups.push({ label: 'skills', tools: skills })
  if (custom.length) groups.push({ label: 'custom', tools: custom })
  if (builtin.length) groups.push({ label: 'builtin', tools: builtin })
  return groups
})

onMounted(async () => {
  try { tools.value = await api.get('/api/v1/tools') } catch {}
  try {
    const list = await api.get('/api/v1/intents')
    if (Array.isArray(list)) {
      intentOptions.value = list.map(i => ({ name: i.name, rank: i.rank }))
    }
  } catch (e) {
    console.error('[tools] failed to load intents registry:', e)
    intentOptions.value = []
  }
})

/**
 * desc: CSS class for the impact badge. Buckets by position in the registry's
 *       ordered rank list: bottom third → lowest tier, top third → highest,
 *       middle → mid. No hardcoded names.
 * @param {number} rank - Impact rank
 * @returns {string} CSS class name for the badge
 */
function impactClass(rank) {
  const sorted = [...intentOptions.value].sort((a, b) => a.rank - b.rank)
  if (!sorted.length) return 'badge-tier-low'
  const top = sorted[sorted.length - 1].rank
  if (rank <= 0) return 'badge-tier-low'
  if (rank >= top) return 'badge-tier-high'
  return 'badge-tier-mid'
}

/**
 * desc: Resolve an impact rank to its registry name. Unknown ranks render as
 *       "rank(N)" — nothing is hardcoded.
 * @param {number} rank - Impact rank
 * @returns {string} Intent name, or "rank(N)" if not in the registry
 */
function impactLabel(rank) {
  const match = intentOptions.value.find(o => o.rank === rank)
  return match ? match.name : `rank(${rank})`
}
</script>

<style scoped>
.tool-group { margin-bottom: 16px; }
.tool-group-label {
  font-size: 10px; font-weight: 700; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.08em;
  color: var(--text-muted); margin-bottom: 6px;
  padding-bottom: 4px; border-bottom: 1px solid var(--border-subtle);
}
.tool-list { display: flex; flex-direction: column; }
.tool-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--border-subtle);
}
.tool-left { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
.tool-name { font-family: var(--mono); font-size: 13px; font-weight: 500; }
.tool-desc { font-size: 11px; color: var(--text-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 500px; }
.empty { color: var(--text-muted); font-size: 13px; }
.skills-info p { font-size: 13px; color: var(--text-secondary); margin-bottom: 10px; }
.skills-info code { font-family: var(--mono); font-size: 12px; background: var(--surface-hover); padding: 1px 4px; border-radius: 3px; }
.skill-example { margin-top: 16px; }
.skill-example pre {
  background: var(--surface-hover); border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm); padding: 14px; overflow-x: auto;
}
.skill-example pre code { font-size: 12px; background: none; padding: 0; }
</style>
