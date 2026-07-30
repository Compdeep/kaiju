//go:build plugin_remote

package main

// Linking this file (only under `-tags plugin_remote`) runs the remote package's
// init(), which self-registers the "remote" plugin bridge so `plugins: ["remote"]`
// / `--plugins remote` can switch it on. Once active it fetches an out-of-process
// plugin host's manifest (KAIJU_PLUGIN_HOST) and turns each advertised tool into a
// native kaiju tool. Without the tag, none of it is compiled in.
import _ "github.com/Compdeep/kaiju/internal/plugins/remote"
