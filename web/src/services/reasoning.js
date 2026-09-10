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

// How long one round is allowed to take at each effort, in seconds, before a
// model measured slow lengthens it.
//
// Mirrors effortBudget and minRoundBudget in agent/round_budget.go, which is
// the authority. A Go test reads this file and fails when the two disagree, so
// the numbers a person is shown cannot drift from the clock that cuts the call
// off — the fault fits_small_call caused, in two languages, for a day.
//
// "" is the ordinary setting. The weak provider values sit at the same two
// minutes as "" on purpose: asking a model to think less is a different thing
// from giving the call less time. "fast" is the one value that asks for the
// second, and the only one below that floor.
export const EFFORT_SECONDS = {
  fast: 60,
  '': 120,
  minimal: 120,
  low: 120,
  medium: 120,
  high: 240,
  xhigh: 480,
  max: 960,
}

/**
 * desc: The rows an effort picker should offer, in order, each with the time it
 *       allows. "fast" and "normal" are always among them: they set a deadline
 *       this engine enforces itself, on every model, measured or not. The rest
 *       are the provider values the configured models were measured to act on.
 * @param {Object} cfg - the config document
 * @param {Array<Object>} models - the catalog
 * @returns {Array<Object>} {value, label, seconds} rows, weakest first
 */
export function effortRows(cfg, models) {
  const measured = effortOptions(cfg, models).filter(v => v !== 'fast' && v !== '')
  return [
    { value: 'fast', label: 'fast' },
    { value: '', label: 'normal' },
    ...measured.map(v => ({ value: v, label: v })),
  ].map(r => ({ ...r, seconds: EFFORT_SECONDS[r.value] }))
}

/**
 * desc: A row's allowance as a person reads it — "1 min", "16 min". Empty for a
 *       value with no entry, so an effort added to the engine and not to the
 *       table above shows no time rather than a wrong one.
 * @param {string} value - the effort, "" for the ordinary setting
 * @returns {string} the time, or ""
 */
export function effortTime(value) {
  const secs = EFFORT_SECONDS[value]
  if (!secs) return ''
  return secs < 120 ? `${secs} sec` : `${Math.round(secs / 60)} min`
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
