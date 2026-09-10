/**
 * Which reasoning controls there is any point offering, and for what.
 *
 * The engine asks for an effort or a thinking budget only where models.json
 * says the model acts on it (agent.applyReasoningBudget). Every provider
 * accepts both parameters and none errors on either, so a picker that offers a
 * value no configured model acts on is a control that appears to work: the
 * setting saves, the file shows it, and nothing changes.
 *
 * The rule lives here rather than in each picker because there are two of them
 * — the chat header and the settings modal — and this is exactly how
 * fits_small_call came to be re-derived in four places and disagree with the
 * daemon for a day.
 */

// Every config field that names a model kaiju will send to. A lane left empty
// falls back to the reasoning model, which is already in the list, so reading
// the empty ones costs nothing and missing a configured one would hide a
// control that does work.
const LANE_MODEL_FIELDS = [
  c => c?.llm?.model,
  c => c?.executor?.model,
  c => c?.chat?.model,
  c => c?.vision?.model,
  c => c?.agent?.route_model,
  c => c?.agent?.answer_model,
]

// Offered in this order whatever order the catalog lists them in, so the control
// reads as a scale rather than as whatever came back first. Six rather than the
// usual three because the vocabulary is the providers' and not ours, and no
// model takes all of it — glm-5.2 takes xhigh and high and neither low nor
// medium. Mirrors agent.ReasoningEfforts().
const EFFORT_ORDER = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']

/**
 * desc: The catalog entries for the models this config actually sends to.
 * @param {Object} cfg - the config document from GET /api/v1/config
 * @param {Array<Object>} models - the catalog from GET /api/v1/models
 * @returns {Array<Object>} one entry per distinct configured model, unknown ids dropped
 */
function laneModels(cfg, models) {
  if (!cfg || !Array.isArray(models)) return []
  const ids = new Set(LANE_MODEL_FIELDS.map(f => f(cfg)).filter(Boolean))
  return [...ids].map(id => models.find(m => m.id === id)).filter(Boolean)
}

/**
 * desc: The effort values worth offering — the union across the configured lane
 *       models, because the setting is one value for all of them and the
 *       catalog decides per model at send time whether it travels. Empty means
 *       offer nothing: no configured model has been measured to act on an
 *       effort, so every value would be inert.
 * @param {Object} cfg - the config document
 * @param {Array<Object>} models - the catalog
 * @returns {Array<string>} accepted efforts, weakest first
 */
export function effortOptions(cfg, models) {
  const seen = new Set()
  for (const m of laneModels(cfg, models)) {
    for (const e of m.reasoning_efforts || []) seen.add(e)
  }
  const known = EFFORT_ORDER.filter(e => seen.has(e))
  const rest = [...seen].filter(e => !EFFORT_ORDER.includes(e))
  return [...known, ...rest]
}

/**
 * desc: Whether a thinking budget in tokens is honoured as a budget by any
 *       configured model. Today that means Anthropic and its native
 *       budget_tokens; the models measured on the OpenAI-compatible wire spent
 *       648 and 1,160 tokens when asked for 512, so a number typed against one
 *       of those would bound nothing.
 * @param {Object} cfg - the config document
 * @param {Array<Object>} models - the catalog
 * @returns {boolean}
 */
export function budgetHonoured(cfg, models) {
  return laneModels(cfg, models).some(m => m.reasoning_budget)
}
