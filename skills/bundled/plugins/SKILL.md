---
name: "Plugins"
description: "Discover the optional plugins/capabilities this kaiju has, and — only when the user explicitly asks — switch one on."
---

## Core Role

When the user asks what you can do, whether any plugins are available, or whether
you can do something that might need an optional capability (reading web pages,
PDFs, etc.), check with the `plugin_list` tool rather than guessing. It reports
each optional plugin as:

- **active** — its tools are live, use them now;
- **available** — built into this binary but switched off; you can enable it;
- and anything not listed at all isn't in this build (adding it needs an operator
  rebuild — you cannot do that yourself).

## Planning Guidance

- On "what can you do / are there plugins / can you do X?" → call `plugin_list`
  and answer from it. Name what's active, and mention anything **available** you
  could switch on.
- **Propose, then enable.** If a capability the user wants is **available (off)**,
  OFFER it and ask them to confirm — e.g. "I can do that if I enable the
  `webreader` plugin — want me to?" Enable only after they say yes, or if they
  asked to enable it outright.
- To enable, call `plugin_enable` with the `name` from `plugin_list`. Its tools
  become available immediately — use them on the next step.
- If `plugin_enable` reports the plugin isn't built into this binary, tell the
  user it needs an operator to rebuild with that plugin — you can't add it.
- If enabling a remote plugin adds no tools, its host is probably not running —
  say that rather than claiming the capability.

## RULES

- Never enable a plugin on your own initiative — only when the user explicitly
  asks, or confirms an offer you made.
- Don't claim a capability you don't have. Check `plugin_list` first; if it isn't
  active and isn't available, say plainly it isn't in this build.
- `plugin_enable` grants you new capabilities — treat it as a deliberate,
  user-approved action, not a routine step. (If it isn't offered at all, the
  operator has disabled runtime activation; you can still explain how to enable a
  plugin via config.)
