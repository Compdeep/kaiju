#!/usr/bin/env bash
# Start the kaiju plugin host (webreader et al) on $1 (default 8092). Idempotent:
# creates the venv + installs base deps on first run, then execs uvicorn. Enabling a
# plugin from chat runs this automatically when the host isn't already up — so the
# user never has to start a service by hand.
set -e
cd "$(dirname "$0")"
PORT="${1:-8092}"
if [ ! -x venv/bin/uvicorn ]; then
  python3 -m venv venv
  ./venv/bin/pip install -q -r requirements.txt
fi
exec ./venv/bin/uvicorn host:app --host 127.0.0.1 --port "$PORT"
