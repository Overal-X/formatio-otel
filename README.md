# Formatio OTel Test Projects

Test targets for Formatio's tracing and error tracking (`specs/otel-tracing.md` in the
formatio repo). One small HTTP app per language, all serving the same routes, each route
producing one thing the **Tools → Traces** and **Tools → Errors** pages should show.

Every app uses the stock OpenTelemetry SDK and is configured **only** by the `OTEL_*`
variables Formatio injects. Nothing in here points at an endpoint or holds a token: if
traces arrive, the injection works.

| Folder        | Stack                                                   | Started by                              |
| ------------- | ------------------------------------------------------- | --------------------------------------- |
| `go/`         | `net/http` + `otelhttp`, OTLP/HTTP exporter             | `main.go` sets up the SDK from env      |
| `javascript/` | Express 5 + `@opentelemetry/auto-instrumentations-node` | `node --require …/register` (zero-code) |
| `python/`     | Flask + `opentelemetry-distro`                          | `opentelemetry-instrument` (zero-code)  |

## Routes (all three apps)

| Route                         | What it does                                      | Expect in Formatio                                                                                                                       |
| ----------------------------- | ------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `/ok`                         | 200, three nested child spans                     | A trace with a nested waterfall                                                                                                          |
| `/slow?ms=1500`               | One long child span                               | Shows under the `> 1 s` filter                                                                                                           |
| `/exception`                  | The same exception from the same line             | **One** group whose count grows                                                                                                          |
| `/not-found/:id`              | Exception, message varies per id                  | Still **one** group (same stack)                                                                                                         |
| `/status-500`                 | 500 with no exception recorded                    | A status-only group named after the route                                                                                                |
| `/crash`                      | Go panic / rejected promise / `ZeroDivisionError` | One group each                                                                                                                           |
| `/downstream?path=/exception` | Calls `DOWNSTREAM_URL` + path                     | **One trace across two apps**, the thrower marked _origin_; the caller gets its own `/downstream` group, the thrower its exception group |
| `/healthz`                    | 200                                               | —                                                                                                                                        |

## Scenario: a frontend error that breaks the services behind it

`GET /checkout` on the **JavaScript** app runs a checkout through all three apps:

```
JavaScript (frontend)          Go (orders API)              Python (payments)
GET /checkout
 └ price cart  ✗ PriceFormatError: cannot price kb-01: "1,299.00"   ← recorded, then swallowed
 └ POST /orders  { total: null } ─▶ POST /orders
                                  └ reserve inventory
                                  └ POST /charge { amount: null } ─▶ POST /charge
                                                                    └ compute fees ✗ TypeError
                                  └ release inventory  ◀── 500
 ◀── 502 "payment service answered 500"
502 to the user
```

The frontend's bug is swallowing its own error and sending the order on anyway; nothing fails
there yet. The failure surfaces two services later, in payments, and travels back as 502s.

What Formatio should show:

- **Tools → Traces**: one trace spanning all three applications. The waterfall labels the
  frontend's `price cart` span **origin** — the first exception in the trace — even though the
  crash happened in payments.
- **Tools → Errors**: `PriceFormatError` (JavaScript), `TypeError` (Python, recorded at both
  `compute fees` and the request but counted once), and a status-only `POST /orders` group (Go,
  "HTTP 502" — Go's HTTP instrumentation sets that status itself). The frontend's own 502 opens no
  extra group: its trace already recorded the exception that caused it.

`GET /checkout?healthy=1` sends clean prices and succeeds end to end — compare the two traces.

It needs two variables, besides the `DOWNSTREAM_URL` ring below (use your application names as
the hostnames):

- JavaScript: `ORDERS_URL=http://otel-go:8080`
- Go: `PAYMENTS_URL=http://otel-python:8080`

```sh
scripts/trigger.sh https://<javascript app domain> checkout 5
scripts/trigger.sh https://<javascript app domain> checkout-ok 5
```

## Scenario: a broken service deep down takes the frontend with it

`GET /dashboard` on the **JavaScript** app is the opposite of checkout: the frontend is where
the failure shows, and the trace leads you to the service that caused it.

```
JavaScript (frontend)     Go (API)                 Python (widgets)          Go (rates)
GET /dashboard ─────────▶ GET /api/dashboard
                          └ load profile
                          ├ GET /recommendations ─▶ ✓ rank items
                          └ GET /loyalty ─────────▶ GET /loyalty
                                                    └ load points
                                                    └ GET /internal/rates ──▶ GET /internal/rates
                                                                              └ query rates-db
                                                                                ✗ connection refused
                                                    ◀── 503 ────────────────
                          ◀── 502 ───────────────── "rates answered 503"
◀── 502 {"error": …} ──── fails the whole page instead of the one widget
✗ TypeError: Cannot read properties of undefined (reading 'name')   in renderDashboard
500 to the user
```

Each hop has its own small bug that lets the failure through — loyalty passes the outage on,
the API fails the whole dashboard over one widget, the frontend renders an error body as if it
were data — which is the point: what the user sees is three services away from what broke.

What Formatio should show:

- **Tools → Errors**: the frontend's `TypeError` (in `renderDashboard`) is what someone notices
  first. Open its trace from an occurrence.
- **The waterfall** spans all three applications and labels **origin** on Go's `query rates-db`
  — `dial tcp 10.0.4.12:5432: connect: connection refused` — the first exception in the trace,
  with the 503 → 502 → 502 → 500 chain above it.
- Also in Errors: Go's `connection refused` group and Python's status-only `GET /loyalty` (HTTP
  502). Python's `HTTPError` from calling rates is not its own group: rates already recorded
  that failure. Go's `GET /api/dashboard` usually opens none either, because its spans are
  exported together with the rates exception from the same process.

`GET /dashboard?healthy=1` takes the same path with the rates database up and renders the page.

It reuses the checkout variables (`ORDERS_URL` on JavaScript, `PAYMENTS_URL` on Go) and needs
one more, on Python, pointing back at Go — `GO_URL=http://otel-go:8080` (it falls back to
`DOWNSTREAM_URL`). `GO_URL` and `PYTHON_URL` also work on JavaScript and Go in place of the
checkout names.

```sh
scripts/trigger.sh https://<javascript app domain> dashboard 5
scripts/trigger.sh https://<javascript app domain> dashboard-ok 5
```

## Deploy on Formatio

Create three applications from this repository in **one project** (so they share its
network). Either builder works.

**Railpack, from the repository root** — leave _Directory_ empty and pick the config:

| Application name  | Builder  | Config file                |
| ----------------- | -------- | -------------------------- |
| `otel-go`         | Railpack | `railpack.go.json`         |
| `otel-javascript` | Railpack | `railpack.javascript.json` |
| `otel-python`     | Railpack | `railpack.python.json`     |

Railpack only detects a language at the root of what it builds, and the root has no
`go.mod`, `package.json` or `requirements.txt`, so each config names its runtime, builds its
folder and says how to start it.

**Dockerfile** — one folder per application:

| Application name  | Dockerfile              | Build context |
| ----------------- | ----------------------- | ------------- |
| `otel-go`         | `go/Dockerfile`         | `go`          |
| `otel-javascript` | `javascript/Dockerfile` | `javascript`  |
| `otel-python`     | `python/Dockerfile`     | `python`      |

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

Then open **Tools → Errors** and **Tools → Traces** (filter by application, or _Errors only_).

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

All three Railpack configs and Dockerfiles build (Railpack 0.23 against BuildKit), and each
image answers `/healthz` and `/exception`. The apps themselves were run locally against a sink
built on `pkgs/otel`'s own decoder and grouping (not a deployed Formatio): every SDK accepted the injected header format, `/downstream`
propagated one trace id across apps, and each scenario produced exactly one occurrence per
request in the expected group.

Known: anonymous handlers (the Express routes here) get a culprit like `at /app/app.js`,
because line numbers are stripped so groups survive deploys. Different error types still
group apart.

scripts/trigger.sh https://javascript.localhost dashboard 5
scripts/trigger.sh https://javascript.localhost dashboard-ok 5
scripts/trigger.sh https://javascript.localhost checkout 5
scripts/trigger.sh https://javascript.localhost checkout-ok 5
