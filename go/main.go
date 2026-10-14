// Command otel-go is a test target for Formatio's tracing: each route produces one kind of span
// or failure the Traces and Errors pages should show. The SDK is configured only by the OTEL_*
// variables Formatio injects, the way a real application would be.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("otel-go")

var errUserNotFound = errors.New("user not found")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := setupTracing(ctx)
	if err != nil {
		log.Fatalf("tracing: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", index)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /ok", ok)
	mux.HandleFunc("GET /slow", slow)
	mux.HandleFunc("GET /exception", exception)
	mux.HandleFunc("GET /not-found/{id}", notFound)
	mux.HandleFunc("GET /status-500", status500)
	mux.HandleFunc("GET /crash", crash)
	mux.HandleFunc("GET /downstream", downstream)

	// recoverPanics sits inside otelhttp so the server span is still current when it records.
	handler := otelhttp.NewHandler(recoverPanics(mux), "otel-go",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Pattern }))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{Addr: ":" + port, Handler: handler}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("otel-go listening on :%s, exporting to %q", port, os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	// Reads OTEL_EXPORTER_OTLP_ENDPOINT and OTEL_EXPORTER_OTLP_HEADERS; an http:// endpoint
	// (Formatio's internal one) is exported to without TLS.
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	// Reads OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES (formatio.deployment.id).
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintln(w, "otel-go: /ok /slow?ms=1500 /exception /not-found/{id} /status-500 /crash /downstream?path=/exception")
}

// ok makes a small tree of child spans: the happy path the waterfall should nest.
func ok(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "load dashboard")
	defer span.End()
	for _, step := range []string{"query users", "query orders", "render"} {
		_, child := tracer.Start(ctx, step, trace.WithAttributes(attribute.String("step", step)))
		time.Sleep(time.Duration(5+rand.IntN(20)) * time.Millisecond)
		child.End()
	}
	fmt.Fprintln(w, "ok")
}

func slow(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.Atoi(r.URL.Query().Get("ms"))
	if err != nil || ms <= 0 {
		ms = 1500
	}
	_, span := tracer.Start(r.Context(), "slow work", trace.WithAttributes(attribute.Int("sleep.ms", ms)))
	time.Sleep(time.Duration(ms) * time.Millisecond)
	span.End()
	fmt.Fprintf(w, "slept %dms\n", ms)
}

// exception records the same error from the same line every time: one group, growing count.
func exception(w http.ResponseWriter, r *http.Request) {
	err := loadInvoice(r.Context())
	fail(w, r, err, http.StatusInternalServerError)
}

func loadInvoice(ctx context.Context) error {
	_, span := tracer.Start(ctx, "load invoice")
	defer span.End()
	err := fmt.Errorf("invoice total is negative: %w", errors.ErrUnsupported)
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())
	return err
}

// notFound varies the message per id; the stack is the same, so it is still one group.
func notFound(w http.ResponseWriter, r *http.Request) {
	err := fmt.Errorf("user %s: %w", r.PathValue("id"), errUserNotFound)
	fail(w, r, err, http.StatusNotFound)
}

// status500 fails without recording an exception: a status-only error on the server span.
func status500(w http.ResponseWriter, r *http.Request) {
	trace.SpanFromContext(r.Context()).SetStatus(codes.Error, "upstream quota exhausted")
	http.Error(w, "upstream quota exhausted", http.StatusInternalServerError)
}

func crash(w http.ResponseWriter, r *http.Request) {
	var cache map[string][]int
	cache["user"][3] = 1 // nil map write: a real panic with a real stack
}

// downstream calls DOWNSTREAM_URL+path with the trace context propagated, so the request
// shows up as one trace across both applications.
func downstream(w http.ResponseWriter, r *http.Request) {
	base := os.Getenv("DOWNSTREAM_URL")
	if base == "" {
		http.Error(w, "set DOWNSTREAM_URL (e.g. http://otel-javascript:8080)", http.StatusBadRequest)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/exception"
	}

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, base+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		fail(w, r, err, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		trace.SpanFromContext(r.Context()).SetStatus(codes.Error, fmt.Sprintf("downstream answered %d", resp.StatusCode))
		w.WriteHeader(http.StatusBadGateway)
	}
	fmt.Fprintf(w, "downstream %s%s → %d: %s", base, path, resp.StatusCode, body)
}

func fail(w http.ResponseWriter, r *http.Request, err error, status int) {
	span := trace.SpanFromContext(r.Context())
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())
	http.Error(w, err.Error(), status)
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				// Still inside the panicking goroutine, so the captured stack includes the panic site.
				fail(w, r, fmt.Errorf("panic: %v", v), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
