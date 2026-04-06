<template>
  <div class="panel-root" :style="{ width: panel.width + 'px' }">
    <!-- Top tab bar: shows open tabs for the active section -->
    <div class="panel-tabs" v-if="sectionTabs.length">
      <button
        v-for="tab in sectionTabs" :key="tab.id"
        :class="['ptab', { active: tab.id === panel.activeTabId }]"
        @click="panel.activateTab(tab.id)"
        :title="tab.path || tab.title"
      >
        <span class="ptab-label">{{ tab.title }}</span>
        <span class="ptab-close" @click.stop="panel.closeTab(tab.id)">&times;</span>
      </button>
      <div class="ptab-spacer"></div>
      <button class="ptab ptab-close-panel" @click="panel.hide()" title="Close panel">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
          <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
        </svg>
      </button>
    </div>
    <div v-else class="panel-tabs">
      <div class="ptab-spacer"></div>
      <button class="ptab ptab-close-panel" @click="panel.hide()" title="Close panel">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
          <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
        </svg>
      </button>
    </div>

    <!-- Content area -->
    <div class="panel-body">
      <!-- FILES section -->
      <div v-if="activeSection === 'files'" class="plugin-slot files-browser">
        <div class="files-path">
          <button class="files-up" @click="navigateUp" :disabled="!filePath || filePath === '/'">
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 18 9 12 15 6"/></svg>
          </button>
          <span class="files-crumb">{{ filePath || '~' }}</span>
        </div>
        <div v-if="filesLoading" class="files-loading">Loading...</div>
        <div v-else class="files-list">
          <div v-for="f in fileEntries" :key="f.name"
            class="file-entry" :class="{ dir: f.is_dir }"
            @click="f.is_dir ? navigateTo(f.name) : openFile(f)">
            <svg v-if="f.is_dir" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8">
              <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/>
            </svg>
            <svg v-else viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8">
              <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/>
            </svg>
            <span class="file-name">{{ f.name }}</span>
            <span v-if="!f.is_dir && f.size" class="file-size">{{ formatSize(f.size) }}</span>
          </div>
          <div v-if="!fileEntries.length" class="files-empty">Empty directory</div>
        </div>
      </div>

      <!-- MEDIA section -->
      <div v-else-if="activeSection === 'media'" class="plugin-slot files-browser">
        <div v-if="mediaLoading" class="files-loading">Loading media...</div>
        <div v-else class="media-grid">
          <div v-for="m in mediaFiles" :key="m.path" class="media-thumb" @click="openViewer(m)">
            <img v-if="m.type === 'image'" :src="serveUrl(m.path)" :alt="m.name" loading="lazy"/>
            <div v-else class="media-video-thumb">
              <svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.5"><polygon points="5 3 19 12 5 21 5 3"/></svg>
            </div>
            <span class="media-name">{{ m.name }}</span>
          </div>
          <div v-if="!mediaFiles.length" class="files-empty">No media files found</div>
        </div>
      </div>

      <!-- CODE section: viewer or editor -->
      <div v-else-if="activeSection === 'code'" class="plugin-slot">
        <div v-if="activeCodeTab" class="code-viewer">
          <div v-if="activeCodeTab._loading" class="files-loading">Loading...</div>
          <template v-else-if="activeCodeTab._editing">
            <CodeEditor
              :content="activeCodeTab.content"
              :path="activeCodeTab.path"
              :filename="activeCodeTab.title"
              @saved="onFileSaved"
            />
          </template>
          <template v-else>
            <div class="code-toolbar">
              <span class="code-lang">{{ activeCodeTab._lang || '' }}</span>
              <div class="editor-spacer"></div>
              <button class="editor-btn" @click="activeCodeTab._editing = true" title="Edit file">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/>
                  <path d="M18.5 2.5a2.121 2.121 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/>
                </svg>
                <span>Edit</span>
              </button>
            </div>
            <pre class="plugin-code"><code v-html="activeCodeTab._highlighted || escapeHtml(activeCodeTab.content || '')"></code></pre>
          </template>
        </div>
        <div v-else class="plugin-placeholder">
          <span>No files open</span>
          <span class="plugin-hint">Open a file from the Files tab</span>
        </div>
      </div>

      <!-- CANVAS section: live preview of HTML files from workspace/canvas/ -->
      <div v-else-if="activeSection === 'canvas'" class="plugin-slot">
        <div v-if="activeCanvasTab" class="canvas-viewer">
          <div v-if="activeCanvasTab._loading" class="files-loading">Loading...</div>
          <template v-else>
            <div class="canvas-toolbar">
              <span class="editor-filename">{{ activeCanvasTab.title }}</span>
              <div class="editor-spacer"></div>
              <button class="editor-btn" @click="refreshCanvas" title="Refresh">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 11-2.12-9.36L23 10"/>
                </svg>
                <span>Refresh</span>
              </button>
              <button class="editor-btn" @click="openCanvasInCode" title="Edit source">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>
                </svg>
                <span>Source</span>
              </button>
            </div>
            <iframe
              class="canvas-iframe"
              :src="canvasUrl(activeCanvasTab)"
              sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
              :key="activeCanvasTab._refreshKey"
            ></iframe>
          </template>
        </div>
        <div v-else>
          <!-- Canvas file browser -->
          <div v-if="canvasLoading" class="files-loading">Loading...</div>
          <div v-else-if="canvasFiles.length" class="files-list" style="padding-top: 8px;">
            <div v-for="f in canvasFiles" :key="f.name"
              class="file-entry" @click="openCanvasFile(f)">
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8">
                <rect x="3" y="3" width="18" height="18" rx="2" ry="2"/><line x1="3" y1="9" x2="21" y2="9"/>
              </svg>
              <span class="file-name">{{ f.name }}</span>
              <span v-if="f.size" class="file-size">{{ formatSize(f.size) }}</span>
            </div>
          </div>
          <div v-else class="plugin-placeholder">
            <span>Canvas</span>
            <span class="plugin-hint">HTML files in workspace/code/ appear here</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Footer section bar -->
    <div class="panel-footer">
      <button v-for="s in sections" :key="s.id" class="footer-tab"
        :class="{ active: activeSection === s.id }"
        @click="activeSection = s.id">
        <svg v-html="s.icon" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8"></svg>
        <span>{{ s.label }}</span>
      </button>
    </div>

    <!-- Floating media viewer -->
    <Teleport to="body">
      <Transition name="viewer">
        <div v-if="viewerFile" class="media-viewer" @keydown="onViewerKey" tabindex="0" ref="viewerEl">
          <div class="viewer-bar">
            <span class="viewer-name">{{ viewerFile.name }}</span>
            <button class="viewer-btn" @click="toggleFullscreen" title="Fullscreen (F)">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>
            </button>
            <button class="viewer-btn" @click="viewerFile = null" title="Close (Esc)">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            </button>
          </div>
          <div class="viewer-content">
            <img v-if="viewerFile.type === 'image'" :src="serveUrl(viewerFile.path)"/>
            <video v-else ref="videoEl" controls :src="serveUrl(viewerFile.path)"></video>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, computed, watch, nextTick, defineAsyncComponent } from 'vue'
import { usePanelStore } from '../stores/panel'
import api from '../api/client'
import hljs from 'highlight.js/lib/core'

// Lazy-load CodeMirror editor — only fetched when user clicks Edit
const CodeEditor = defineAsyncComponent(() => import('./CodeEditor.vue'))

// Register common languages
import javascript from 'highlight.js/lib/languages/javascript'
import python from 'highlight.js/lib/languages/python'
import go from 'highlight.js/lib/languages/go'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'
import css from 'highlight.js/lib/languages/css'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'
import sql from 'highlight.js/lib/languages/sql'
import markdown from 'highlight.js/lib/languages/markdown'
import rust from 'highlight.js/lib/languages/rust'
import cpp from 'highlight.js/lib/languages/cpp'
import ruby from 'highlight.js/lib/languages/ruby'
import typescript from 'highlight.js/lib/languages/typescript'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('python', python)
hljs.registerLanguage('go', go)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('json', json)
hljs.registerLanguage('css', css)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('sql', sql)
hljs.registerLanguage('markdown', markdown)
hljs.registerLanguage('rust', rust)
hljs.registerLanguage('cpp', cpp)
hljs.registerLanguage('ruby', ruby)
hljs.registerLanguage('typescript', typescript)

const panel = usePanelStore()

// Section state (footer tabs)
const activeSection = ref('files')

// Footer section definitions
const sections = [
  { id: 'files', label: 'Files', icon: '<path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/>' },
  { id: 'media', label: 'Media', icon: '<polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/>' },
  { id: 'code', label: 'Code', icon: '<polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>' },
  { id: 'canvas', label: 'Canvas', icon: '<rect x="3" y="3" width="18" height="18" rx="2" ry="2"/><line x1="3" y1="9" x2="21" y2="9"/><line x1="9" y1="21" x2="9" y2="9"/>' },
]

// Tabs for current section (code tabs only show on code section)
const sectionTabs = computed(() => {
  if (activeSection.value === 'code') {
    return panel.tabs.filter(t => t.plugin === 'code')
  }
  if (activeSection.value === 'canvas') {
    return panel.tabs.filter(t => t.plugin === 'canvas')
  }
  return []
})

// Active code tab
const activeCodeTab = computed(() => {
  if (activeSection.value !== 'code') return null
  const tab = panel.tabs.find(t => t.id === panel.activeTabId && t.plugin === 'code')
  return tab || panel.tabs.find(t => t.plugin === 'code') || null
})

// Active canvas tab
const activeCanvasTab = computed(() => {
  if (activeSection.value !== 'canvas') return null
  const tab = panel.tabs.find(t => t.id === panel.activeTabId && t.plugin === 'canvas')
  return tab || panel.tabs.find(t => t.plugin === 'canvas') || null
})

// Canvas state
const canvasFiles = ref([])
const canvasLoading = ref(false)

// File browser state
const filePath = ref('')
const fileEntries = ref([])
const filesLoading = ref(false)

// Media state
const mediaFiles = ref([])
const mediaLoading = ref(false)
const mediaExts = { image: ['jpg','jpeg','png','gif','webp','svg','bmp','ico'], video: ['mp4','webm','mov','avi','mkv','m4v','ogv'] }

// Viewer state
const viewerFile = ref(null)
const viewerEl = ref(null)
const videoEl = ref(null)

// Extension → language map for highlight.js
const extLangMap = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', tsx: 'typescript',
  vue: 'xml', html: 'xml', htm: 'xml', svg: 'xml', xml: 'xml',
  py: 'python', pyw: 'python',
  go: 'go',
  sh: 'bash', bash: 'bash', zsh: 'bash',
  json: 'json', jsonc: 'json',
  css: 'css', scss: 'css',
  yaml: 'yaml', yml: 'yaml', toml: 'yaml',
  sql: 'sql',
  md: 'markdown',
  rs: 'rust',
  cpp: 'cpp', cc: 'cpp', cxx: 'cpp', c: 'cpp', h: 'cpp', hpp: 'cpp',
  rb: 'ruby',
}

const codeExts = ['go','js','mjs','ts','tsx','vue','py','json','jsonc','yaml','yml','toml','md','html','htm','css','scss','sh','bash','sql','txt','csv','env','cfg','conf','ini','xml','svg','rs','cpp','cc','c','h','hpp','rb','pl','java','kt','swift','r','lua','zig','Makefile','Dockerfile']

async function loadFiles(path) {
  filesLoading.value = true
  try {
    const params = path ? `?path=${encodeURIComponent(path)}` : ''
    const data = await api.get(`/api/v1/workspace/files${params}`)
    fileEntries.value = data.entries || data || []
    filePath.value = data.path || path || ''
  } catch (e) {
    fileEntries.value = []
  }
  filesLoading.value = false
}

async function scanMedia(path = 'media') {
  mediaLoading.value = true
  mediaFiles.value = []
  try {
    const data = await api.get(`/api/v1/workspace/files?path=${encodeURIComponent(path)}`)
    const entries = data.entries || []
    const allExts = [...mediaExts.image, ...mediaExts.video]
    for (const e of entries) {
      const full = path ? `${path}/${e.name}` : e.name
      if (e.is_dir) {
        try {
          const sub = await api.get(`/api/v1/workspace/files?path=${encodeURIComponent(full)}`)
          for (const s of (sub.entries || [])) {
            const ext = s.name.split('.').pop().toLowerCase()
            if (allExts.includes(ext)) {
              const type = mediaExts.image.includes(ext) ? 'image' : 'video'
              mediaFiles.value.push({ name: s.name, path: `${full}/${s.name}`, type, size: s.size })
            }
          }
        } catch {}
      } else {
        const ext = e.name.split('.').pop().toLowerCase()
        if (allExts.includes(ext)) {
          const type = mediaExts.image.includes(ext) ? 'image' : 'video'
          mediaFiles.value.push({ name: e.name, path: full, type, size: e.size })
        }
      }
    }
  } catch {}
  mediaLoading.value = false
}

function navigateTo(name) {
  const next = filePath.value ? `${filePath.value}/${name}` : name
  loadFiles(next)
}

function navigateUp() {
  if (!filePath.value) return
  const parts = filePath.value.split('/')
  parts.pop()
  loadFiles(parts.join('/'))
}

function openFile(f) {
  const fullPath = filePath.value ? `${filePath.value}/${f.name}` : f.name
  const ext = f.name.split('.').pop().toLowerCase()
  const allMedia = [...mediaExts.image, ...mediaExts.video]
  if (allMedia.includes(ext)) {
    const type = mediaExts.image.includes(ext) ? 'image' : 'video'
    openViewer({ name: f.name, path: fullPath, type })
    return
  }
  // HTML files in code/: open in code AND render in canvas
  if ((ext === 'html' || ext === 'htm') && fullPath.startsWith('code')) {
    openCodeFile(f.name, fullPath, ext)
    openCanvasFile({ name: f.name, path: fullPath })
    return
  }
  if (codeExts.includes(ext) || f.name === 'Makefile' || f.name === 'Dockerfile') {
    openCodeFile(f.name, fullPath, ext)
    return
  }
  // Fallback: try as code
  openCodeFile(f.name, fullPath, ext)
}

async function openCodeFile(name, path, ext) {
  // Switch to code section and open tab
  activeSection.value = 'code'

  // Check if already open
  const existing = panel.tabs.find(t => t.plugin === 'code' && t.path === path)
  if (existing) {
    panel.activateTab(existing.id)
    return
  }

  // Create tab with loading state
  const tabId = panel.pushTab({ plugin: 'code', title: name, path, content: null })
  const tab = panel.tabs.find(t => t.id === tabId)
  if (tab) tab._loading = true

  // Fetch file content
  try {
    const resp = await fetch(serveUrl(path))
    const text = await resp.text()
    if (tab) {
      tab.content = text
      tab._loading = false
      tab._editing = false
      tab._lang = extLangMap[ext] || ''
      // Syntax highlight
      const lang = extLangMap[ext]
      if (lang && hljs.getLanguage(lang)) {
        try {
          tab._highlighted = hljs.highlight(text, { language: lang }).value
        } catch { tab._highlighted = null }
      }
    }
  } catch (e) {
    if (tab) {
      tab.content = `Error loading file: ${e.message}`
      tab._loading = false
    }
  }
}

function onFileSaved(newContent) {
  const tab = activeCodeTab.value
  if (!tab) return
  tab.content = newContent
  // Re-highlight for viewer mode
  if (tab._lang && hljs.getLanguage(tab._lang)) {
    try { tab._highlighted = hljs.highlight(newContent, { language: tab._lang }).value }
    catch { tab._highlighted = null }
  }
}

// ── Canvas functions ──

async function scanCanvas() {
  canvasLoading.value = true
  canvasFiles.value = []
  try {
    const data = await api.get('/api/v1/workspace/files?path=code')
    const entries = data.entries || []
    const htmlExts = ['html', 'htm']
    for (const e of entries) {
      if (e.is_dir) {
        // Directory — check for index.html (webapp)
        try {
          const sub = await api.get(`/api/v1/workspace/files?path=code/${e.name}`)
          const hasIndex = (sub.entries || []).some(s => s.name === 'index.html')
          if (hasIndex) {
            canvasFiles.value.push({ name: e.name, path: `code/${e.name}/index.html`, isApp: true })
          }
        } catch {}
      } else {
        const ext = e.name.split('.').pop().toLowerCase()
        if (htmlExts.includes(ext)) {
          canvasFiles.value.push({ name: e.name, path: `code/${e.name}`, size: e.size })
        }
      }
    }
  } catch {}
  canvasLoading.value = false
}

function openCanvasFile(f) {
  const existing = panel.tabs.find(t => t.plugin === 'canvas' && t.path === f.path)
  if (existing) {
    existing._refreshKey = Date.now()
    panel.activateTab(existing.id)
    return
  }
  panel.pushTab({
    plugin: 'canvas',
    title: f.name,
    path: f.path,
    content: null,
    _refreshKey: Date.now(),
    _loading: false,
  })
}

function canvasUrl(tab) {
  if (!tab || !tab.path) return 'about:blank'
  return `/api/v1/workspace/live/${tab.path.replace(/^code\//, '')}`
}

function refreshCanvas() {
  const tab = activeCanvasTab.value
  if (!tab) return
  tab._refreshKey = Date.now()
}

function openCanvasInCode() {
  const tab = activeCanvasTab.value
  if (!tab || !tab.path) return
  const ext = tab.title.split('.').pop().toLowerCase()
  // For webapps, open the index.html
  openCodeFile(tab.title, tab.path, ext || 'html')
}

function openViewer(m) {
  viewerFile.value = m
  nextTick(() => { viewerEl.value?.focus() })
}

function onViewerKey(e) {
  if (e.key === 'Escape') { viewerFile.value = null; return }
  if (e.key === 'f' || e.key === 'F') { toggleFullscreen(); return }
  if (e.key === ' ' && videoEl.value) {
    e.preventDefault()
    videoEl.value.paused ? videoEl.value.play() : videoEl.value.pause()
  }
}

function toggleFullscreen() {
  if (!viewerEl.value) return
  if (document.fullscreenElement) document.exitFullscreen()
  else viewerEl.value.requestFullscreen()
}

function serveUrl(path) {
  const token = localStorage.getItem('kaiju_token') || ''
  return `/api/v1/workspace/serve?path=${encodeURIComponent(path)}&token=${encodeURIComponent(token)}`
}

function formatSize(bytes) {
  if (!bytes) return ''
  if (bytes < 1024) return `${bytes}B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}K`
  return `${(bytes / (1024 * 1024)).toFixed(1)}M`
}

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// Load content when section changes
watch(activeSection, (section) => {
  if (section === 'files' && !fileEntries.value.length) loadFiles('')
  if (section === 'media' && !mediaFiles.value.length) scanMedia('media')
  if (section === 'canvas' && !canvasFiles.value.length) scanCanvas()
}, { immediate: true })

// When panel opens via pushTab (e.g. from agent), switch to appropriate section
watch(() => panel.activeTab, (tab) => {
  if (!tab) return
  if (tab.plugin === 'code') activeSection.value = 'code'
  else if (tab.plugin === 'canvas') activeSection.value = 'canvas'
})
</script>

<style scoped>
.panel-root {
  display: flex; flex-direction: column;
  background: var(--surface);
  border-left: 1px solid var(--border);
  height: 100%; min-width: 320px;
}

/* ── Tab bar ─────────────────────────────────────────────── */
.panel-tabs {
  display: flex; align-items: center;
  height: 32px; min-height: 32px;
  border-bottom: 1px solid var(--border);
  padding: 0 2px;
  gap: 1px;
  overflow: hidden;
}

.ptab {
  display: flex; align-items: center; gap: 4px;
  padding: 4px 8px;
  font-size: 11px; font-family: var(--mono); font-weight: 500;
  color: var(--text-muted);
  background: none; border: none; border-radius: 3px;
  cursor: pointer; white-space: nowrap;
  transition: all var(--transition);
  max-width: 140px;
}
.ptab:hover { color: var(--text-secondary); background: var(--surface-hover); }
.ptab.active { color: var(--accent); background: var(--accent-subtle); }

.ptab-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ptab-close {
  font-size: 13px; line-height: 1; opacity: 0;
  transition: opacity var(--transition);
  color: var(--text-muted);
}
.ptab:hover .ptab-close { opacity: 0.6; }
.ptab-close:hover { opacity: 1; color: var(--signal-red); }

.ptab-spacer { flex: 1; }

.ptab-close-panel {
  display: flex; align-items: center; justify-content: center;
  padding: 4px; margin-left: 4px; flex-shrink: 0;
}
.ptab-close-panel:hover { color: var(--signal-red); }

/* ── Body ────────────────────────────────────────────────── */
.panel-body {
  flex: 1; overflow: hidden;
  position: relative;
}

.plugin-slot {
  width: 100%; height: 100%;
  display: flex; flex-direction: column;
  overflow: auto;
}

/* Code viewer */
/* Code viewer */
.code-viewer { width: 100%; height: 100%; overflow: auto; display: flex; flex-direction: column; }
.code-toolbar {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 12px; border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0; background: var(--surface);
}
.code-lang {
  font-size: 9px; font-family: var(--mono); color: var(--text-muted);
  text-transform: uppercase; letter-spacing: 0.5px;
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

/* Canvas viewer */
.canvas-viewer { width: 100%; height: 100%; display: flex; flex-direction: column; }
.canvas-toolbar {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 12px; border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0; background: var(--surface);
}
.canvas-iframe {
  flex: 1; width: 100%; border: none;
  background: #fff;
}
.plugin-code {
  margin: 0; padding: 12px 16px;
  font-size: 11px; font-family: var(--mono);
  line-height: 1.6; color: var(--text);
  white-space: pre; word-break: normal;
  overflow: auto; flex: 1;
  background: transparent;
  tab-size: 4;
}
.plugin-code code {
  font-family: inherit;
  background: transparent;
}

.plugin-canvas-wrap {
  width: 100%; height: 100%;
  display: flex; align-items: center; justify-content: center;
  overflow: auto; padding: 16px;
}
.plugin-mermaid {
  padding: 16px;
  font-family: var(--mono); font-size: 12px;
  color: var(--text-secondary);
}

.plugin-placeholder {
  display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  height: 100%; gap: 8px;
  color: var(--text-muted);
  font-size: 13px; font-family: var(--mono);
}
.plugin-hint { font-size: 11px; opacity: 0.6; }

/* File browser */
.files-browser { display: flex; flex-direction: column; height: 100%; }
.files-path {
  display: flex; align-items: center; gap: 6px;
  padding: 8px 12px; border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.files-up {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; border-radius: 4px;
  background: none; border: 1px solid var(--border);
  color: var(--text-muted); cursor: pointer;
  transition: all 0.1s ease;
}
.files-up:hover:not(:disabled) { color: var(--accent); border-color: var(--accent); }
.files-up:disabled { opacity: 0.3; cursor: default; }
.files-crumb {
  font-size: 11px; font-family: var(--mono);
  color: var(--text-muted); overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap;
}
.files-loading { padding: 24px; text-align: center; color: var(--text-muted); font-size: 12px; }
.files-list { flex: 1; overflow-y: auto; padding: 4px 0; }
.file-entry {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 14px; cursor: pointer;
  font-size: 12px; font-family: var(--mono);
  color: var(--text-secondary);
  transition: background 0.08s ease;
}
.file-entry:hover { background: var(--surface-hover); }
.file-entry.dir { color: var(--accent); }
.file-entry.dir:hover { color: var(--accent); }
.file-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-size { font-size: 10px; color: var(--text-muted); flex-shrink: 0; }
.files-empty { padding: 24px; text-align: center; color: var(--text-muted); font-size: 12px; font-style: italic; }

/* Media grid */
.media-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(100px, 1fr));
  gap: 8px; padding: 8px; overflow-y: auto; flex: 1;
}
.media-thumb {
  display: flex; flex-direction: column; align-items: center; gap: 4px;
  cursor: pointer; padding: 6px; border-radius: 6px;
  transition: background 0.1s ease;
}
.media-thumb:hover { background: var(--surface-hover); }
.media-thumb img {
  width: 100%; aspect-ratio: 1; object-fit: cover; border-radius: 4px;
  background: var(--bg-soft);
}
.media-video-thumb {
  width: 100%; aspect-ratio: 1; display: flex; align-items: center; justify-content: center;
  background: var(--bg-soft); border-radius: 4px; color: var(--text-muted);
}
.media-name {
  font-size: 9px; font-family: var(--mono); color: var(--text-muted);
  text-align: center; overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; width: 100%;
}

/* Footer section bar */
.panel-footer {
  display: flex; border-top: 1px solid var(--border);
  flex-shrink: 0; background: var(--surface);
}
.footer-tab {
  flex: 1; display: flex; align-items: center; justify-content: center; gap: 5px;
  padding: 8px 0; border: none; background: none;
  font-size: 10px; font-family: var(--mono); color: var(--text-muted);
  cursor: pointer; transition: all 0.1s ease;
}
.footer-tab:hover { color: var(--text); background: var(--bg-soft); }
.footer-tab.active { color: var(--accent); }
.footer-tab svg { flex-shrink: 0; }

/* Floating media viewer */
.media-viewer {
  position: fixed; inset: 0; z-index: 9999;
  background: rgba(0,0,0,0.92); display: flex; flex-direction: column;
  outline: none;
}
.viewer-bar {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 16px; flex-shrink: 0;
}
.viewer-name {
  flex: 1; font-size: 12px; font-family: var(--mono); color: #aaa;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.viewer-btn {
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px; border-radius: 6px;
  background: rgba(255,255,255,0.1); border: none;
  color: #ccc; cursor: pointer; transition: all 0.1s ease;
}
.viewer-btn:hover { background: rgba(255,255,255,0.2); color: #fff; }
.viewer-content {
  flex: 1; display: flex; align-items: center; justify-content: center;
  overflow: hidden; padding: 16px;
}
.viewer-content img { max-width: 100%; max-height: 100%; object-fit: contain; border-radius: 4px; }
.viewer-content video { max-width: 100%; max-height: 100%; border-radius: 4px; }

/* Viewer transition */
.viewer-enter-active, .viewer-leave-active { transition: opacity 0.15s ease; }
.viewer-enter-from, .viewer-leave-to { opacity: 0; }
</style>
