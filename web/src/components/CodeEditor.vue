<template>
  <div class="editor-wrap">
    <div class="editor-toolbar">
      <span class="editor-filename">{{ filename }}</span>
      <span v-if="dirty" class="editor-dirty">modified</span>
      <div class="editor-spacer"></div>
      <button class="editor-btn" @click="save" :disabled="saving || !dirty" title="Save (Ctrl+S)">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M19 21H5a2 2 0 01-2-2V5a2 2 0 012-2h11l5 5v11a2 2 0 01-2 2z"/>
          <polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/>
        </svg>
        <span>{{ saving ? 'Saving...' : 'Save' }}</span>
      </button>
    </div>
    <div ref="editorEl" class="editor-container"></div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { keymap } from '@codemirror/view'
import { oneDark } from '@codemirror/theme-one-dark'
import { autocompletion } from '@codemirror/autocomplete'
import api from '../api/client'

// Language imports
import { python } from '@codemirror/lang-python'
import { javascript } from '@codemirror/lang-javascript'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { json } from '@codemirror/lang-json'
import { markdown } from '@codemirror/lang-markdown'
import { rust } from '@codemirror/lang-rust'
import { cpp } from '@codemirror/lang-cpp'
import { java } from '@codemirror/lang-java'
import { sql } from '@codemirror/lang-sql'
import { yaml } from '@codemirror/lang-yaml'

const props = defineProps({
  content: { type: String, default: '' },
  path: { type: String, default: '' },
  filename: { type: String, default: '' },
})

const emit = defineEmits(['saved'])

const editorEl = ref(null)
const dirty = ref(false)
const saving = ref(false)
let view = null
let originalContent = ''

const extLangMap = {
  py: python, pyw: python,
  js: javascript, mjs: javascript, cjs: javascript, ts: javascript, tsx: javascript, jsx: javascript, vue: javascript,
  html: html, htm: html, svg: html, xml: html,
  css: css, scss: css,
  json: json, jsonc: json,
  md: markdown,
  rs: rust,
  cpp: cpp, cc: cpp, cxx: cpp, c: cpp, h: cpp, hpp: cpp,
  java: java, kt: java,
  sql: sql,
  yaml: yaml, yml: yaml, toml: yaml,
}

function getLang(filename) {
  const ext = filename.split('.').pop().toLowerCase()
  const langFn = extLangMap[ext]
  return langFn ? langFn() : []
}

async function save() {
  if (!props.path || !view || saving.value) return
  saving.value = true
  try {
    const content = view.state.doc.toString()
    const token = localStorage.getItem('kaiju_token') || ''
    await fetch('/api/v1/workspace/write', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`,
      },
      body: JSON.stringify({ path: props.path, content }),
    })
    originalContent = content
    dirty.value = false
    emit('saved', content)
  } catch (e) {
    console.error('Save failed:', e)
  }
  saving.value = false
}

onMounted(() => {
  if (!editorEl.value) return
  originalContent = props.content || ''

  const saveKeymap = keymap.of([{
    key: 'Mod-s',
    run: () => { save(); return true },
  }])

  const updateListener = EditorView.updateListener.of((update) => {
    if (update.docChanged) {
      dirty.value = update.state.doc.toString() !== originalContent
    }
  })

  const state = EditorState.create({
    doc: originalContent,
    extensions: [
      basicSetup,
      oneDark,
      autocompletion(),
      getLang(props.filename),
      saveKeymap,
      updateListener,
      EditorView.theme({
        '&': { height: '100%', fontSize: '13px' },
        '.cm-scroller': { overflow: 'auto', fontFamily: 'var(--mono)' },
        '.cm-gutters': { borderRight: '1px solid var(--border-subtle)' },
      }),
    ],
  })

  view = new EditorView({ state, parent: editorEl.value })
})

onBeforeUnmount(() => {
  if (view) { view.destroy(); view = null }
})

// Update content when props change
watch(() => props.content, (newContent) => {
  if (!view || !newContent) return
  const current = view.state.doc.toString()
  if (current !== newContent) {
    view.dispatch({
      changes: { from: 0, to: current.length, insert: newContent },
    })
    originalContent = newContent
    dirty.value = false
  }
})
</script>

<style scoped>
.editor-wrap {
  display: flex; flex-direction: column;
  height: 100%; width: 100%;
}

.editor-toolbar {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
  background: var(--surface);
}

.editor-filename {
  font-size: 11px; font-family: var(--mono);
  color: var(--text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.editor-dirty {
  font-size: 9px; font-family: var(--mono);
  color: var(--signal-amber);
  background: rgba(245, 158, 11, 0.1);
  padding: 1px 6px; border-radius: 3px;
}

.editor-spacer { flex: 1; }

.editor-btn {
  display: flex; align-items: center; gap: 5px;
  padding: 4px 10px; border-radius: 4px;
  font-size: 11px; font-family: var(--mono);
  color: var(--text-muted); background: none;
  border: 1px solid var(--border);
  cursor: pointer; transition: all 0.1s ease;
}
.editor-btn:hover:not(:disabled) {
  color: var(--accent); border-color: var(--accent);
  background: var(--accent-subtle);
}
.editor-btn:disabled { opacity: 0.4; cursor: default; }

.editor-container {
  flex: 1; overflow: hidden;
}
.editor-container :deep(.cm-editor) {
  height: 100%;
}
</style>
