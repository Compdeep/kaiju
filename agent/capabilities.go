package agent

import "github.com/Compdeep/kaiju/agent/llm"

// Applying the optional capabilities an application supplies.
//
// Each of these was once a Set* method the caller had to know to call after
// New. Doing it here means an agent is fully formed when New returns, and
// Config is the single place to look for what an agent was given.
//
// Everything is optional. An absent capability leaves its feature off rather
// than broken: no executor and targets run locally, no store and nothing is
// recorded, no environment and prompts simply carry no description of the
// surroundings.

/*
 * applyCapabilities wires the optional pieces from Config onto the agent.
 * desc: Called by New. Order matters only in that the executor falls back to
 *       the main model's settings, so the main model is resolved first.
 * param: cfg - the configuration New was given.
 */
func (a *Agent) applyCapabilities(cfg Config) {
	a.applyModels(cfg)

	if len(cfg.ChatTools) > 0 {
		a.chatTools = cfg.ChatTools
	}
	if cfg.Clearance != nil {
		a.clearanceCheck = cfg.Clearance
	}
	if cfg.Store != nil {
		a.eventStore = cfg.Store
	}
	if cfg.Remote != nil {
		a.remoteExec = cfg.Remote
	}
	if cfg.Validate != nil {
		a.targetValid = cfg.Validate
	}
	if cfg.Environment != nil {
		a.environment = cfg.Environment
	}
}

/*
 * applyModels builds the LLM clients and per-lane model choices.
 * desc: The main model is resolved first because the executor lane falls back
 *       to it field by field — an application can name only a cheaper model and
 *       inherit the endpoint and key.
 * param: cfg - the configuration New was given.
 */
func (a *Agent) applyModels(cfg Config) {
	if cfg.LLMEndpoint != "" || cfg.LLMAPIKey != "" || cfg.LLMModel != "" {
		a.llm = llm.NewClientWithProvider(cfg.LLMProvider, cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel)
	}

	if cfg.ExecutorEndpoint != "" || cfg.ExecutorAPIKey != "" || cfg.ExecutorModel != "" {
		endpoint := firstNonEmpty(cfg.ExecutorEndpoint, cfg.LLMEndpoint)
		apiKey := firstNonEmpty(cfg.ExecutorAPIKey, cfg.LLMAPIKey)
		model := firstNonEmpty(cfg.ExecutorModel, cfg.LLMModel)
		provider := firstNonEmpty(cfg.ExecutorProvider, cfg.LLMProvider)
		a.executor = llm.NewClientWithProvider(provider, endpoint, apiKey, model)
	}

	// Per-lane choices. Leaving a lane empty keeps it on the main model.
	if cfg.VisionProvider != "" || cfg.VisionModel != "" {
		a.visionProvider, a.visionModel = cfg.VisionProvider, cfg.VisionModel
	}
	if cfg.ChatProvider != "" || cfg.ChatModel != "" {
		a.chatProvider, a.chatModel = cfg.ChatProvider, cfg.ChatModel
	}
	if cfg.RouteProvider != "" || cfg.RouteModel != "" {
		a.routeProvider, a.routeModel = cfg.RouteProvider, cfg.RouteModel
	}
	if cfg.AnswerProvider != "" || cfg.AnswerModel != "" {
		a.answerProvider, a.answerModel = cfg.AnswerProvider, cfg.AnswerModel
	}
}

// firstNonEmpty returns the first non-empty string, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
