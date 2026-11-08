#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'Repository privacy' 180 python3 scripts/privacy_check.py
if [ -n "${CI_BASE_SHA:-}" ]; then
  ci_run 'Commit and changed-file privacy' 180 python3 scripts/privacy_check.py --range "${CI_BASE_SHA}..HEAD"
fi
# CI_BASE_SHA when the workflow set it, otherwise the clone's default branch. A migration already on that base
# cannot be edited, deleted or renamed (comment- and whitespace-only edits pass).
ci_run 'Frozen migrations' 60 python3 scripts/ci/migration_freeze.py
# Fixed time.Sleep calls in Go tests may stay or fall, never rise. A // sleep-ok: <reason> line does not count.
ci_run 'Fixed sleeps' 60 python3 scripts/ci/sleep_ratchet.py
ci_run 'Case-sensitive paths and external references' 180 bash -c 'python3 scripts/test/test_path_case.py && python3 scripts/test/test_external_service_refs.py'
