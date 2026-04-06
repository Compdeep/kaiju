<template>
  <div class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-panel">
      <div class="modal-header">
        <h2>settings</h2>
        <button class="btn-icon" @click="$emit('close')">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
      </div>
      <div class="modal-tabs">
        <button :class="['modal-tab', { active: tab === 'models' }]" @click="tab = 'models'">models</button>
        <button :class="['modal-tab', { active: tab === 'agent' }]" @click="tab = 'agent'">agent</button>
        <button :class="['modal-tab', { active: tab === 'display' }]" @click="tab = 'display'">display</button>
      </div>
      <div class="modal-body">
        <transition name="slide" mode="out-in">

          <!-- Models Tab -->
          <div v-if="tab === 'models'" key="models">

            <!-- API Key (shared) -->
            <div class="form-group">
              <label>api key <span v-if="cfg.llm.api_key" class="key-set">&#10003; set</span><span v-else class="key-missing">not set</span></label>
              <div class="key-row">
                <input v-model="apiKey" type="password" :placeholder="cfg.llm.api_key ? 'enter new key to replace' : 'paste key'" />
                <button class="btn btn-sm" @click="saveKey" :disabled="!apiKey">save</button>
              </div>
            </div>

            <div class="divider"></div>

            <!-- Reasoning Model -->
            <div class="model-section">
              <div class="model-label">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                reasoning
              </div>
              <div class="model-desc">planner, aggregator, classifier, direct responses</div>
              <div class="form-row">
                <div class="form-group">
                  <label>provider</label>
                  <select v-model="cfg.llm.provider" @change="onReasoningProviderChange">
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="ollama">Ollama</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>model</label>
                  <select v-model="cfg.llm.model" @change="patchConfig">
                    <option v-for="m in reasoningModels" :key="m.id" :value="m.id">{{ m.name }}</option>
                  </select>
                </div>
              </div>
            </div>

            <div class="divider"></div>

            <!-- Executor Model -->
            <div class="model-section">
              <div class="model-label">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>
                executor
              </div>
              <div class="model-desc">reflection, observer, micro-planner, compactor</div>
              <div class="form-row">
                <div class="form-group">
                  <label>provider</label>
                  <select v-model="execProvider" @change="onExecutorProviderChange">
                    <option value="">same as reasoning</option>
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="ollama">Ollama</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>model</label>
                  <select v-model="cfg.executor.model" @change="patchConfig">
                    <option value="">same as reasoning</option>
                    <option v-for="m in executorModels" :key="m.id" :value="m.id">{{ m.name }}</option>
                  </select>
                </div>
              </div>
            </div>

            <div class="divider"></div>

            <div class="form-group">
              <label>endpoint (reasoning)</label>
              <input v-model="cfg.llm.endpoint" @change="patchConfig" />
            </div>
          </div>

          <!-- Agent Tab -->
          <div v-else-if="tab === 'agent'" key="agent">
            <div class="form-group">
              <label>dag mode</label>
              <select v-model="cfg.agent.dag_mode" @change="patchConfig">
                <option value="reflect">reflect — conservative</option>
                <option value="nReflect">nReflect — balanced</option>
                <option value="orchestrator">orchestrator — interactive</option>
              </select>
            </div>
            <div class="form-group">
              <label>planner mode</label>
              <select v-model="cfg.agent.planner_mode" @change="patchConfig">
                <option value="structured">structured — text JSON parsing</option>
                <option value="native">native — function calling</option>
              </select>
            </div>
            <div class="form-group">
              <label>default safety</label>
              <select v-model.number="cfg.agent.safety_level" @change="patchConfig">
                <option v-for="i in intentOptions" :key="i.name" :value="i.rank">{{ i.name }} ({{ i.rank }})</option>
              </select>
            </div>
          </div>

          <!-- Display Tab -->
          <div v-else key="display">
            <div class="toggle-row">
              <div>
                <div class="toggle-label">dark mode</div>
                <div class="toggle-desc">{{ settings.theme === 'dark' ? 'dark pink + green' : 'white + blue' }}</div>
              </div>
              <button class="toggle-switch" :class="{ on: settings.theme === 'dark' }" @click="settings.toggleTheme()">
                <span class="toggle-knob"></span>
              </button>
            </div>
          </div>

        </transition>
      </div>
    </div>
  </div>
</template>

<script setup>
/**
 * desc: Settings modal for configuring LLM providers/models, executor settings, agent behavior, and display preferences
 */
import { ref, computed, onMounted } from 'vue'
import { useSettingsStore } from '../stores/settings'
import api from '../api/client'

defineEmits(['close'])
const settings = useSettingsStore()
const tab = ref('models')
const cfg = ref({ llm: { provider: '', model: '', endpoint: '' }, executor: { provider: '', model: '' }, agent: { dag_mode: '', planner_mode: 'structured', safety_level: 1 } })
const allModels = ref([])
const apiKey = ref('')
const execProvider = ref('')
const intentOptions = ref([])

const ENDPOINTS = {
  openai: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com',
  openrouter: 'https://openrouter.ai/api/v1',
  ollama: 'http://localhost:11434/v1',
}

/**
 * desc: Filter the full model list to only those matching the reasoning provider
 * @returns {Array<Object>} Models available for the selected reasoning provider
 */
const reasoningModels = computed(() => {
  const p = cfg.value.llm.provider
  return allModels.value.filter(m => m.provider === p)
})

/**
 * desc: Filter the full model list to only those matching the executor provider (or reasoning provider as fallback)
 * @returns {Array<Object>} Models available for the selected executor provider
 */
const executorModels = computed(() => {
  const p = execProvider.value || cfg.value.llm.provider
  return allModels.value.filter(m => m.provider === p)
})

/**
 * desc: Handle reasoning provider change by updating the endpoint and selecting the first available model
 * @returns {void}
 */
function onReasoningProviderChange() {
  cfg.value.llm.endpoint = ENDPOINTS[cfg.value.llm.provider] || ''
  const available = reasoningModels.value
  if (available.length) cfg.value.llm.model = available[0].id
  patchConfig()
}

/**
 * desc: Handle executor provider change by updating the executor config and selecting the first available model
 * @returns {void}
 */
function onExecutorProviderChange() {
  cfg.value.executor.provider = execProvider.value
  const available = executorModels.value
  if (available.length) cfg.value.executor.model = available[0].id
  else cfg.value.executor.model = ''
  patchConfig()
}

/**
 * desc: Persist the current LLM, executor, and agent configuration to the server
 * @returns {Promise<void>}
 */
async function patchConfig() {
  try {
    await api.patch('/api/v1/config', {
      llm: { provider: cfg.value.llm.provider, model: cfg.value.llm.model, endpoint: cfg.value.llm.endpoint },
      executor: { provider: cfg.value.executor.provider || undefined, model: cfg.value.executor.model || undefined },
      agent: { dag_mode: cfg.value.agent.dag_mode, planner_mode: cfg.value.agent.planner_mode, safety_level: cfg.value.agent.safety_level },
    })
  } catch (err) { console.error('config patch:', err) }
}

/**
 * desc: Save a new API key to the server and refresh the config
 * @returns {Promise<void>}
 */
async function saveKey() {
  if (!apiKey.value) return
  try {
    await api.patch('/api/v1/config', { llm: { api_key: apiKey.value } })
    apiKey.value = ''
    cfg.value = await api.get('/api/v1/config')
  } catch (err) { alert(err.message) }
}

onMounted(async () => {
  try {
    const [c, m] = await Promise.all([api.get('/api/v1/config'), api.get('/api/v1/models')])
    cfg.value = c
    if (!cfg.value.executor) cfg.value.executor = { provider: '', model: '' }
    execProvider.value = cfg.value.executor.provider || ''
    allModels.value = m
  } catch (err) { console.error('settings load:', err) }
  // Load intent registry — the sole source of truth for the default-safety
  // dropdown. On failure the dropdown is empty; no hardcoded fallback.
  try {
    const list = await api.get('/api/v1/intents')
    if (Array.isArray(list)) {
      intentOptions.value = list.map(i => ({ name: i.name, rank: i.rank }))
    }
  } catch (err) {
    console.error('[settings] failed to load intents registry:', err)
    intentOptions.value = []
  }
})
</script>

<style scoped>
.key-row { display: flex; gap: 8px; }
.key-row input { flex: 1; }
.key-set { color: var(--signal-green); font-size: 10px; font-weight: 600; margin-left: 4px; text-transform: none; letter-spacing: 0; }
.key-missing { color: var(--signal-red); font-size: 10px; font-weight: 600; margin-left: 4px; text-transform: none; letter-spacing: 0; }
.divider { border-top: 1px solid var(--border-subtle); margin: 16px 0; }
.model-section { }
.model-label {
  display: flex; align-items: center; gap: 6px;
  font-size: 13px; font-weight: 600; font-family: var(--mono);
  color: var(--text); margin-bottom: 2px;
}
.model-desc { font-size: 11px; color: var(--text-muted); margin-bottom: 10px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.toggle-row { display: flex; justify-content: space-between; align-items: center; padding: 8px 0; }
.toggle-label { font-size: 13px; font-weight: 500; }
.toggle-desc { font-size: 11px; color: var(--text-muted); }
.toggle-switch {
  width: 40px; height: 22px; border-radius: 11px;
  background: var(--border); border: none; cursor: pointer;
  position: relative; transition: background var(--transition);
}
.toggle-switch.on { background: var(--accent); }
.toggle-knob {
  position: absolute; top: 2px; left: 2px;
  width: 18px; height: 18px; border-radius: 50%;
  background: white; transition: transform var(--transition);
}
.toggle-switch.on .toggle-knob { transform: translateX(18px); }
</style>
