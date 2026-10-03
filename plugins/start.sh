#!/usr/bin/env bash
# Start the kaiju plugin host on $1 (default 8092), with $2 as the agent workspace
# (default: the directory this was launched from, which is what the service manager
# sets). Idempotent: creates the venv and installs base deps on first run, then
# execs uvicorn.
#
# The venv does NOT live beside this script. It was created here, which put 269 MB
# of runtime into the source tree — gitignored, but still a checkout you cannot
# copy, archive or read the size of. It goes under the user's data directory, and
# KAIJU_PLUGIN_VENV overrides that.
#
# KAIJU_WORKSPACE is exported because the remote plugin protocol does not carry a
# workspace, so a plugin that touches files enforces its own sandbox against this.
# Unset, such a plugin refuses every path, which is the safe direction.
set -e

PORT="${1:-8092}"
WORKSPACE="${2:-$PWD}"
HERE="$(cd "$(dirname "$0")" && pwd)"

if [ -n "$KAIJU_PLUGIN_VENV" ]; then
  VENV="$KAIJU_PLUGIN_VENV"
else
  DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
  VENV="$DATA/kaiju/plugin-host/venv"
fi

if [ ! -x "$VENV/bin/uvicorn" ]; then
  mkdir -p "$(dirname "$VENV")"
  python3 -m venv "$VENV"
  "$VENV/bin/pip" install -q -r "$HERE/requirements.txt"
fi

# Each plugin's own base tier, into the same venv. The host installed only its own
# requirements, so a plugin that declared dependencies had them declared and never
# installed — it loaded, advertised its tools, and every call answered "needs its
# base tier". Stamped by mtime so a start with nothing new to do does no work;
# heavier tiers (requirements-pdf.txt, requirements-cv.txt) stay opt-in and are
# installed by hand or by the agent.
STAMPS="${VENV%/venv}/stamps"
mkdir -p "$STAMPS"
for req in "$HERE"/*/requirements.txt; do
  [ -f "$req" ] || continue
  name="$(basename "$(dirname "$req")")"
  stamp="$STAMPS/$name"
  if [ ! -f "$stamp" ] || [ "$req" -nt "$stamp" ]; then
    echo "[plugin-host] installing $name base tier"
    "$VENV/bin/pip" install -q -r "$req" && touch "$stamp"
  fi
done

# Run from the host's directory so `host:app` imports, and keep bytecode out of the
# source tree for the same reason the venv is not here.
cd "$HERE"
export KAIJU_WORKSPACE="$WORKSPACE"
# KAIJU_PLUGINS, if the caller set it, names which plugins to load. Unset, the
# host loads every folder it finds — see load_plugins in registry.py.
export KAIJU_PLUGINS="${KAIJU_PLUGINS:-}"
export PYTHONPYCACHEPREFIX="${VENV%/venv}/pycache"
exec "$VENV/bin/uvicorn" host:app --host 127.0.0.1 --port "$PORT"
