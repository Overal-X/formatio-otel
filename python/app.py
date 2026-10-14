"""A test target for Formatio's tracing.

Tracing is started by `opentelemetry-instrument` (see the Dockerfile), configured only by the
OTEL_* variables Formatio injects, the way a real application would be.
"""

import json
import os
import random
import time
import urllib.request
from urllib.error import HTTPError

from flask import Flask, request
from opentelemetry import trace
from opentelemetry.trace import Status, StatusCode

app = Flask(__name__)
tracer = trace.get_tracer("otel-python")


@app.get("/")
def index():
    return "otel-python: /ok /slow?ms=1500 /exception /not-found/<id> /status-500 /crash /downstream?path=/exception\n"


@app.get("/healthz")
def healthz():
    return "ok"


@app.get("/ok")
def ok():
    """A small tree of child spans: the happy path the waterfall should nest."""
    with tracer.start_as_current_span("load dashboard"):
        for step in ("query users", "query orders", "render"):
            with tracer.start_as_current_span(step):
                time.sleep((5 + random.random() * 20) / 1000)
    return "ok\n"


@app.get("/slow")
def slow():
    ms = request.args.get("ms", type=int) or 1500
    with tracer.start_as_current_span("slow work") as span:
        span.set_attribute("sleep.ms", ms)
        time.sleep(ms / 1000)
    return f"slept {ms}ms\n"


@app.get("/exception")
def exception():
    """The same error from the same line every time: one group, growing count."""
    invoice = {"lines": []}
    return str(invoice["total"])  # KeyError


class NotFound(Exception):
    pass


@app.get("/not-found/<user_id>")
def not_found(user_id):
    """The message varies per id; the stack is the same, so it is still one group."""
    raise NotFound(f"user {user_id} not found")


@app.get("/status-500")
def status_500():
    """Fails without an exception: a status-only error on the server span."""
    trace.get_current_span().set_status(Status(StatusCode.ERROR, "upstream quota exhausted"))
    return "upstream quota exhausted\n", 500


@app.get("/crash")
def crash():
    ratio = len(request.args) / len(request.args)  # ZeroDivisionError with no query string
    return str(ratio)


@app.get("/downstream")
def downstream():
    """Calls DOWNSTREAM_URL+path; urllib is instrumented, so the trace context is propagated
    and the request shows up as one trace across both applications."""
    base = os.environ.get("DOWNSTREAM_URL")
    if not base:
        return "set DOWNSTREAM_URL (e.g. http://otel-go:8080)\n", 400
    path = request.args.get("path", "/exception")
    try:
        with urllib.request.urlopen(base + path, timeout=10) as response:
            return f"downstream {base}{path} → {response.status}: {response.read().decode()}"
    except HTTPError as error:
        if error.code >= 500:
            trace.get_current_span().set_status(
                Status(StatusCode.ERROR, f"downstream answered {error.code}")
            )
        return f"downstream {base}{path} → {error.code}: {error.read().decode()}", 502


@app.errorhandler(Exception)
def record(error):
    """Flask turns the exception into a 500 before the instrumentation sees it; recording it
    here is what gives the Errors page a type, message and stack."""
    span = trace.get_current_span()
    span.record_exception(error)
    span.set_status(Status(StatusCode.ERROR, str(error)))
    status = 404 if isinstance(error, NotFound) else 500
    return json.dumps({"error": type(error).__name__, "message": str(error)}), status


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8080"))
    print(f"otel-python listening on :{port}, exporting to {os.environ.get('OTEL_EXPORTER_OTLP_ENDPOINT')}")
    app.run(host="0.0.0.0", port=port)
