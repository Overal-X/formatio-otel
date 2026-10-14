// A test target for Formatio's tracing. Tracing is started by the zero-code
// @opentelemetry/auto-instrumentations-node/register hook (see package.json "start"), configured
// only by the OTEL_* variables Formatio injects, the way a real application would be.
// CommonJS on purpose: require-time patching is the path auto-instrumentation supports best.
const express = require('express')
const { SpanStatusCode, trace } = require('@opentelemetry/api')

const tracer = trace.getTracer('otel-javascript')
const app = express()

app.get('/', (_req, res) => {
  res.send(
    'otel-javascript: /ok /slow?ms=1500 /exception /not-found/:id /status-500 /crash /downstream?path=/exception\n',
  )
})

app.get('/healthz', (_req, res) => res.send('ok'))

// A small tree of child spans: the happy path the waterfall should nest.
app.get('/ok', async (_req, res) => {
  await tracer.startActiveSpan('load dashboard', async (span) => {
    for (const step of ['query users', 'query orders', 'render']) {
      await tracer.startActiveSpan(step, async (child) => {
        await sleep(5 + Math.random() * 20)
        child.end()
      })
    }
    span.end()
  })
  res.send('ok\n')
})

app.get('/slow', async (req, res) => {
  const ms = Number(req.query.ms) > 0 ? Number(req.query.ms) : 1500
  await tracer.startActiveSpan('slow work', async (span) => {
    span.setAttribute('sleep.ms', ms)
    await sleep(ms)
    span.end()
  })
  res.send(`slept ${ms}ms\n`)
})

// The same error from the same line every time: one group, growing count.
app.get('/exception', () => {
  const invoice = { lines: undefined }
  return invoice.lines.reduce((sum, line) => sum + line.total, 0) // TypeError
})

// The message varies per id; the stack is the same, so it is still one group.
app.get('/not-found/:id', (req) => {
  throw new NotFoundError(`user ${req.params.id} not found`)
})

// Fails without an exception: a status-only error on the server span.
app.get('/status-500', (_req, res) => {
  trace.getActiveSpan()?.setStatus({
    code: SpanStatusCode.ERROR,
    message: 'upstream quota exhausted',
  })
  res.status(500).send('upstream quota exhausted\n')
})

// A rejected promise inside an async handler, the way most Node crashes happen.
app.get('/crash', async () => {
  await sleep(5)
  JSON.parse('{"broken": ')
})

// Calls DOWNSTREAM_URL+path; the http instrumentation propagates the trace context, so the
// request shows up as one trace across both applications.
app.get('/downstream', async (req, res) => {
  const base = process.env.DOWNSTREAM_URL
  if (!base) {
    res.status(400).send('set DOWNSTREAM_URL (e.g. http://otel-python:8080)\n')
    return
  }
  const path = req.query.path || '/exception'
  const response = await fetch(base + path)
  const body = await response.text()
  if (response.status >= 500) {
    trace.getActiveSpan()?.setStatus({
      code: SpanStatusCode.ERROR,
      message: `downstream answered ${response.status}`,
    })
    res.status(502)
  }
  res.send(`downstream ${base}${path} → ${response.status}: ${body}`)
})

// Express 5 forwards thrown errors and rejected promises here. Recording them on the server
// span is what gives the Errors page a type, message and stack.
app.use((err, _req, res, _next) => {
  const span = trace.getActiveSpan()
  span?.recordException(err)
  span?.setStatus({ code: SpanStatusCode.ERROR, message: err.message })
  res.status(err.status ?? 500).send(`${err.name}: ${err.message}\n`)
})

class NotFoundError extends Error {
  name = 'NotFoundError'
  status = 404
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

const port = process.env.PORT || 8080
app.listen(port, () => {
  console.log(
    `otel-javascript listening on :${port}, exporting to ${process.env.OTEL_EXPORTER_OTLP_ENDPOINT}`,
  )
})
