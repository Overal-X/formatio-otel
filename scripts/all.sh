#!/usr/bin/env bash
# Runs every scenario against one app, a few times each, so both Tools pages fill up.
#
#   scripts/all.sh <base-url> [count]
set -euo pipefail

base="${1:?usage: all.sh <base-url> [count]}"
count="${2:-3}"
here="$(cd "$(dirname "$0")" && pwd)"

for scenario in ok slow exception not-found status-500 crash downstream; do
  echo "== $scenario"
  "$here/trigger.sh" "$base" "$scenario" "$count" || true
done
echo
echo "Expect in Tools → Errors: one group each for exception, not-found, status-500 and crash,"
echo "plus the downstream app's exception. Tools → Traces → errors only shows the failing requests."
