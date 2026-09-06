package configapi

import (
	"net/http"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/internal/plugins"
	"github.com/Compdeep/kaiju/models/openrouter"
)

// What this build can be told to do, and the config key that tells it.
//
// A caller configuring this node over the API can read every setting's current
// value from GET /api/v1/config, and cannot learn from it what a setting will
// ACCEPT — or whether a setting it wants exists here at all. An older build
// answers a request for an unknown key by ignoring it, so a client that guesses
// gets a success and no effect.
//
// So the accepted values are published, and published from the same functions
// the endpoints validate against. A listing kept by hand beside the code it
// describes is a listing that is wrong within a release: this one cannot say
// "on" and "off" while the parser also takes "enabled", because it asks the
// parser.

// Where a setting is accepted.
const (
	whereConfig  = "config"
	whereRequest = "request"
)

// Setting is one thing a caller may set, and what it takes.
type Setting struct {
	// Key is the name as the caller writes it: a dotted path into the config
	// document for a saved setting, or a field name for a request one.
	Key string `json:"key"`
	// Where says which surface accepts it — "config" for PATCH /api/v1/config,
	// "request" for a field on POST /api/v1/execute.
	//
	// Both are listed because a caller has to configure both, and only one of
	// them is readable anywhere else: the config document can be fetched and its
	// current values inspected, while a request field appears nowhere until it
	// is sent. Publishing the saved settings alone described half the surface
	// and left the more discoverable half as the documented one.
	Where string `json:"where"`
	// Values are the accepted values. Empty means the setting is free-form —
	// a model id, an endpoint — and this listing does not enumerate it.
	Values []string `json:"values,omitempty"`
	// EmptyMeans says what the empty string does, when it is accepted. A caller
	// clearing a setting needs to know whether that is a valid state or a
	// rejected one, and for these it is valid and is the default.
	EmptyMeans string `json:"empty_means,omitempty"`
	// Applies says where the setting takes effect, because two of these look
	// general and are not: the reasoning switch is one lane's, and the other
	// lanes decide it themselves.
	Applies string `json:"applies"`
}

// Capabilities is the whole answer.
type Capabilities struct {
	// Settings are the enumerable config keys and their accepted values.
	Settings []Setting `json:"settings"`
	// Plugins compiled into this binary, and the ones switched on. Compiled but
	// not active is the ordinary case: a plugin is code that is present and not
	// asked for, and a caller enabling one needs to know which of those it is.
	PluginsCompiled []string `json:"plugins_compiled"`
	PluginsActive   []string `json:"plugins_active"`
	// ProviderRouting reports whether this build steers OpenRouter away from
	// particular hosts. The lists themselves are not published: they are this
	// deployment's operational judgement about third parties, and a caller needs
	// to know the mechanism is on, not who is on it.
	ProviderRoutingBlocks int `json:"provider_routing_blocks"`
	ProviderRoutingAllows int `json:"provider_routing_allows"`
}

/*
 * handleCapabilities reports what this build accepts.
 * desc: Built from the parsers the write endpoints use, so it cannot describe a
 *       setting differently from the code that enforces it. Read-only, and safe
 *       to call before anything is configured.
 * param: w - HTTP response writer.
 */
func (c *API) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	out := Capabilities{
		Settings: []Setting{
			{
				Key:        "agent.execution_mode",
				Where:      whereConfig,
				Values:     agent.ExecutionModes(),
				EmptyMeans: "interactive",
				Applies:    "every turn this node runs, unless the request overrides it",
			},
			{
				Key:        "llm.reasoning",
				Where:      whereConfig,
				Values:     agent.ReasoningModes(),
				EmptyMeans: "whatever the chosen model does unasked",
				Applies: "the reasoning lane only — the router and the executor send " +
					"reasoning off whatever is configured, and answer and chat take the model's default",
			},
			{
				Key:        "agent.dag_mode",
				Where:      whereConfig,
				Values:     agent.DAGModes(),
				EmptyMeans: "orchestrator",
				Applies:    "the shape of the graph a run builds",
			},
			{
				Key:        "execution_mode",
				Where:      whereRequest,
				Values:     agent.ExecutionModes(),
				EmptyMeans: "whatever agent.execution_mode is set to",
				Applies:    "this one turn, overriding the node's setting",
			},
			{
				Key:        "mode",
				Where:      whereRequest,
				Values:     agent.DAGModes(),
				EmptyMeans: "whatever agent.dag_mode is set to",
				Applies:    "this one turn, overriding the node's setting",
			},
			{
				Key:   "chat_mode",
				Where: whereRequest,
				// No values: it is a boolean, and the JSON type says so. What a
				// caller cannot guess is which way the default falls, and that
				// is below.
				EmptyMeans: "false — the turn goes to the agent, not the chat lane",
				Applies: "this one turn: true answers it directly with no planner and no " +
					"tools, and the node's execution mode does not apply to it",
			},
		},
		PluginsCompiled:       plugins.Compiled(),
		PluginsActive:         activePlugins(),
		ProviderRoutingBlocks: len(openrouter.Blocked()),
		ProviderRoutingAllows: len(openrouter.Allowed()),
	}
	jsonResponse(w, out, http.StatusOK)
}

// activePlugins is the compiled list narrowed to the ones switched on.
func activePlugins() []string {
	all := plugins.Compiled()
	on := make([]string, 0, len(all))
	for _, n := range all {
		if plugins.IsActive(n) {
			on = append(on, n)
		}
	}
	return on
}
