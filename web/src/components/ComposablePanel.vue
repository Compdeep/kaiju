<template>
  <div class="panel-root" :style="{ width: panel.width + 'px' }">
    <!-- Tab bar: recent tabs + overflow dropdown + close -->
    <div class="panel-tabs">
      <button
        v-for="tab in panel.visibleTabs" :key="tab.id"
        :class="['ptab', { active: tab.id === panel.activeTabId }]"
        @click="panel.activateTab(tab.id)"
        :title="tab.path || tab.title"
      >
        <span class="ptab-label">{{ tab.title }}</span>
        <span class="ptab-close" @click.stop="panel.closeTab(tab.id)">&times;</span>
      </button>

      <!-- Overflow dropdown -->
      <div v-if="panel.overflowTabs.length" class="ptab-overflow" ref="overflowRef">
        <button class="ptab ptab-more" @click="showOverflow = !showOverflow">
          {{ panel.overflowTabs.length }} more
          <svg viewBox="0 0 24 24" width="10" height="10" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="6 9 12 15 18 9"/></svg>
        </button>
        <div v-if="showOverflow" class="overflow-menu">
          <button
            v-for="tab in panel.overflowTabs" :key="tab.id"
            class="overflow-item"
            @click="panel.activateTab(tab.id); showOverflow = false"
          >
            <span class="overflow-label">{{ tab.title }}</span>
            <span class="overflow-plugin">{{ tab.plugin }}</span>
          </button>
        </div>
      </div>

      <div class="ptab-spacer"></div>
      <button class="ptab ptab-close-panel" @click="panel.hide()" title="Close panel">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
          <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
        </svg>
      </button>
    </div>

    <!-- Content area — renders based on active tab's plugin type -->
    <div class="panel-body" v-if="panel.activeTab">
      <!-- Preview: sandboxed iframe -->
      <div v-if="panel.activeTab.plugin === 'preview'" class="plugin-slot">
        <iframe
          v-if="panel.activeTab.content"
          class="plugin-iframe"
          :srcdoc="panel.activeTab.content"
          sandbox="allow-scripts allow-same-origin"
        ></iframe>
        <iframe
          v-else-if="panel.activeTab.path"
          class="plugin-iframe"
          :src="serveUrl(panel.activeTab.path)"
          sandbox="allow-scripts allow-same-origin"
        ></iframe>
        <div v-else class="plugin-empty">No content</div>
      </div>

      <!-- Code: syntax-highlighted view -->
      <div v-else-if="panel.activeTab.plugin === 'code'" class="plugin-slot">
        <pre class="plugin-code"><code>{{ panel.activeTab.content || '(loading...)' }}</code></pre>
      </div>

      <!-- Canvas -->
      <div v-else-if="panel.activeTab.plugin === 'canvas'" class="plugin-slot">
        <div v-if="panel.activeTab.mime === 'text/x-mermaid'" class="plugin-mermaid">
          <pre><code>{{ panel.activeTab.content }}</code></pre>
        </div>
        <div v-else-if="panel.activeTab.content" class="plugin-canvas-wrap" v-html="panel.activeTab.content"></div>
        <canvas v-else ref="canvasEl" class="plugin-canvas-el"></canvas>
      </div>

      <!-- Graph -->
      <div v-else-if="panel.activeTab.plugin === 'graph'" class="plugin-slot">
        <pre class="plugin-code"><code>{{ panel.activeTab.content || '(no data)' }}</code></pre>
      </div>

      <!-- Files -->
      <div v-else-if="panel.activeTab.plugin === 'files'" class="plugin-slot files-browser">
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

      <!-- Media browser -->
      <div v-else-if="panel.activeTab.plugin === 'media'" class="plugin-slot files-browser">
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

      <!-- Fallback -->
      <div v-else class="plugin-slot">
        <div class="plugin-placeholder"><span>Unknown plugin: {{ panel.activeTab.plugin }}</span></div>
      </div>
    </div>

    <!-- No tabs state -->
    <div v-else class="panel-body">
      <div class="plugin-slot">
        <div class="plugin-placeholder">
          <span>No content open</span>
          <span class="plugin-hint">Agent output will appear here</span>
        </div>
      </div>
    </div>

    <!-- Footer tab bar -->
    <div class="panel-footer">
      <button v-for="p in footerTabs" :key="p.id" class="footer-tab"
        :class="{ active: panel.activeTab?.plugin === p.id }"
        @click="switchFooterTab(p.id)">
        <svg v-html="p.icon" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8"></svg>
        <span>{{ p.label }}</span>
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
/**
 * desc: Composable side panel that renders plugin-based tabs (preview, code, canvas, graph, files)
 */
import { ref, watch, nextTick } from 'vue'
import { usePanelStore } from '../stores/panel'
import api from '../api/client'

const panel = usePanelStore()
const canvasEl = ref(null)
const overflowRef = ref(null)
const showOverflow = ref(false)

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

// Footer tabs
const footerTabs = [
  { id: 'files', label: 'Files', icon: '<path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/>' },
  { id: 'media', label: 'Media', icon: '<polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/>' },
]

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

async function scanMedia(path = '') {
  mediaLoading.value = true
  mediaFiles.value = []
  try {
    const data = await api.get(`/api/v1/workspace/files?path=${encodeURIComponent(path)}`)
    const entries = data.entries || []
    const allExts = [...mediaExts.image, ...mediaExts.video]
    for (const e of entries) {
      const full = path ? `${path}/${e.name}` : e.name
      if (e.is_dir) {
        // Scan one level deep
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
  const codeExts = ['go','js','ts','vue','py','json','yaml','yml','toml','md','html','css','sh','sql','txt','csv','env','cfg','conf','ini','xml']
  const plugin = codeExts.includes(ext) ? 'code' : 'preview'
  panel.pushTab({ plugin, title: f.name, path: fullPath })
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

function switchFooterTab(pluginId) {
  const existing = panel.tabs.find(t => t.plugin === pluginId)
  if (existing) { panel.activateTab(existing.id) }
  else { panel.pushTab({ plugin: pluginId, title: pluginId.charAt(0).toUpperCase() + pluginId.slice(1) }) }
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

// Load content when tab becomes active
watch(() => panel.activeTab, (tab) => {
  if (tab?.plugin === 'files' && !fileEntries.value.length) loadFiles('')
  if (tab?.plugin === 'media' && !mediaFiles.value.length) scanMedia('')
}, { immediate: true })
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

.ptab-label {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
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

/* ── Overflow dropdown ───────────────────────────────────── */
.ptab-overflow { position: relative; }
.ptab-more { font-size: 10px; color: var(--text-muted); gap: 3px; }

.overflow-menu {
  position: absolute; top: 100%; left: 0; z-index: 20;
  min-width: 200px; max-height: 300px; overflow-y: auto;
  background: var(--surface-raised); border: 1px solid var(--border);
  border-radius: var(--radius-sm); box-shadow: var(--shadow-md);
  padding: 4px;
}
.overflow-item {
  display: flex; align-items: center; justify-content: space-between;
  width: 100%; padding: 6px 8px;
  font-size: 11px; font-family: var(--mono);
  color: var(--text-secondary);
  background: none; border: none; border-radius: 3px;
  cursor: pointer; text-align: left;
  transition: all var(--transition);
}
.overflow-item:hover { background: var(--surface-hover); color: var(--text); }
.overflow-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
.overflow-plugin { font-size: 9px; color: var(--text-muted); text-transform: uppercase; margin-left: 8px; flex-shrink: 0; }

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

.plugin-iframe {
  width: 100%; height: 100%;
  border: none; display: block;
  background: #fff;
}

.plugin-code {
  margin: 0; padding: 12px 16px;
  font-size: 12px; font-family: var(--mono);
  line-height: 1.6; color: var(--text);
  white-space: pre-wrap; word-break: break-word;
  overflow: auto; flex: 1;
}

.plugin-canvas-el {
  width: 100%; height: 100%; display: block;
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
.plugin-empty { display: flex; align-items: center; justify-content: center; height: 100%; color: var(--text-muted); font-size: 12px; }

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

/* Footer tab bar */
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
