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

- **This is about PLUGINS, not system services.** A request to "enable web
  reading / read these JS pages / turn on the crawler / enable the reader / enable
  this capability" is a plugin question — use `plugin_list` / `plugin_enable` /
  `plugin_option`. Do NOT reach for the `service` tool (that manages OS daemons
  like nginx/redis) and do NOT ask "which service" — the user means a plugin.
- On "what can you do / are there plugins / can you do X?" → call `plugin_list`
  and answer from it. Name what's active, and mention anything **available** you
  could switch on.
- **Propose, then enable.** If a capability the user wants is **available (off)**,
  OFFER it and ask them to confirm — e.g. "I can do that if I enable the
  `webreader` plugin — want me to?" Enable only after they say yes, or if they
  asked to enable it outright.
- To enable, call `plugin_enable` with the `name` from `plugin_list`. Its tools
  become available immediately — use them on the next step.
- **Remote plugins need a host URL.** Webreader runs in an out-of-process host
  reached by the `remote` bridge. If `plugin_enable name="remote"` reports the
  host is unreachable / added no tools, set the URL first with
  `plugin_option {name:"remote", key:"host", value:"http://127.0.0.1:8091"}`
  (ask the user for the URL if you don't know it), then enable.
- **A reader plugin wires itself into `web_fetch`.** Once `webreader` is enabled,
  you do NOT call a separate tool to use it — `web_fetch` reads every page through
  it automatically (JS/SPA pages included). Just fetch as usual.
- If `plugin_enable` reports the plugin isn't built into this binary, tell the
  user it needs an operator to rebuild with that plugin — you can't add it.

## RULES

- Never enable a plugin on your own initiative — only when the user explicitly
  asks, or confirms an offer you made.
- Don't claim a capability you don't have. Check `plugin_list` first; if it isn't
  active and isn't available, say plainly it isn't in this build.
- `plugin_enable` grants you new capabilities — treat it as a deliberate,
  user-approved action, not a routine step. (If it isn't offered at all, the
  operator has disabled runtime activation; you can still explain how to enable a
  plugin via config.)
