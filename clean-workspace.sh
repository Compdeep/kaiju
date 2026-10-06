#!/usr/bin/env bash
# Clear what kaiju leaves in its workspace and in this checkout between runs.
# KAIJU_HOME is the agent's data directory (default ~/.kaiju).
set -euo pipefail
KAIJU_HOME="${KAIJU_HOME:-$HOME/.kaiju}"
REPO="$(cd "$(dirname "$0")" && pwd)"

rm -rf "$KAIJU_HOME/workspace/.services" "$KAIJU_HOME/workspace/.services.json" "$KAIJU_HOME/workspace/.worklog"
rm -rf "$KAIJU_HOME"/workspace/project/* "$KAIJU_HOME"/workspace/sessions/* "$KAIJU_HOME"/workspace/blueprints/*
rm -rf "$REPO/.kaiju/blueprints/"
rm -rf "$REPO"/project/*
