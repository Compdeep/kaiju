package tools

// PluginConfig is what the plugin tools need from the application's
// configuration.
//
// An interface rather than kaiju's own config type, so an application
// embedding this engine can enable a plugin without adopting kaiju's
// configuration file. It is six methods because that is what the tools use:
// which plugins are on, where a remote host is, how to start one, where the
// workspace is, and a way to persist the first two.
//
// Persisting is the application's business — it decides whether a plugin
// enabled during a run is still enabled after a restart, and where that is
// written. Returning an error is how it says the change could not be kept.
type PluginConfig interface {
	// PluginNames are the plugins currently marked active.
	PluginNames() []string
	// SetPluginNames records a change, persisting it if the application does.
	SetPluginNames(names []string) error

	// PluginHost is the base URL of an out-of-process plugin host, empty when
	// there is none.
	PluginHost() string
	// SetPluginHost records the host a plugin was found at.
	SetPluginHost(url string) error
	// PluginHostStart is the command that launches that host, empty for the
	// default.
	PluginHostStart() string

	// PluginWorkspace is where a plugin host runs and writes.
	PluginWorkspace() string
}
