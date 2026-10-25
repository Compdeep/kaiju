//go:build windows

package cli

// The console has no OPOST. term.MakeRaw on Windows changes the input mode and
// leaves ENABLE_PROCESSED_OUTPUT alone, so the newline handling OPOST restores
// on a terminal is already in force here and there is nothing to re-enable.
func enableOPOST(int) error { return nil }
