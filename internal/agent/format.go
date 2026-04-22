package agent

// FormatRule returns the channel-aware prompt fragment describing what the
// target rendering surface can display. Insert it into any system prompt that
// produces user-facing FREE text (aggregator, reflector verdicts, chat-mode
// direct replies). The CLI channel has no markdown renderer — fenced code
// blocks and tables land as literal characters in the terminal — so we tell
// the model to emit plain text instead.
//
// The channel-capability read currently keys off cfg.CLIMode. If Kaiju ever
// grows richer channel metadata (html? tui?), widen here.
func (a *Agent) FormatRule() string {
	if a.cfg.CLIMode {
		return "Plain text only — this channel renders raw output in a terminal.\n" +
			"- NO fenced code blocks (no triple-backticks).\n" +
			"- NO markdown tables.\n" +
			"- Use single `backticks` sparingly for inline code references.\n" +
			"- Bullet lists and paragraphs are fine; short error quotes go on their own indented line."
	}
	return "Markdown is rendered — fenced code blocks, tables, and bullet lists all display correctly."
}
