<template>
  <span class="up-chip" :class="{ pending: att.pending, error: att.error }" :title="title">
    <svg class="up-icon" viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
      <polyline points="14 2 14 8 20 8"/>
    </svg>
    <span class="up-name">{{ att.filename }}</span>
    <span class="up-size">{{ sizeText }}</span>
    <span v-if="att.pending" class="up-status">uploading…</span>
    <span v-else-if="att.error" class="up-status">{{ att.error }}</span>
    <button v-else class="up-x" @click.stop="$emit('remove')" title="Remove">×</button>
  </span>
</template>

<script setup>
/**
 * desc: One attached upload rendered as a compact chip — filename, size,
 * status indicator, and remove button. Emits 'remove' when the × is clicked.
 */
import { computed } from 'vue'

const props = defineProps({ att: { type: Object, required: true } })
defineEmits(['remove'])

const sizeText = computed(() => {
  const n = props.att.size || 0
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / 1024 / 1024).toFixed(1)}MB`
})

const title = computed(() => {
  const a = props.att
  const parts = [a.filename, a.type, sizeText.value]
  if (a.lines) parts.push(`${a.lines} lines`)
  if (a.summary_path) parts.push('summarised')
  return parts.join(' · ')
})
</script>

<style scoped>
.up-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 3px 8px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 12px;
  color: var(--text);
  max-width: 240px;
  overflow: hidden;
}
.up-chip.pending { opacity: 0.7; }
.up-chip.error { border-color: var(--signal-red); color: var(--signal-red); }
.up-icon { flex: 0 0 auto; opacity: 0.7; }
.up-name {
  font-family: var(--mono);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 130px;
}
.up-size, .up-status {
  font-size: 11px;
  color: var(--text-muted);
  flex: 0 0 auto;
}
.up-x {
  background: none;
  border: none;
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  color: var(--text-muted);
  padding: 0 2px;
}
.up-x:hover { color: var(--text); }
</style>
