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
          :src="'/api/v1/file?path=' + encodeURIComponent(panel.activeTab.path)"
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
      <div v-else-if="panel.activeTab.plugin === 'files'" class="plugin-slot">
        <div class="plugin-placeholder">
          <svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5">
            <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/>
          </svg>
          <span>File Manager</span>
          <span class="plugin-hint">Browse project files</span>
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
  </div>
</template>

<script setup>
/**
 * desc: Composable side panel that renders plugin-based tabs (preview, code, canvas, graph, files)
 */
import { ref } from 'vue'
import { usePanelStore } from '../stores/panel'

const panel = usePanelStore()
const canvasEl = ref(null)
const overflowRef = ref(null)
const showOverflow = ref(false)
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
</style>
