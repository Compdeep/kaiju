package tools

// PluginConfig is what the plugin tools need from the application's
// configuration.
//
// An interface rather than kaiju's own config type, so an application embedding
// this engine can install a plugin without adopting kaiju's configuration file.
//
// It is four methods because that is what is used: which plugins this
// installation should have, a way to record a change to that, and where to find
// or start the out-of-process host. It was six. SetPluginHost and
// PluginWorkspace went with the tool that called them — plugin_option, which let
// a planner point the bridge at a URL, and whose job is configuration.
//
// Recording is the application's business: it decides whether a plugin installed
// during a run is still installed after a restart, and where that is written.
// Returning an error is how it says the change could not be kept — which matters
// more than it used to, because an unrecorded install is undone by the next start.
type PluginConfig interface {
	// PluginNames are the plugins this installation should have.
	PluginNames() []string
	// SetPluginNames records a change, persisting it if the application does.
	SetPluginNames(names []string) error

	// PluginHost is the base URL of an out-of-process plugin host, empty when
	// there is none and the default should be used.
	PluginHost() string
	// PluginHostStart is the command that launches that host, empty for the
	// default.
	PluginHostStart() string
}
