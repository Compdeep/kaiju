//go:build plugin_pdf

package main

// Linking this file (only under `-tags plugin_pdf`) runs the pdf package's init(),
// which self-registers the "pdf" plugin so `plugins: ["pdf"]` / `--plugins pdf`
// can switch it on. Without the tag, neither the plugin nor its PDF-parsing
// dependency is compiled into the binary.
import _ "github.com/Compdeep/kaiju/internal/plugins/pdf"
