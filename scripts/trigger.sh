#!/usr/bin/env bash
# Hits one scenario on a deployed test app, N times.
#
#   scripts/trigger.sh <base-url> <scenario> [count]
#   scripts/trigger.sh https://otel-go.example.com exception 20
#
# Scenarios (every language serves the same ones):
#   ok          200 with nested child spans
#   slow        a 1.5s span (try the "> 1 s" filter)
#   exception   the same exception every time → one group, count grows
#   not-found   exception with a different message per id → still one group
#   status-500  500 with no exception → a status-only group named after the route
#   crash       Go panic / rejected promise / ZeroDivisionError
#   downstream  calls DOWNSTREAM_URL/exception → one trace across two apps, origin marked
#   checkout    frontend error that breaks orders and payments (run against the JavaScript app)
#   checkout-ok the same flow with clean prices, for comparison
#   dashboard   a broken service three hops down that takes the frontend with it (JavaScript app)
#   dashboard-ok the same path with every service healthy
set -euo pipefail

base="${1:?usage: trigger.sh <base-url> <scenario> [count]}"
scenario="${2:?usage: trigger.sh <base-url> <scenario> [count]}"
count="${3:-1}"
base="${base%/}"

for i in $(seq 1 "$count"); do
  case "$scenario" in
    ok) path="/ok" ;;
    slow) path="/slow?ms=1500" ;;
    exception) path="/exception" ;;
    not-found) path="/not-found/$RANDOM" ;;
    status-500) path="/status-500" ;;
    crash) path="/crash" ;;
    downstream) path="/downstream?path=/exception" ;;
    checkout) path="/checkout" ;;
    checkout-ok) path="/checkout?healthy=1" ;;
    dashboard) path="/dashboard" ;;
    dashboard-ok) path="/dashboard?healthy=1" ;;
    *) echo "unknown scenario: $scenario" >&2; exit 2 ;;
  esac
  # A panic or crash can drop the connection: curl still prints 000 but exits non-zero, which
  # set -e would turn into skipping the remaining requests.
  status=$(curl -sk -o /dev/null -w '%{http_code}' "$base$path" || true)
  printf '%3s/%-3s %s %s\n' "$i" "$count" "$status" "$path"
done
