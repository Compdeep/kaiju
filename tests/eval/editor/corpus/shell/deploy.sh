#!/usr/bin/env bash
set -euo pipefail

APP_DIR="/srv/app"
RELEASES_KEPT=5
HEALTH_URL="http://127.0.0.1:3000/health"
TIMEOUT=30

log() { printf '[%s] %s\n' "$(date -Is)" "$*"; }

prune_releases() {
  ls -1dt "${APP_DIR}/releases"/* | tail -n +$((RELEASES_KEPT + 1)) | xargs -r rm -rf
}

wait_for_health() {
  local waited=0
  until curl -fsS "$HEALTH_URL" >/dev/null; do
    sleep 1
    waited=$((waited + 1))
    if [ "$waited" -ge "$TIMEOUT" ]; then
      log "health check failed after ${TIMEOUT}s"
      return 1
    fi
  done
}

main() {
  log "deploying"
  prune_releases
  systemctl restart app.service
  wait_for_health
  log "done"
}

main "$@"
