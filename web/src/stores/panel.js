import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

/** Reactive state for the composable panel. Tabs, layout, plugins. */
export const usePanelStore = defineStore('panel', () => {
  const open = ref(false)
  const width = ref(parseInt(localStorage.getItem('kaiju_panel_w')) || 480)
  const tabs = ref([])
  const activeTabId = ref(null)
  const MAX_VISIBLE_TABS = 5

  let tabCounter = 0

  const plugins = ref([
    { id: 'canvas',  label: 'Canvas' },
    { id: 'code',    label: 'Code' },
    { id: 'files',   label: 'Files' },
    { id: 'preview', label: 'Preview' },
    { id: 'graph',   label: 'Graph' },
  ])

  const visibleTabs = computed(() => tabs.value.slice(0, MAX_VISIBLE_TABS))
  const overflowTabs = computed(() => tabs.value.slice(MAX_VISIBLE_TABS))
  const activeTab = computed(() => tabs.value.find(t => t.id === activeTabId.value) || null)

  /**
   * desc: Open or focus a tab in the panel; reuses an existing tab if one matches the given path
   * @param {Object} options - Tab configuration
   * @param {string} options.plugin - Plugin type (canvas, code, files, preview, graph)
   * @param {string} [options.title] - Display title for the tab
   * @param {string|null} [options.path] - File path for deduplication and display
   * @param {string|null} [options.content] - Raw content to render in the tab
   * @param {string|null} [options.mime] - MIME type of the content
   * @param {number} [options.line] - Line number to scroll to
   * @returns {string} The tab ID (new or existing)
   */
  function pushTab({ plugin, title, path, content, mime, line }) {
    if (path) {
      const existing = tabs.value.find(t => t.path === path)
      if (existing) {
        existing.content = content || existing.content
        existing.mime = mime || existing.mime
        existing.line = line || existing.line
        existing.ts = Date.now()
        existing.title = title || existing.title
        activateTab(existing.id)
        return existing.id
      }
    }
    const id = `tab-${++tabCounter}`
    tabs.value.unshift({ id, plugin: plugin || 'preview', title: title || plugin || 'Untitled', path: path || null, content: content || null, mime: mime || null, line: line || 0, ts: Date.now() })
    activeTabId.value = id
    open.value = true
    return id
  }

  /**
   * desc: Bring an existing tab to the front and make it the active tab
   * @param {string} id - The tab ID to activate
   * @returns {void}
   */
  function activateTab(id) {
    const idx = tabs.value.findIndex(t => t.id === id)
    if (idx < 0) return
    const tab = tabs.value.splice(idx, 1)[0]
    tab.ts = Date.now()
    tabs.value.unshift(tab)
    activeTabId.value = id
    open.value = true
  }

  /**
   * desc: Close a tab by ID and select the next available tab, hiding the panel if none remain
   * @param {string} id - The tab ID to close
   * @returns {void}
   */
  function closeTab(id) {
    const idx = tabs.value.findIndex(t => t.id === id)
    if (idx < 0) return
    tabs.value.splice(idx, 1)
    if (activeTabId.value === id) activeTabId.value = tabs.value[0]?.id || null
    if (!tabs.value.length) open.value = false
  }

  /**
   * desc: Toggle the panel open or closed, opening a default Files tab if no tabs exist
   * @returns {void}
   */
  function toggle() {
    if (open.value) { open.value = false }
    else {
      open.value = true
      if (!tabs.value.length) pushTab({ plugin: 'files', title: 'Files' })
    }
  }

  /**
   * desc: Hide the panel without removing any tabs
   * @returns {void}
   */
  function hide() { open.value = false }

  /**
   * desc: Set the panel width and persist it to localStorage
   * @param {number} w - The new width in pixels
   * @returns {void}
   */
  function setWidth(w) {
    width.value = w
    localStorage.setItem('kaiju_panel_w', String(w))
  }

  /**
   * desc: Register a new plugin type if it does not already exist
   * @param {Object} descriptor - Plugin descriptor with id and label
   * @param {string} descriptor.id - Unique plugin identifier
   * @param {string} descriptor.label - Display label
   * @returns {void}
   */
  function registerPlugin(descriptor) {
    if (!plugins.value.find(p => p.id === descriptor.id)) plugins.value.push(descriptor)
  }

  return {
    open, width, tabs, activeTabId, plugins,
    visibleTabs, overflowTabs, activeTab,
    pushTab, activateTab, closeTab,
    toggle, hide, setWidth, registerPlugin,
  }
})
