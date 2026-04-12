<template>
  <div :class="['trace', { collapsed: !expanded }]">
    <div class="trace-header" @click="expanded = !expanded">
      <span class="h-chr">{{ expanded ? '─' : '+' }}</span>
      <span class="h-title">trace</span>
      <span class="h-sep">──</span>
      <span class="h-val">{{ nodes.length }}</span>
      <span class="h-dim">nodes</span>
      <span class="h-sep">·</span>
      <span class="h-val">{{ totalMs }}</span>
      <span class="h-dim">ms</span>
      <span :class="['h-status', status]">{{ statusLabel }}</span>
    </div>

    <transition name="expand">
      <div v-if="expanded" class="trace-body">
        <template v-for="(item, i) in layout" :key="item.key">

          <div v-if="item.type === 'wave-open'" class="tl tl-wave">
            <span class="t-idx"></span>
            <span class="t-pipe">│</span>
            <span class="t-wf">┬</span>
          </div>

          <div v-if="item.type === 'wave-close'" class="tl tl-wave">
            <span class="t-idx"></span>
            <span class="t-pipe">│</span>
            <span class="t-wf">┴</span>
          </div>

          <div v-if="item.type === 'node'" class="tl tl-clickable" @click="toggleResult(item.node.id)">
            <span class="t-idx">{{ pad(item.index) }}</span>
            <span class="t-pipe">│</span>
            <span v-if="item.inWave" class="t-wf">├</span>
            <span :class="['t-ty', item.node.type, { 'is-skill': item.node.source === 'skillmd' }]">{{ item.node.source === 'skillmd' ? 'SKI' : tyLabel(item.node.type) }}</span>
            <span :class="['t-name', { 'is-skill': item.node.source === 'skillmd' }]">{{ item.node.tag || item.node.tool || item.node.id }}</span>
            <span v-if="item.node.params" class="t-params">[{{ compactParams(item.node.params) }}]</span>
            <span v-if="item.node.summary" class="t-summary">{{ item.node.summary }}</span>
            <span class="t-ms">{{ item.node.ms || 0 }}ms</span>
            <span v-if="item.node.result_size" class="t-size">{{ fmtSize(item.node.result_size) }}</span>
            <span :class="['t-st', item.node.state]">{{ stChr(item.node.state) }}</span>
            <span v-if="item.node.result" class="t-expand">{{ expandedResults[item.node.id] ? '−' : '+' }}</span>
          </div>

          <div v-if="item.type === 'node' && item.node.result && expandedResults[item.node.id]" class="tl-result">
            <pre class="t-result-content">{{ item.node.result }}</pre>
          </div>

          <div v-if="item.type === 'dep'" class="tl tl-sub">
            <span class="t-idx"></span>
            <span class="t-pipe">│</span>
            <span v-if="item.inWave" class="t-wf">│</span>
            <span class="t-art">╰──</span>
            <span class="t-dep-arrow">←</span>
            <span class="t-dep-ref">{{ item.name }}</span>
          </div>

          <div v-if="item.type === 'spawn'" class="tl tl-sub">
            <span class="t-idx"></span>
            <span class="t-pipe">│</span>
            <span v-if="item.inWave" class="t-wf">│</span>
            <span class="t-art">╰──</span>
            <span class="t-spawn">spawned by {{ item.name }}</span>
          </div>

          <div v-if="item.type === 'skills'" class="tl tl-sub">
            <span class="t-idx"></span>
            <span class="t-pipe">│</span>
            <span v-if="item.inWave" class="t-wf">│</span>
            <span class="t-art">╰──</span>
            <span class="t-skill-label">guided by</span>
            <span v-for="s in item.skills" :key="s" class="t-skill-chip">{{ s }}</span>
          </div>

          <div v-if="item.type === 'error'" class="tl tl-sub tl-clickable" @click="toggleResult('err-' + item.key)">
            <span class="t-idx"></span>
            <span class="t-pipe">│</span>
            <span v-if="item.inWave" class="t-wf">│</span>
            <span :class="['t-err-badge', item.errType || 'exec']">{{ errLabel(item.errType) }}</span>
            <span class="t-err-msg">{{ trunc(item.msg, 55) }}</span>
            <span class="t-expand">{{ expandedResults['err-' + item.key] ? '−' : '+' }}</span>
          </div>

          <div v-if="item.type === 'error' && expandedResults['err-' + item.key]" class="tl-result">
            <pre class="t-result-content">{{ item.msg }}</pre>
          </div>

        </template>

        <div class="tl tl-footer">
          <span class="t-idx">──</span>
          <span class="t-pipe">╰─</span>
          <span class="t-dim">{{ nodes.length }} nodes</span>
          <span class="t-dim">·</span>
          <span class="t-dim">{{ totalMs }}ms</span>
          <span class="t-dim">·</span>
          <span :class="['t-final', status]">{{ statusLabel }}</span>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup>
/**
 * desc: DAG execution trace visualizer that renders nodes, dependencies, waves, and errors in a terminal-style layout
 */
import { computed, ref, watch } from 'vue'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  running: { type: Boolean, default: false },
})

const expanded = ref(true)
const expandedResults = ref({})
watch(() => props.running, (val) => { if (!val && props.nodes.length > 0) setTimeout(() => { expanded.value = false }, 2500) })
watch(() => props.nodes.length, (n, o) => { if (n > 0 && o === 0) expanded.value = true })

/**
 * desc: Toggle the expanded/collapsed state of a node's result content
 * @param {string} id - The node ID whose result visibility to toggle
 * @returns {void}
 */
function toggleResult(id) {
  expandedResults.value[id] = !expandedResults.value[id]
}

/**
 * desc: Compute the overall trace status based on node states and running flag
 * @returns {string} One of 'live', 'fail', 'done', or 'idle'
 */
const status = computed(() => {
  if (props.running) return 'live'
  const failed = failCount.value
  const total = props.nodes.length
  if (failed > 0 && failed < total) return 'partial'
  if (failed > 0) return 'fail'
  if (total && props.nodes.every(n => n.state === 'resolved' || n.state === 'skipped')) return 'done'
  return 'idle'
})

const statusLabel = computed(() => {
  if (status.value === 'live') return 'live'
  if (status.value === 'done') return 'done'
  if (status.value === 'partial') {
    const passed = props.nodes.filter(n => n.state === 'resolved').length
    return `${passed} ok · ${failCount.value} failed`
  }
  if (status.value === 'fail') return `${failCount.value} failed`
  return 'idle'
})

/**
 * desc: Compute the total execution time in milliseconds across all nodes
 * @returns {number} Sum of all node ms values
 */
const totalMs = computed(() => props.nodes.reduce((s, n) => s + (n.ms || 0), 0))

/**
 * desc: Compute the number of failed nodes in the trace
 * @returns {number} Count of nodes with state 'failed'
 */
const failCount = computed(() => props.nodes.filter(n => n.state === 'failed').length)

/**
 * desc: Build the flat layout array from nodes, grouping independent tools into waves and attaching deps/spawns/errors
 * @returns {Array<Object>} Layout items for rendering (node, wave-open, wave-close, dep, spawn, error)
 */
const layout = computed(() => {
  const nodes = [...props.nodes]
  // Sort by node ID (chronological creation order). IDs are "n1", "n2", etc.
  // Executive and aggregator are pinned to start/end.
  nodes.sort((a, b) => {
    const aIsBookend = a.type === 'executive' ? -1 : a.type === 'aggregator' ? 1 : 0
    const bIsBookend = b.type === 'executive' ? -1 : b.type === 'aggregator' ? 1 : 0
    if (aIsBookend !== bIsBookend) return aIsBookend - bIsBookend
    // Extract numeric part of ID for natural sort (n1, n2, ... n10, n11)
    const aNum = parseInt((a.id || '').replace(/\D/g, '')) || 0
    const bNum = parseInt((b.id || '').replace(/\D/g, '')) || 0
    return aNum - bNum
  })

  const items = []
  let idx = 0
  let i = 0

  while (i < nodes.length) {
    const n = nodes[i]

    if (n.type === 'tool' && (!n.deps || !n.deps.length) && !n.spawn) {
      let waveEnd = i + 1
      while (waveEnd < nodes.length &&
             nodes[waveEnd].type === 'tool' &&
             (!nodes[waveEnd].deps || !nodes[waveEnd].deps.length) &&
             !nodes[waveEnd].spawn) {
        waveEnd++
      }

      if (waveEnd - i >= 2) {
        items.push({ type: 'wave-open', key: `wo-${i}` })
        for (let j = i; j < waveEnd; j++) pushNode(items, nodes[j], idx++, true)
        items.push({ type: 'wave-close', key: `wc-${i}` })
        i = waveEnd
        continue
      }
    }

    pushNode(items, n, idx++, false)
    i++
  }
  return items
})

/**
 * desc: Append a node and its dependency, spawn, and error sub-items to the layout items array
 * @param {Array<Object>} items - The layout items array to push onto
 * @param {Object} n - The DAG node object
 * @param {number} idx - The sequential index for display
 * @param {boolean} inWave - Whether this node is inside a parallel wave group
 * @returns {void}
 */
function pushNode(items, n, idx, inWave) {
  items.push({ type: 'node', key: `n-${n.id}`, node: n, index: idx, inWave })
  if (n.skills && n.skills.length) {
    items.push({ type: 'skills', key: `sk-${n.id}`, skills: n.skills, inWave })
  }
  // Show only the immediate parent (spawner or first dep), not the full chain.
  // Use tag first (descriptive), then tool type as fallback.
  if (n.deps && n.deps.length && !n.spawn) {
    const parentId = n.deps[n.deps.length - 1] // last dep = most direct parent
    const dn = props.nodes.find(x => x.id === parentId)
    const label = dn ? (dn.tag || dn.tool || dn.id) : parentId
    items.push({ type: 'dep', key: `d-${n.id}-${parentId}`, name: label, inWave })
  }
  if (n.spawn) {
    const sn = props.nodes.find(x => x.id === n.spawn)
    if (sn) items.push({ type: 'spawn', key: `s-${n.id}`, name: sn.tool || sn.tag || sn.id, inWave })
  }
  if (n.err) {
    items.push({ type: 'error', key: `e-${n.id}`, msg: n.err, errType: n.err_type, inWave })
  }
}

/**
 * desc: Format node parameters into a compact key=value string for inline display
 * @param {string|Object} p - Parameters as a JSON string or object
 * @returns {string} Compact formatted parameter string
 */
function compactParams(p) {
  if (!p) return ''
  try {
    const obj = typeof p === 'string' ? JSON.parse(p) : p
    return Object.entries(obj).map(([k, v]) => {
      let val = typeof v === 'string' ? v : JSON.stringify(v)
      if (val.length > 18) val = val.slice(0, 18) + '\u2026'
      return `${k}=${val}`
    }).join(' ')
  } catch { return '' }
}

/**
 * desc: Format a byte count into a human-readable size string (b or kb)
 * @param {number} b - Size in bytes
 * @returns {string} Formatted size string
 */
function fmtSize(b) { return b < 1024 ? b + 'b' : (b / 1024).toFixed(1) + 'kb' }

/**
 * desc: Map an error type to its display label with icon
 * @param {string} t - Error type (gate, clearance, timeout, or other)
 * @returns {string} Formatted error label
 */
function errLabel(t) { return { gate: '\u26D4 GATE', clearance: '\uD83D\uDD12 CLEARANCE', timeout: '\u23F1 TIMEOUT' }[t] || '\u2717 ERROR' }

/**
 * desc: Zero-pad an index number to two digits for display alignment
 * @param {number} i - The index to pad
 * @returns {string} Two-character padded string
 */
function pad(i) { return String(i).padStart(2, '0') }

/**
 * desc: Map a node state to its single-character status symbol
 * @param {string} s - Node state (running, resolved, failed, skipped)
 * @returns {string} Status character
 */
function stChr(s) { return { running: '\u25B8', resolved: '\u2713', failed: '\u2717', skipped: '\u2013' }[s] || '\u00B7' }

/**
 * desc: Map a node type to its three-letter abbreviation label
 * @param {string} t - Node type (planner, aggregator, skill, etc.)
 * @returns {string} Three-letter type label
 */
function tyLabel(t) { return { executive:'EXE', aggregator:'AGG', tool:'TLL', compute:'CMP', reflection:'RFL', observer:'OBS', micro_planner:'MPL', interjection:'INJ', actuator:'ACT', holmes:'RCA' }[t] || '???' }

/**
 * desc: Truncate a string to a maximum length, appending an ellipsis if needed
 * @param {string} s - The string to truncate
 * @param {number} n - Maximum length before truncation
 * @returns {string} Truncated string
 */
function trunc(s, n) { return s && s.length > n ? s.slice(0, n) + '\u2026' : s }
</script>

<style scoped>
.trace {
  margin: 6px 0;
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.2;
  border-left: 1px solid var(--border);
  padding-left: 10px;
}

.trace-header {
  display: flex; align-items: center; gap: 6px;
  cursor: pointer; user-select: none;
  color: var(--text-muted); transition: color var(--transition);
}
.trace-header:hover { color: var(--text-secondary); }
.h-chr { color: var(--text-muted); }
.h-title { font-weight: 700; text-transform: uppercase; letter-spacing: 0.1em; color: var(--text-secondary); font-size: 10px; }
.h-sep { color: var(--border); }
.h-val { color: var(--text); font-weight: 600; }
.h-dim { color: var(--text-muted); }
.h-fail { color: var(--signal-red); font-weight: 600; font-size: 10px; } /* kept for compat */
.h-status { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em; }
.h-status.live { color: var(--accent); }
.h-status.done { color: var(--signal-green); }
.h-status.partial { color: var(--signal-amber); }
.h-status.fail { color: var(--signal-red); }

.trace-body { padding: 4px 0; }

.tl { display: flex; align-items: baseline; gap: 5px; padding: 2px 0; white-space: nowrap; }
.tl-sub { padding: 0; }
.tl-wave { padding: 0; }

.t-idx { width: 16px; text-align: right; color: var(--text-muted); font-size: 10px; flex-shrink: 0; }
.t-pipe { color: var(--border); flex-shrink: 0; width: 8px; }

/* Wave fork chars */
.t-wf { color: var(--accent); opacity: 0.4; flex-shrink: 0; width: 8px; }

/* Type — now before name */
.t-ty { width: 26px; flex-shrink: 0; font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; }
.t-ty.executive, .t-ty.aggregator { color: var(--accent); }
.t-ty.reflection, .t-ty.observer, .t-ty.micro_planner { color: var(--accent-warm); }
.t-ty.tool { color: var(--text-muted); }
.t-ty.compute { color: #60a5fa; }
.t-ty.holmes { color: #a78bfa; } /* RCA — purple, groups with dep arrows/expansions */
.t-ty.is-skill { color: #e879a0; }
.t-name.is-skill { color: #e879a0; font-style: italic; }
.t-ty.interjection { color: var(--signal-amber); }
.t-ty.actuator { color: var(--signal-red); }

.t-name { color: var(--text); font-weight: 500; }
.t-params { color: var(--text-muted); font-size: 10px; max-width: 220px; overflow: hidden; text-overflow: ellipsis; }
.t-summary { color: var(--text-secondary); font-size: 10px; font-style: italic; max-width: 300px; overflow: hidden; text-overflow: ellipsis; }
.t-ms { color: var(--text-muted); font-size: 10px; }
.t-size { color: var(--text-muted); font-size: 9px; opacity: 0.6; }
.t-expand { color: var(--accent); font-weight: 700; font-size: 10px; opacity: 0.6; cursor: pointer; }
.t-expand:hover { opacity: 1; }

/* State — at the end now */
.t-st { width: 10px; text-align: center; font-weight: 700; flex-shrink: 0; }
.t-st.running { color: var(--accent); animation: blink-st 1s step-end infinite; }
.t-st.resolved { color: var(--signal-green); }
.t-st.failed { color: var(--signal-red); }
.t-st.skipped, .t-st.pending { color: var(--text-muted); }
@keyframes blink-st { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }

/* Deps — purple to match compute (CMP) so the dependency lines visually
   group with the compute nodes they're chaining instead of stealing the
   eye with the dark-pink accent. */
.t-art { color: #a78bfa; opacity: 0.5; }
.t-dep-arrow { color: #a78bfa; }
.t-dep-ref { color: #a78bfa; font-size: 10px; font-weight: 500; }
.t-spawn { color: var(--accent-warm); font-size: 10px; }
.t-skill-label { color: var(--text-dim); font-size: 10px; font-style: italic; }
.t-skill-chip {
  display: inline-block;
  font-size: 9px;
  font-weight: 600;
  letter-spacing: 0.03em;
  padding: 1px 6px;
  margin-left: 4px;
  border-radius: 3px;
  background: var(--accent-bg, rgba(100, 150, 200, 0.15));
  color: var(--accent, #6ea8d8);
  border: 1px solid var(--accent-border, rgba(100, 150, 200, 0.35));
}

/* Errors — dark pink (#c0428a) so failures are sharper than the soft
   light-pink signal-red they used to share with informational warnings.
   Hardcoded so the contrast holds in both light and dark themes. */
.t-err-badge { font-size: 9px; font-weight: 700; letter-spacing: 0.05em; padding: 0 4px; border-radius: 2px; }
.t-err-badge.gate { color: #c0428a; background: var(--signal-red-bg); }
.t-err-badge.clearance { color: var(--signal-amber); background: var(--signal-amber-bg); }
.t-err-badge.timeout { color: var(--accent-warm); background: var(--accent-warm-subtle); }
.t-err-badge.exec { color: #c0428a; }
.t-err-msg { color: #c0428a; font-size: 10px; opacity: 0.85; }

/* Footer */
.tl-footer { margin-top: 3px; }
.t-dim { color: var(--text-muted); }
.t-err-count { color: #c0428a; }
.t-final { font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em; font-size: 10px; }
.t-final.done { color: var(--signal-green); }
.t-final.partial { color: var(--signal-amber); }
.t-final.fail { color: var(--signal-red); }
.t-final.live { color: var(--accent); }

/* Clickable node rows */
.tl-clickable { cursor: pointer; border-radius: 2px; transition: background var(--transition); }
.tl-clickable:hover { background: var(--surface-hover, rgba(128,128,128,0.08)); }

/* Expanded result block — purple left border so the expanded payload
   visually belongs to the dep/compute family rather than the accent. */
.tl-result {
  margin: 0 0 4px 30px;
  padding: 6px 8px;
  background: var(--surface-alt, rgba(0,0,0,0.04));
  border-left: 2px solid #a78bfa;
  border-radius: 0 3px 3px 0;
  max-height: 200px;
  overflow-y: auto;
}
.t-result-content {
  margin: 0;
  font-size: 10px;
  line-height: 1.4;
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

.expand-enter-active { transition: all 0.25s ease; }
.expand-leave-active { transition: all 0.15s ease; }
.expand-enter-from, .expand-leave-to { opacity: 0; max-height: 0; overflow: hidden; }
.expand-enter-to, .expand-leave-from { max-height: 1000px; }
</style>
