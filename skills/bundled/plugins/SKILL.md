---
name: "Plugins"
description: "Report which plugins this kaiju has and which it could have, and — only when the user asks — install one, which rebuilds kaiju and restarts it."
---

## Core Role

When the user asks what you can do, whether a plugin exists, or whether you can do
something that might need one, call `plugin_list` rather than guessing. It reports
every plugin in the catalogue as:

- **installed** — its tools are live, use them now;
- **not installed** — in the catalogue, so `plugin_install` can build it in.

A name the catalogue does not list cannot be installed here at all, and saying so
is the correct answer.

## Planning Guidance

- **This is about PLUGINS, not system services.** "Can you read JS-heavy pages",
  "turn on the crawler", "can you edit images" are plugin questions — use
  `plugin_list`, then `plugin_install`. Do NOT reach for the `service` tool, which
  manages OS daemons, and do NOT ask "which service".
- On "what can you do / are there plugins / can you do X?" → call `plugin_list` and
  answer from it. Name what is installed, and name anything you could install.
- **Propose, then install.** If the thing the user wants is not installed, say what
  installing it involves and ask. Install without asking only when they asked for
  it outright.
- **Installing rebuilds kaiju and restarts it.** `plugin_install` compiles a new
  binary, proves it boots, replaces the running one and hands over about twenty
  seconds later. Say that before you call it, because the user will see a restart.
  Your answer is delivered first; the handover follows it.
- **It can fail for reasons you cannot fix.** A rebuild needs kaiju's source tree
  and a Go toolchain on the machine. Without them `plugin_install` says so, and
  that is a complete answer — do not look for another route to the same end, and do
  not offer to run build commands through `bash`.
- **One call installs one plugin.** It keeps everything already installed; you do
  not need to name the others.
- **A reader plugin wires itself into `web_fetch`.** Once `webreader` is installed
  you do not call a separate tool — `web_fetch` reads every page through it,
  JavaScript-heavy pages included. Just fetch as usual.
- **Report honestly when a capability is absent.** "I can't do that, and there is
  no plugin here for it" is a finished answer. Never describe what a tool you do
  not have would have returned.

## RULES

1. Never claim a plugin is installed without `plugin_list` saying so.
2. Never install a plugin the user did not ask for, even when it would help.
3. Never present a plugin's absence as a failure of the request.
