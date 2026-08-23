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
                <input v-model="apiKey" type="password" name="kaiju-api-key" id="kaiju-api-key" autocomplete="off" data-form-type="other" data-1p-ignore data-lpignore="true" :placeholder="cfg.llm.api_key ? 'enter new key to replace' : 'paste key'" />
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
              <div class="model-desc">executive, aggregator, classifier, direct responses</div>
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
                    <option v-for="m in reasoningModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
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
                    <option v-for="m in executorModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
                  </select>
                </div>
              </div>
            </div>

            <!-- Router Model (chat-vs-investigate classifier) -->
            <div class="model-section">
              <div class="model-label">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="12" r="3"/><path d="M9 6h6a3 3 0 0 1 3 3M9 18h6a3 3 0 0 0 3-3"/></svg>
                router
              </div>
              <div class="model-desc">decides chat vs. agent each turn — a small, reliable non-thinking tool-caller (e.g. GPT-4.1 Mini) routes best. Only tool-call-capable models are listed.</div>
              <div class="form-row">
                <div class="form-group">
                  <label>provider</label>
                  <select v-model="routeProvider" @change="onRouteProviderChange">
                    <option value="">same as executor</option>
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="ollama">Ollama</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>model</label>
                  <select v-model="cfg.agent.route_model" @change="patchConfig">
                    <option value="">same as executor</option>
                    <option v-for="m in routeModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
                  </select>
                </div>
              </div>
            </div>

            <!-- Answer Model (the final answer the user hears) -->
            <div class="model-section">
              <div class="model-label">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
                answer
              </div>
              <div class="model-desc">writes the final answer (the aggregator). Open-ended generation, so a thinking model is fine here — unlike the tool-calling lanes. Empty ⇒ same as reasoning.</div>
              <div class="form-row">
                <div class="form-group">
                  <label>provider</label>
                  <select v-model="answerProvider" @change="onAnswerProviderChange">
                    <option value="">same as reasoning</option>
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="ollama">Ollama</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>model</label>
                  <select v-model="cfg.agent.answer_model" @change="patchConfig">
                    <option value="">same as reasoning</option>
                    <option v-for="m in answerModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
                  </select>
                </div>
              </div>
            </div>

            <!-- Vision Model -->
            <div class="model-section">
              <div class="model-label">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/></svg>
                vision
              </div>
              <div class="model-desc">answers questions about attached images (direct, bypasses tools)</div>
              <div class="form-row">
                <div class="form-group">
                  <label>provider</label>
                  <select v-model="visionProvider" @change="onVisionProviderChange">
                    <option value="">none (disabled)</option>
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="ollama">Ollama</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>model</label>
                  <select v-model="cfg.vision.model" @change="patchConfig">
                    <option value="">none</option>
                    <option v-for="m in visionModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
                  </select>
                </div>
              </div>
            </div>

            <!-- Chat Model -->
            <div class="model-section">
              <div class="model-label">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
                chat
              </div>
              <div class="model-desc">the default lane. Answers directly, uses the tools you enable below, and hands multi-step work to the agent automatically.</div>
              <div class="form-row">
                <div class="form-group">
                  <label>provider</label>
                  <select v-model="chatProvider" @change="onChatProviderChange">
                    <option value="">same as reasoning</option>
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="ollama">Ollama</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>model</label>
                  <select v-model="cfg.chat.model" @change="onChatModelChange">
                    <option value="">same as reasoning</option>
                    <optgroup v-if="unrestrictedModels.length" label="Chat / Unrestricted">
                      <option v-for="m in unrestrictedModels" :key="'u-' + m.id" :value="m.id">{{ modelLabel(m) }}</option>
                    </optgroup>
                    <optgroup :label="(chatProvider || 'openrouter') + ' models'">
                      <option v-for="m in chatModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
                    </optgroup>
                  </select>
                </div>
              </div>
              <div class="form-group">
                <label>tools chat can use</label>
                <div class="model-desc">the palette the agent uses when chat hands off multi-step work (it escalates automatically). <strong>web_fetch</strong> covers quick lookups; others are advanced.</div>
                <div class="tool-picker">
                  <label v-for="t in availableTools" :key="t.name" class="tool-chk" :title="t.description">
                    <input type="checkbox" :checked="chatToolsHas(t.name)" @change="toggleChatTool(t.name)" />
                    <span class="tool-name">{{ t.name }}</span>
                  </label>
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
              <label>executive mode</label>
              <select v-model="cfg.agent.executive_mode" @change="patchConfig">
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
const cfg = ref({ llm: { provider: '', model: '', endpoint: '' }, executor: { provider: '', model: '' }, vision: { provider: '', model: '' }, chat: { provider: '', model: '' }, agent: { dag_mode: '', executive_mode: 'structured', safety_level: 1, route_provider: '', route_model: '', answer_provider: '', answer_model: '' } })
const allModels = ref([])
const apiKey = ref('')
const execProvider = ref('')
const visionProvider = ref('')
const chatProvider = ref('')
const routeProvider = ref('')
const answerProvider = ref('')
const availableTools = ref([])

/** desc: is a tool in the chat allowlist? */
function chatToolsHas(name) {
  return Array.isArray(cfg.value.chat.tools) && cfg.value.chat.tools.includes(name)
}
/** desc: toggle a tool in the chat allowlist and persist. */
function toggleChatTool(name) {
  if (!Array.isArray(cfg.value.chat.tools)) cfg.value.chat.tools = []
  const i = cfg.value.chat.tools.indexOf(name)
  if (i >= 0) cfg.value.chat.tools.splice(i, 1)
  else cfg.value.chat.tools.push(name)
  patchConfig()
}
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
  // Reasoning lane drives the planner (forced plan() tool call) → must call tools.
  return allModels.value.filter(m => m.provider === p && m.tools)
})

/**
 * desc: Filter the full model list to only those matching the executor provider (or reasoning provider as fallback)
 * @returns {Array<Object>} Models available for the selected executor provider
 */
const executorModels = computed(() => {
  const p = execProvider.value || cfg.value.llm.provider
  // Executor lane runs the small forced tool-call classifiers (preflight/reflect/
  // observer). Only tool_call_ok models survive the tight budget — a thinking model
  // here starves and returns nothing (the flat-DAG bug). See router-model-bench.
  return allModels.value.filter(m => m.provider === p && m.tool_call_ok)
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
 * desc: Vision-capable models for the selected vision provider.
 * @returns {Array<Object>} Models with vision=true for that provider
 */
const visionModels = computed(() => {
  const p = visionProvider.value
  return allModels.value.filter(m => m.provider === p && m.vision)
})

/**
 * desc: Handle vision provider change — pick the first vision model, or clear.
 * @returns {void}
 */
function onVisionProviderChange() {
  cfg.value.vision.provider = visionProvider.value
  const available = visionModels.value
  cfg.value.vision.model = available.length ? available[0].id : ''
  patchConfig()
}

/**
 * desc: Models for the selected chat provider. The chat lane is tool-less, so ANY
 *   model works there — show the full provider list (like the executor picker),
 *   with the chat-flagged tunes (RP / uncensored / conversation) sorted to the top
 *   for relevance.
 * @returns {Array<Object>}
 */
const chatModels = computed(() => {
  const p = chatProvider.value || cfg.value.llm.provider
  return allModels.value
    .filter(m => m.provider === p && m.family !== 'unrestricted')
    .sort((a, b) => (b.chat ? 1 : 0) - (a.chat ? 1 : 0))
})

/**
 * desc: Handle chat provider change — pick the first model, or "same as reasoning".
 * @returns {void}
 */
function onChatProviderChange() {
  cfg.value.chat.provider = chatProvider.value
  cfg.value.chat.model = chatProvider.value && chatModels.value.length ? chatModels.value[0].id : ''
  patchConfig()
}

/**
 * desc: Every WORKING (available) unrestricted chat model, across all providers —
 * surfaced as one group in the Direct (chat) lane picker so switching to Direct puts
 * them a click away with no provider-hunting. Biggest first.
 * @returns {Array<Object>}
 */
const unrestrictedModels = computed(() =>
  allModels.value
    .filter(m => m.family === 'unrestricted' && m.available !== false)
    .sort((a, b) => (parseInt(b.params) || 0) - (parseInt(a.params) || 0))
)

/**
 * desc: Pick a chat-lane model AND set its provider from the model itself, so a
 * chat model chosen from the grouped list routes to the right provider.
 * @returns {void}
 */
function onChatModelChange() {
  const m = allModels.value.find(x => x.id === cfg.value.chat.model)
  chatProvider.value = m ? m.provider : ''
  cfg.value.chat.provider = m ? m.provider : ''
  patchConfig()
}

/**
 * desc: Models for the selected router provider (the chat-vs-investigate classifier).
 *   Empty provider ⇒ falls back to the executor lane.
 * @returns {Array<Object>}
 */
const routeModels = computed(() => {
  const p = routeProvider.value || execProvider.value || cfg.value.llm.provider
  // Router = a 16-token forced tool call → only tool_call_ok models. A thinking
  // model here emits no tool call and silently falls back to "chat".
  return allModels.value.filter(m => m.provider === p && m.tool_call_ok)
})

/**
 * desc: Legible dropdown label — name plus params / thinking / tool-call badges so
 *   the choice isn't a bare slug. "tools✓" means bench-verified tool-call-ok.
 * @returns {string}
 */
function modelLabel(m) {
  const t = []
  if (m.params && m.params !== '?') t.push(m.params)
  if (m.thinking) t.push('thinking')
  if (m.tool_call_ok) t.push(m.verified ? 'tools✓' : 'tools')
  return t.length ? `${m.name} · ${t.join(' · ')}` : m.name
}
function onRouteProviderChange() {
  cfg.value.agent.route_provider = routeProvider.value
  cfg.value.agent.route_model = routeProvider.value && routeModels.value.length ? routeModels.value[0].id : ''
  patchConfig()
}

/**
 * desc: Models for the selected answer provider. The answer lane writes the final
 *   answer (aggregator) — open-ended generation, no tool call — so ANY model works
 *   here, including a thinking model. Empty provider ⇒ the reasoning provider.
 * @returns {Array<Object>}
 */
const answerModels = computed(() => {
  const p = answerProvider.value || cfg.value.llm.provider
  return allModels.value.filter(m => m.provider === p)
})
function onAnswerProviderChange() {
  cfg.value.agent.answer_provider = answerProvider.value
  cfg.value.agent.answer_model = answerProvider.value && answerModels.value.length ? answerModels.value[0].id : ''
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
      vision: { provider: cfg.value.vision.provider, model: cfg.value.vision.model },
      chat: { provider: cfg.value.chat.provider, model: cfg.value.chat.model, tools: cfg.value.chat.tools || [] },
      agent: {
        dag_mode: cfg.value.agent.dag_mode,
        safety_level: cfg.value.agent.safety_level,
        route_provider: cfg.value.agent.route_provider,
        route_model: cfg.value.agent.route_model,
        answer_provider: cfg.value.agent.answer_provider,
        answer_model: cfg.value.agent.answer_model,
      },
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
    if (!cfg.value.vision) cfg.value.vision = { provider: '', model: '' }
    if (!cfg.value.chat) cfg.value.chat = { provider: '', model: '' }
    if (!cfg.value.agent) cfg.value.agent = {}
    if (!Array.isArray(cfg.value.chat.tools)) cfg.value.chat.tools = []
    try { availableTools.value = await api.get('/api/v1/tools') } catch {}
    execProvider.value = cfg.value.executor.provider || ''
    visionProvider.value = cfg.value.vision.provider || ''
    chatProvider.value = cfg.value.chat.provider || ''
    routeProvider.value = cfg.value.agent.route_provider || ''
    answerProvider.value = cfg.value.agent.answer_provider || ''
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
.tool-picker { display: flex; flex-wrap: wrap; gap: 6px 14px; }
.tool-chk { display: flex; align-items: center; gap: 6px; font-size: 12px; cursor: pointer; user-select: none; }
.tool-chk input { cursor: pointer; }
.tool-chk .tool-name { font-family: var(--font-mono, monospace); }
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
