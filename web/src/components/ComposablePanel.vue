<template>
  <div class="panel-root" :style="{ width: panel.width + 'px' }">
    <!-- Column header: hosts the movable utility cluster (passed in from ChatPage
         via the #header-actions slot) while the panel is open. No title — the
         columns read as one open surface. -->
    <div class="col-header panel-header">
      <slot name="header-actions" />
    </div>

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
          <button class="files-up" @click="navigateFileHistoryBack" :disabled="!fileHistory.length" title="Back">
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 18 9 12 15 6"/></svg>
          </button>
          <button class="files-up" @click="navigateUp" :disabled="!filePath" title="Up one folder">
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="18 15 12 9 6 15"/></svg>
          </button>
          <input class="files-crumb files-path-input" v-model="pathInput" placeholder="~"
            title="The workspace folder to show; Enter to go" spellcheck="false"
            @keydown.enter="goToPath(pathInput)" @blur="pathInput = filePath" />
        </div>
        <div v-if="filesLoading" class="files-loading">Loading...</div>
        <div v-else class="files-list">
          <div v-for="f in fileEntries" :key="f.name"
            class="file-entry" :class="{ dir: f.is_dir }"
            @click="f.is_dir ? goToPath(childFilePath(f.name)) : openFile(f)">
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
          <div v-for="(row, ri) in mediaRows" :key="ri" class="media-row">
            <div v-for="m in row" :key="m.path" class="media-thumb" :style="{ flex: m.ratio || 1.5 }" @click="openViewer(m)">
              <img v-if="m.type === 'image'" :src="serveUrl(m.path)" :alt="m.name" loading="lazy" @load="onThumbLoad"/>
              <div v-else class="media-video-thumb">
                <video :src="serveUrl(m.path)" preload="metadata" muted @loadeddata="onVideoMeta($event, m)" class="video-preview"></video>
                <div class="video-play-overlay">
                  <svg viewBox="0 0 24 24" width="20" height="20" fill="white" stroke="none"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                </div>
              </div>
              <span class="media-name">{{ m.name }}</span>
            </div>
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
              <button class="editor-btn" @click="backToCodeFileDirectory" title="Back to containing folder">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="15 18 9 12 15 6"/>
                </svg>
                <span>Back</span>
              </button>
              <span class="code-lang">{{ activeCodeTab._lang || '' }}</span>
              <div class="editor-spacer"></div>
              <a v-if="activeCodeTab.path" class="editor-btn" :href="serveUrl(activeCodeTab.path)" :download="fileName(activeCodeTab.path)" title="Download file">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
                <span>Download</span>
              </a>
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
              <span class="canvas-title">
                <span class="editor-filename">{{ activeCanvasTab.title }}</span>
                <span class="canvas-kind">{{ canvasKind(activeCanvasTab) }}</span>
                <span v-if="canvasMeta(activeCanvasTab)" class="canvas-meta">{{ canvasMeta(activeCanvasTab) }}</span>
              </span>
              <div class="editor-spacer"></div>
              <button class="editor-btn" @click="refreshCanvas" title="Refresh">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 11-2.12-9.36L23 10"/>
                </svg>
                <span>Refresh</span>
              </button>
              <a v-if="activeCanvasTab.path" class="editor-btn" :href="serveUrl(activeCanvasTab.path)" :download="fileName(activeCanvasTab.path)" title="Download file">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
                <span>Download</span>
              </a>
              <button v-if="canvasHasSource(activeCanvasTab)" class="editor-btn" @click="openCanvasInCode" title="Edit source">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>
                </svg>
                <span>Source</span>
              </button>
            </div>
            <iframe
              v-if="canvasKind(activeCanvasTab) === 'url' || (canvasKind(activeCanvasTab) === 'file' && isLivePath(activeCanvasTab.path))"
              class="canvas-iframe"
              :src="canvasSrc(activeCanvasTab)"
              sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
              :key="`${canvasKind(activeCanvasTab)}:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <!-- A page outside project/ is served through the token URL, so it
                 runs without kaiju's origin: it cannot read the token. -->
            <iframe
              v-else-if="canvasKind(activeCanvasTab) === 'file'"
              class="canvas-iframe"
              :src="serveUrl(activeCanvasTab.path)"
              sandbox="allow-scripts allow-forms allow-popups"
              :key="`file:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <iframe
              v-else-if="canvasKind(activeCanvasTab) === 'svgfile'"
              class="canvas-iframe"
              :src="serveUrl(activeCanvasTab.path)"
              sandbox=""
              :key="`svgfile:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <!-- No sandbox: a sandboxed frame refuses the browser's own PDF viewer. -->
            <iframe
              v-else-if="canvasKind(activeCanvasTab) === 'pdf'"
              class="canvas-iframe"
              :src="serveUrl(activeCanvasTab.path)"
              :key="`pdf:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <div v-else-if="canvasKind(activeCanvasTab) === 'image'" class="canvas-image">
              <img :src="serveUrl(activeCanvasTab.path)" :alt="activeCanvasTab.title" :key="`image:${activeCanvasTab._refreshKey}`" />
            </div>
            <div v-else-if="canvasKind(activeCanvasTab) === 'markdown'" class="canvas-markdown" v-html="markdownDoc(activeCanvasTab)"></div>
            <div v-else-if="canvasKind(activeCanvasTab) === 'binary'" class="canvas-binary">
              <div class="canvas-binary-name">{{ activeCanvasTab.title }}</div>
              <div class="canvas-binary-meta">{{ activeCanvasTab.mime || 'binary file' }}<span v-if="activeCanvasTab.size"> · {{ formatSize(activeCanvasTab.size) }}</span></div>
              <div class="canvas-binary-meta">{{ activeCanvasTab.path }}</div>
              <a class="editor-btn" :href="serveUrl(activeCanvasTab.path)" :download="fileName(activeCanvasTab.path)">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
                <span>Download</span>
              </a>
            </div>
            <iframe
              v-else-if="canvasKind(activeCanvasTab) === 'html'"
              class="canvas-iframe"
              :srcdoc="canvasSrcdoc(activeCanvasTab)"
              sandbox="allow-scripts allow-forms allow-popups"
              :key="`${canvasKind(activeCanvasTab)}:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <iframe
              v-else-if="canvasKind(activeCanvasTab) === 'svg'"
              class="canvas-iframe"
              :srcdoc="canvasSrcdoc(activeCanvasTab)"
              sandbox=""
              :key="`${canvasKind(activeCanvasTab)}:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <iframe
              v-else-if="canvasKind(activeCanvasTab) === 'mermaid'"
              class="canvas-iframe"
              :srcdoc="canvasSrcdoc(activeCanvasTab)"
              sandbox="allow-scripts"
              :key="`${canvasKind(activeCanvasTab)}:${activeCanvasTab._refreshKey}`"
            ></iframe>
            <div v-else class="canvas-fallback" v-html="textDoc(activeCanvasTab)"></div>
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
import { renderMarkdown } from '../markdown'
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
const fileHistory = ref([])
const pathInput = ref('')
watch(filePath, p => { pathInput.value = p })

// Media state
const mediaFiles = ref([])
const mediaLoading = ref(false)
const mediaExts = { image: ['jpg','jpeg','png','gif','webp','svg','bmp','ico'], video: ['mp4','webm','mov','avi','mkv','m4v','ogv'] }

/**
 * Pack media files into rows based on aspect ratios. Each row's items
 * fill the full width with proportional flex. Target: ~2.5 total ratio
 * per row (gives 2-3 items per row depending on aspect ratios).
 */
const mediaRows = computed(() => {
  const items = mediaFiles.value
  if (!items.length) return []
  const targetRowRatio = 3.0 // sum of aspect ratios per row
  const rows = []
  let row = []
  let rowRatio = 0
  for (const m of items) {
    const r = m.ratio || 1.5 // default 16:10-ish for unloaded videos
    row.push(m)
    rowRatio += r
    if (rowRatio >= targetRowRatio) {
      rows.push(row)
      row = []
      rowRatio = 0
    }
  }
  if (row.length) rows.push(row) // last partial row
  return rows
})

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

function normalizeFilePath(path) {
  return String(path || '').replace(/^\/+|\/+$/g, '')
}

function currentFilePath() {
  return normalizeFilePath(filePath.value)
}

function childFilePath(name) {
  const base = currentFilePath()
  const child = normalizeFilePath(name)
  return base ? `${base}/${child}` : child
}

// Every move through the folders goes through here, so Back can retrace it.
// A folder that does not load leaves the list where it was.
async function goToPath(path) {
  const from = currentFilePath()
  const target = normalizeFilePath(path)
  if (target === from) return
  if (await loadFiles(target)) fileHistory.value.push(from)
}

async function navigateFileHistoryBack() {
  if (!fileHistory.value.length) return
  await loadFiles(fileHistory.value.pop() || '')
}

function navigateUp() {
  goToPath(directoryOf(currentFilePath()))
}

function directoryOf(path) {
  const normalized = normalizeFilePath(path)
  if (!normalized.includes('/')) return ''
  return normalized.split('/').slice(0, -1).join('/')
}

function fileName(path) {
  return normalizeFilePath(path).split('/').pop() || 'download'
}

async function backToCodeFileDirectory() {
  const tab = activeCodeTab.value
  activeSection.value = 'files'
  await goToPath(directoryOf(tab?.path || ''))
}

async function loadFiles(path) {
  filesLoading.value = true
  let ok = false
  try {
    const params = path ? `?path=${encodeURIComponent(path)}` : ''
    const data = await api.get(`/api/v1/workspace/files${params}`)
    fileEntries.value = data.entries || data || []
    filePath.value = normalizeFilePath(data.path || path || '')
    ok = true
  } catch (e) {
    pathInput.value = filePath.value
  }
  filesLoading.value = false
  return ok
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

// Files the canvas draws, by extension. Each has a renderer below and none
// needs a library: the browser draws pages, SVG, PDF and images itself, and
// markdown goes through the renderer the chat already uses.
const canvasExtKinds = {
  html: 'file', htm: 'file',
  svg: 'svgfile',
  pdf: 'pdf',
  md: 'markdown', markdown: 'markdown',
  png: 'image', jpg: 'image', jpeg: 'image', gif: 'image', webp: 'image', bmp: 'image', ico: 'image', avif: 'image',
}
// The kinds whose source is text, so the canvas offers to edit it.
const editableCanvasKinds = new Set(['file', 'svgfile', 'markdown'])
// Known binary formats, shown as a file card without reading them first.
const binaryExts = new Set(['pptx', 'ppt', 'docx', 'doc', 'xlsx', 'xls', 'odt', 'ods', 'odp', 'key', 'pages', 'numbers',
  'zip', 'gz', 'tgz', 'bz2', 'xz', '7z', 'rar', 'tar', 'jar', 'whl', 'deb', 'rpm', 'dmg', 'iso',
  'exe', 'dll', 'so', 'dylib', 'o', 'a', 'bin', 'wasm', 'pyc', 'class', 'db', 'sqlite', 'bsp',
  'mp3', 'wav', 'flac', 'ogg', 'm4a', 'ttf', 'otf', 'woff', 'woff2', 'psd'])

function extOf(name) {
  const base = fileName(name)
  return base.includes('.') ? base.split('.').pop().toLowerCase() : ''
}

// The canvas tab already showing this path, when it is the one the canvas
// shows now: the newest canvas tab.
function shownInCanvas(path) {
  const tab = panel.tabs.find(t => t.plugin === 'canvas')
  return tab && normalizeFilePath(tab.path) === normalizeFilePath(path) ? tab : null
}

async function openFile(f) {
  const fullPath = childFilePath(f.name)
  const ext = extOf(f.name)
  if (mediaExts.video.includes(ext)) {
    openViewer({ name: f.name, path: fullPath, type: 'video' })
    return
  }
  const kind = canvasExtKinds[ext]
  if (kind) {
    // A second click on a file the canvas is showing opens its source to edit.
    if (shownInCanvas(fullPath) && editableCanvasKinds.has(kind)) {
      openCodeFile(f.name, fullPath, ext, { edit: true })
    } else {
      openCanvasFile({ name: f.name, path: fullPath, size: f.size })
    }
    return
  }
  if (codeExts.includes(ext) || f.name === 'Makefile' || f.name === 'Dockerfile') {
    openCodeFile(f.name, fullPath, ext)
    return
  }
  if (binaryExts.has(ext) || await looksBinary(fullPath)) {
    openCanvasFile({ name: f.name, path: fullPath, size: f.size, binary: true })
    return
  }
  openCodeFile(f.name, fullPath, ext)
}

// Reads the first 8 KB and calls the file binary when it holds a NUL byte,
// which text in any common encoding but UTF-16 never does.
async function looksBinary(path) {
  try {
    const resp = await fetch(serveUrl(path), { headers: { Range: 'bytes=0-8191' } })
    const bytes = new Uint8Array(await resp.arrayBuffer())
    return bytes.subarray(0, 8192).includes(0)
  } catch {
    return false
  }
}

async function openCodeFile(name, path, ext, { edit = false } = {}) {
  // Switch to code section and open tab
  activeSection.value = 'code'

  // Check if already open
  const existing = panel.tabs.find(t => t.plugin === 'code' && t.path === path)
  if (existing) {
    if (edit && !existing._loading) existing._editing = true
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
      tab._editing = edit
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

const CANVAS_RENDERER_KINDS = new Set(['file', 'html', 'url', 'svg', 'svgfile', 'pdf', 'image', 'markdown', 'binary', 'mermaid', 'text'])

function openCanvasFile(f) {
  activeSection.value = 'canvas'
  const existing = panel.tabs.find(t => t.plugin === 'canvas' && t.path === f.path)
  if (existing) {
    existing._refreshKey = Date.now()
    panel.activateTab(existing.id)
    if (canvasKind(existing) === 'markdown') loadCanvasText(existing)
    return
  }
  const id = panel.pushTab({
    plugin: 'canvas',
    type: f.binary ? 'binary' : null,
    title: f.name,
    path: f.path,
    content: null,
  })
  const tab = panel.tabs.find(t => t.id === id)
  if (!tab) return
  tab.size = f.size || 0
  tab._refreshKey = Date.now()
  if (canvasKind(tab) === 'markdown') loadCanvasText(tab)
}

// A markdown file is drawn from its text, so the canvas reads it first.
async function loadCanvasText(tab) {
  tab._loading = true
  try {
    const resp = await fetch(serveUrl(tab.path))
    tab.content = resp.ok ? await resp.text() : `Could not read ${tab.path}: HTTP ${resp.status}`
  } catch (e) {
    tab.content = `Could not read ${tab.path}: ${e.message}`
  }
  tab._loading = false
}

// A tab the agent opened by path arrives without its text.
watch(activeCanvasTab, tab => {
  if (tab?.path && tab.content == null && !tab._loading && canvasKind(tab) === 'markdown') loadCanvasText(tab)
})

function markdownDoc(tab) {
  return renderMarkdown(tabContent(tab))
}

// Files under project/ are served by the live route, where a page's own
// relative scripts and styles resolve. code/ is that route's older name.
function isLivePath(path) {
  return /^(project|code)\//.test(normalizeFilePath(path))
}

function normalizeCanvasKind(value) {
  const kind = String(value || '').trim().toLowerCase()
  if (CANVAS_RENDERER_KINDS.has(kind)) return kind
  if (kind === 'iframe' || kind === 'link' || kind === 'website') return 'url'
  if (kind === 'plain' || kind === 'plaintext' || kind === 'markdown' || kind === 'md') return 'text'
  return null
}

function kindFromMime(mime) {
  const value = String(mime || '').split(';')[0].trim().toLowerCase()
  if (!value) return null
  if (value === 'text/html' || value === 'application/xhtml+xml') return 'html'
  if (value === 'image/svg+xml' || value === 'text/svg' || value === 'application/svg+xml') return 'svg'
  if (value.includes('mermaid')) return 'mermaid'
  if (value === 'text/uri-list') return 'url'
  if (value.startsWith('text/') || value === 'application/json' || value.endsWith('+json')) return 'text'
  return null
}

function tabContent(tab) {
  if (!tab || tab.content == null) return ''
  if (typeof tab.content === 'string') return tab.content
  try {
    return JSON.stringify(tab.content, null, 2)
  } catch {
    return String(tab.content)
  }
}

function isSafeIframeUrl(value) {
  const url = String(value || '').trim()
  return url === 'about:blank' || /^https?:\/\//i.test(url) || /^\/\//.test(url) || url.startsWith('/')
}

function looksLikeMermaid(source) {
  return /^(graph|flowchart|sequenceDiagram|classDiagram|stateDiagram|stateDiagram-v2|erDiagram|journey|gantt|pie|mindmap|timeline|gitGraph)\b/i.test(source.trim())
}

function resolveCanvasKind(tab) {
  if (!tab) return 'text'

  if (tab.kind) return normalizeCanvasKind(tab.kind) || 'text'
  if (tab.type) return normalizeCanvasKind(tab.type) || 'text'

  const mimeKind = kindFromMime(tab.mime)
  if (mimeKind) return mimeKind

  const content = tabContent(tab).trim()
  if (content) {
    if (/^<svg[\s>]/i.test(content)) return 'svg'
    if (/^(<!doctype\s+html\b|<html[\s>]|<head[\s>]|<body[\s>])/i.test(content)) return 'html'
    if (looksLikeMermaid(content)) return 'mermaid'
    if (isSafeIframeUrl(content)) return 'url'
  }

  const path = String(tab.path || '').trim()
  if (path) {
    if (isSafeIframeUrl(path)) return 'url'
    return canvasExtKinds[extOf(path)] || 'binary'
  }

  if (isSafeIframeUrl(tab.url)) return 'url'
  return 'text'
}

function canvasKind(tab) {
  return resolveCanvasKind(tab)
}

function canvasHasSource(tab) {
  return !!tab?.path && editableCanvasKinds.has(canvasKind(tab))
}

function canvasUrl(tab) {
  if (!tab || !tab.path) return 'about:blank'
  return `/api/v1/workspace/live/${normalizeFilePath(tab.path).replace(/^(project|code)\//, '')}`
}

function canvasSrc(tab) {
  const kind = canvasKind(tab)
  if (kind === 'file') return canvasUrl(tab)
  if (kind === 'url') {
    const url = String(tab?.url || tab?.path || tabContent(tab)).trim()
    return isSafeIframeUrl(url) ? url : 'about:blank'
  }
  return 'about:blank'
}

function canvasSrcdoc(tab) {
  const kind = canvasKind(tab)
  if (kind === 'html') return htmlDoc(tabContent(tab))
  if (kind === 'svg') return svgDoc(tabContent(tab))
  if (kind === 'mermaid') return mermaidDoc(tabContent(tab))
  return textDoc(tab)
}

function canvasMeta(tab) {
  if (!tab) return ''
  const meta = []
  if (tab.mime) meta.push(`mime: ${tab.mime}`)
  if (tab.path) meta.push(`path: ${tab.path}`)
  if (tab.url) meta.push(`url: ${tab.url}`)
  return meta.join(' · ')
}

function htmlDoc(content) {
  const html = String(content || '')
  if (/^\s*(<!doctype\s+html\b|<html[\s>])/i.test(html)) return html
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
</head>
<body>
${html}
</body>
</html>`
}

function sanitizeSvg(svg) {
  return String(svg || '')
    .replace(/<script[\s\S]*?<\/script>/gi, '')
    .replace(/<foreignObject[\s\S]*?<\/foreignObject>/gi, '')
    .replace(/\son[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)/gi, '')
    .replace(/\s(?:href|xlink:href)\s*=\s*(["'])\s*javascript:[\s\S]*?\1/gi, '')
}

function svgDoc(content) {
  const svg = sanitizeSvg(content).trim()
  const body = /^<svg[\s>]/i.test(svg) ? svg : `<pre>${escapeHtml(svg)}</pre>`
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    html, body { margin: 0; width: 100%; min-height: 100%; background: #fff; }
    body { display: flex; align-items: center; justify-content: center; padding: 16px; box-sizing: border-box; }
    svg { max-width: 100%; max-height: 100vh; }
    pre { white-space: pre-wrap; color: #334155; font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
  </style>
</head>
<body>
${body}
</body>
</html>`
}

function mermaidDoc(content) {
  const source = escapeHtml(content || '')
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    html, body { margin: 0; min-height: 100%; background: #fff; color: #0f172a; }
    body { padding: 24px; box-sizing: border-box; font-family: Inter, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    .mermaid { display: flex; justify-content: center; }
    .error { margin-top: 16px; color: #b91c1c; white-space: pre-wrap; font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
  </style>
</head>
<body>
  <div class="mermaid">${source}</div>
  <pre class="error"></pre>
  ${'<script type="module">'}
    import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@10.9.1/dist/mermaid.esm.min.mjs';
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict' });
    const el = document.querySelector('.mermaid');
    mermaid.run({ nodes: [el] }).catch((err) => {
      document.querySelector('.error').textContent = err?.message || String(err);
    });
  </scr${'ipt'}>
</body>
</html>`
}

function textDoc(tab) {
  const rows = [
    ['renderer', canvasKind(tab)],
    ['title', tab?.title || ''],
    ['mime', tab?.mime || ''],
    ['path', tab?.path || ''],
    ['url', tab?.url || ''],
  ].filter(([, value]) => value)
  const meta = rows.map(([key, value]) => `<div><dt>${escapeHtml(key)}</dt><dd>${escapeHtml(value)}</dd></div>`).join('')
  const content = tabContent(tab)
  return `<div class="canvas-fallback-inner">
    <h3>Canvas content</h3>
    <dl class="canvas-fallback-meta">${meta}</dl>
    <pre>${escapeHtml(content)}</pre>
  </div>`
}

function refreshCanvas() {
  const tab = activeCanvasTab.value
  if (!tab) return
  tab._refreshKey = Date.now()
}

function openCanvasInCode() {
  const tab = activeCanvasTab.value
  if (!tab || !tab.path) return
  const ext = extOf(tab.path)
  openCodeFile(fileName(tab.path), tab.path, ext || 'html', { edit: true })
}

function openViewer(m) {
  viewerFile.value = m
  nextTick(() => { viewerEl.value?.focus() })
}

function onThumbLoad(e) {
  e.target.classList.add('loaded')
}

function onVideoMeta(e, m) {
  const v = e.target
  v.currentTime = 1
  if (v.videoWidth && v.videoHeight) {
    m.ratio = v.videoWidth / v.videoHeight
  }
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
  return String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
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
  /* No visible column line at rest — the gutter reveals the divider on hover. */
  border-left: 1px solid transparent;
  height: 100%; min-width: 320px;
}

/* ── Column header ───────────────────────────────────────── */
/* Right-aligned so the utility cluster lines up with the chat header's. */
.panel-header { justify-content: flex-end; }

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
a.editor-btn { text-decoration: none; }

/* Canvas viewer */
.canvas-viewer { width: 100%; height: 100%; display: flex; flex-direction: column; }
.canvas-toolbar {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 12px; border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0; background: var(--surface);
}
.canvas-title {
  display: flex; align-items: center; gap: 6px;
  min-width: 0; overflow: hidden;
}
.canvas-kind {
  flex-shrink: 0; padding: 1px 5px; border-radius: 4px;
  border: 1px solid var(--border-subtle);
  color: var(--text-muted); font-size: 10px; font-family: var(--mono);
  text-transform: uppercase; letter-spacing: 0.04em;
}
.canvas-meta {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--text-muted); font-size: 10px; font-family: var(--mono);
}
.canvas-iframe {
  flex: 1; width: 100%; border: none;
  background: #fff;
}
.canvas-image {
  flex: 1; overflow: auto; display: flex; align-items: center; justify-content: center;
  padding: 16px; background: var(--surface);
}
.canvas-image img { max-width: 100%; max-height: 100%; object-fit: contain; }
.canvas-markdown {
  flex: 1; overflow: auto; padding: 16px 24px;
  background: var(--surface); color: var(--text-secondary);
  font-size: 13px; line-height: 1.6;
}
.canvas-binary {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 8px; padding: 24px; background: var(--surface); text-align: center;
}
.canvas-binary-name { font-size: 13px; color: var(--text-secondary); word-break: break-all; }
.canvas-binary-meta { font-size: 11px; font-family: var(--mono); color: var(--text-muted); word-break: break-all; }
.canvas-fallback {
  flex: 1; overflow: auto; padding: 16px;
  background: var(--surface); color: var(--text-secondary);
}
.canvas-fallback-inner {
  max-width: 920px; margin: 0 auto;
  font-family: var(--mono); font-size: 12px; line-height: 1.5;
}
.canvas-fallback-inner h3 {
  margin: 0 0 12px; color: var(--text); font-size: 13px; font-weight: 600;
}
.canvas-fallback-meta {
  display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 4px 10px;
  margin: 0 0 14px; padding: 10px; border: 1px solid var(--border-subtle);
  border-radius: 6px; background: var(--surface-elevated, transparent);
}
.canvas-fallback-meta div { display: contents; }
.canvas-fallback-meta dt { color: var(--text-muted); }
.canvas-fallback-meta dd {
  margin: 0; min-width: 0; overflow-wrap: anywhere; color: var(--text-secondary);
}
.canvas-fallback pre {
  margin: 0; white-space: pre-wrap; overflow-wrap: anywhere;
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
.files-path-input {
  flex: 1; min-width: 0; padding: 3px 6px; border-radius: 4px;
  background: none; border: 1px solid transparent; outline: none;
}
.files-path-input:hover, .files-path-input:focus { border-color: var(--border); color: var(--text-secondary); }
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

/* Media grid — row-packed Tetris layout */
.media-grid {
  padding: 2px;
  overflow-y: auto;
  flex: 1;
}
.media-row {
  display: flex;
  gap: 2px;
  margin-bottom: 2px;
}
.media-thumb {
  position: relative;
  display: flex;
  flex-direction: column;
  min-width: 0;
  cursor: pointer;
  border-radius: 3px;
  overflow: hidden;
  transition: transform 0.15s ease, box-shadow 0.15s ease;
  background: #000;
}
.media-thumb:hover {
  transform: scale(1.02);
  box-shadow: 0 2px 8px rgba(0,0,0,0.25);
}
.media-thumb img {
  width: 100%;
  height: auto;
  display: block;
  object-fit: cover;
  opacity: 0;
  transition: opacity 0.2s ease;
}
.media-thumb img.loaded {
  opacity: 1;
}
.media-video-thumb {
  width: 100%;
  position: relative;
  background: #000;
}
.video-preview {
  width: 100%;
  height: auto;
  display: block;
  object-fit: cover;
}
.video-play-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0,0,0,0.25);
  opacity: 0;
  transition: opacity 0.15s ease;
}
.media-thumb:hover .video-play-overlay {
  opacity: 1;
}
.media-name {
  font-size: 8px;
  font-family: var(--mono);
  color: rgba(255,255,255,0.7);
  text-align: center;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  width: 100%;
  padding: 2px 4px;
  background: rgba(0,0,0,0.6);
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
