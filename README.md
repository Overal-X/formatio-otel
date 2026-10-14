# formatio-otel

Test targets for Formatio's tracing and error tracking (`specs/otel-tracing.md` in the
formatio repo). One small HTTP app per language, all serving the same routes, each route
producing one thing the **Tools → Traces** and **Tools → Errors** pages should show.

Every app uses the stock OpenTelemetry SDK and is configured **only** by the `OTEL_*`
variables Formatio injects. Nothing in here points at an endpoint or holds a token: if
traces arrive, the injection works.

| Folder | Stack | Started by |
| --- | --- | --- |
| `go/` | `net/http` + `otelhttp`, OTLP/HTTP exporter | `main.go` sets up the SDK from env |
| `javascript/` | Express 5 + `@opentelemetry/auto-instrumentations-node` | `node --require …/register` (zero-code) |
| `python/` | Flask + `opentelemetry-distro` | `opentelemetry-instrument` (zero-code) |

## Routes (all three apps)

| Route | What it does | Expect in Formatio |
| --- | --- | --- |
| `/ok` | 200, three nested child spans | A trace with a nested waterfall |
| `/slow?ms=1500` | One long child span | Shows under the `> 1 s` filter |
| `/exception` | The same exception from the same line | **One** group whose count grows |
| `/not-found/:id` | Exception, message varies per id | Still **one** group (same stack) |
| `/status-500` | 500 with no exception recorded | A status-only group named after the route |
| `/crash` | Go panic / rejected promise / `ZeroDivisionError` | One group each |
| `/downstream?path=/exception` | Calls `DOWNSTREAM_URL` + path | **One trace across two apps**, the thrower marked *origin*; the caller gets its own `/downstream` group, the thrower its exception group |
| `/healthz` | 200 | — |

## Deploy on Formatio

Create three applications from this repository in **one project** (so they share its
network), with the **Dockerfile** builder:

| Application name | Dockerfile | Build context |
| --- | --- | --- |
| `otel-go` | `go/Dockerfile` | `go` |
| `otel-javascript` | `javascript/Dockerfile` | `javascript` |
| `otel-python` | `python/Dockerfile` | `python` |

For each one:

1. Add a domain in **Networking** on port **8080** (the apps listen on `$PORT`, default 8080).
2. Add a variable so the apps call each other in a ring — siblings are reachable by their
   application name on the project network:
   - `otel-go`: `DOWNSTREAM_URL=http://otel-javascript:8080`
   - `otel-javascript`: `DOWNSTREAM_URL=http://otel-python:8080`
   - `otel-python`: `DOWNSTREAM_URL=http://otel-go:8080`
3. **Settings → Tracing → Enable tracing**, then **Deploy**. Tracing only reaches a deploy made
   after it was enabled.

## Trigger

```sh
scripts/all.sh https://<otel-go domain>            # every scenario, 3× each
scripts/trigger.sh https://<otel-python domain> exception 20
scripts/trigger.sh https://<otel-javascript domain> downstream 5
```

Then open **Tools → Errors** and **Tools → Traces** (filter by application, or *Errors only*).

## Running one locally

Point it at any OTLP/HTTP endpoint with the same variables Formatio would inject — e.g. the
public endpoint and the token from the application's **Settings → Tracing** card:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=https://formatio.cloud/otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer fot_…"
export OTEL_SERVICE_NAME=otel-go
cd go && go run .                      # :8080
```

JavaScript: `npm install && npm start`. Python: `pip install -r requirements.txt &&
OTEL_TRACES_EXPORTER=otlp_proto_http opentelemetry-instrument python app.py`.

## Verified so far

All three were run locally against a sink built on `pkgs/otel`'s own decoder and grouping
(not a deployed Formatio): every SDK accepted the injected header format, `/downstream`
propagated one trace id across apps, and each scenario produced exactly one occurrence per
request in the expected group.

Known: anonymous handlers (the Express routes here) get a culprit like `at /app/app.js`,
because line numbers are stripped so groups survive deploys. Different error types still
group apart.
