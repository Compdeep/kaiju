<template>
  <button class="up-btn" @click="trigger" :disabled="disabled" title="Attach file">
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
      <line x1="12" y1="5" x2="12" y2="19"/>
      <line x1="5" y1="12" x2="19" y2="12"/>
    </svg>
    <input
      ref="fileInput"
      type="file"
      multiple
      class="up-hidden"
      :accept="accept"
      @change="onChange"
    />
  </button>
</template>

<script setup>
/**
 * desc: + icon button that opens a multi-select file picker. Each chosen
 * file is forwarded one-by-one to the parent via the 'files' event so the
 * caller can drive the upload pipeline however it wants.
 */
import { ref } from 'vue'

defineProps({
  disabled: { type: Boolean, default: false },
  accept: { type: String, default: '.txt,.md,.log,.csv,.tsv,.json,.jsonl,.yaml,.yml,.xml,.html,.htm,.py,.js,.ts,.jsx,.tsx,.go,.rs,.java,.c,.cpp,.h,.hpp,.sh,.rb,.php,.sql,.pdf,.png,.jpg,.jpeg,.gif,.webp' },
})
const emit = defineEmits(['files'])
const fileInput = ref(null)

function trigger() {
  fileInput.value?.click()
}

function onChange(e) {
  const files = Array.from(e.target.files || [])
  if (files.length) emit('files', files)
  // Reset so picking the same file twice in a row still fires.
  e.target.value = ''
}
</script>

<style scoped>
.up-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 28px; height: 28px;
  background: none;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  color: var(--text-muted);
}
.up-btn:hover:not(:disabled) {
  background: var(--surface);
  color: var(--text);
}
.up-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.up-hidden { display: none; }
</style>
