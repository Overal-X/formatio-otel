// Command otel-go is a test target for Formatio's tracing: each route produces one kind of span
// or failure the Traces and Errors pages should show. The SDK is configured only by the OTEL_*
// variables Formatio injects, the way a real application would be.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
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
	mux.HandleFunc("POST /orders", createOrder)
	mux.HandleFunc("GET /api/dashboard", dashboard)
	mux.HandleFunc("GET /internal/rates", rates)

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
	fmt.Fprintln(w, "otel-go: /ok /slow?ms=1500 /exception /not-found/{id} /status-500 /crash /downstream?path=/exception, POST /orders (checkout scenario), /api/dashboard and /internal/rates (dashboard scenario)")
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

// The checkout scenario (see the README): the frontend posts an order here, and this service
// plays the orders API between it and payments.
type order struct {
	Items []struct {
		SKU string `json:"sku"`
		Qty int    `json:"qty"`
	} `json:"items"`
	// Total is null when the frontend failed to price the cart and sent the order anyway.
	Total *float64 `json:"total"`
}

// createOrder trusts the total it is given — the bug that lets a frontend failure travel on:
// it reserves stock, asks payments to charge a null amount, and only finds out when payments
// falls over.
func createOrder(w http.ResponseWriter, r *http.Request) {
	var o order
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		fail(w, r, err, http.StatusBadRequest)
		return
	}
	base := os.Getenv("PAYMENTS_URL")
	if base == "" {
		http.Error(w, "set PAYMENTS_URL (e.g. http://otel-python:8080)", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	_, reserve := tracer.Start(ctx, "reserve inventory", trace.WithAttributes(attribute.Int("order.items", len(o.Items))))
	time.Sleep(time.Duration(10+rand.IntN(20)) * time.Millisecond)
	reserve.End()

	body, _ := json.Marshal(map[string]any{"amount": o.Total, "currency": "USD"})
	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
	if err != nil || resp.StatusCode >= 500 {
		_, release := tracer.Start(ctx, "release inventory")
		time.Sleep(time.Duration(5+rand.IntN(10)) * time.Millisecond)
		release.End()

		reason := "payment service unreachable"
		if err == nil {
			reason = fmt.Sprintf("payment service answered %d", resp.StatusCode)
		}
		trace.SpanFromContext(ctx).SetStatus(codes.Error, reason)
		http.Error(w, reason, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"order": fmt.Sprintf("ord_%d", rand.IntN(1_000_000)), "charged": o.Total})
}

// The dashboard scenario (see the README): this service is both the API the frontend calls and,
// two hops further down, the rates service whose database outage breaks everything above it.

var errRatesDB = errors.New("dial tcp 10.0.4.12:5432: connect: connection refused")

// rates is the service that breaks: its database is down, so every lookup fails.
// ?healthy=1 answers normally, for comparison.
func rates(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "query rates-db", trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
	))
	time.Sleep(time.Duration(20+rand.IntN(20)) * time.Millisecond)
	if r.URL.Query().Get("healthy") == "" {
		err := fmt.Errorf("load exchange rates: %w", errRatesDB)
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetStatus(codes.Error, err.Error())
		span.End()
		trace.SpanFromContext(r.Context()).SetStatus(codes.Error, "rates database unavailable")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rates database unavailable"})
		return
	}
	span.End()
	writeJSON(w, http.StatusOK, map[string]any{"base": "USD", "EUR": 0.92, "GBP": 0.79})
}

// dashboard is the API behind the frontend's dashboard. It fetches the widgets in parallel and
// fails the whole page when one of them fails — the missing graceful degradation that lets one
// broken service take the frontend down with it.
func dashboard(w http.ResponseWriter, r *http.Request) {
	base := os.Getenv("PYTHON_URL")
	if base == "" {
		base = os.Getenv("PAYMENTS_URL")
	}
	if base == "" {
		http.Error(w, "set PYTHON_URL (or PAYMENTS_URL), e.g. http://otel-python:8080", http.StatusBadRequest)
		return
	}
	suffix := ""
	if r.URL.Query().Get("healthy") != "" {
		suffix = "?healthy=1"
	}

	ctx := r.Context()
	_, profile := tracer.Start(ctx, "load profile")
	time.Sleep(time.Duration(5+rand.IntN(10)) * time.Millisecond)
	profile.End()

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 10 * time.Second}
	widgets := map[string]string{"recommendations": "/recommendations", "loyalty": "/loyalty" + suffix}
	results := map[string]json.RawMessage{}
	failures := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, path := range widgets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
			resp, err := client.Do(req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures[name] = http.StatusBadGateway
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 400 {
				failures[name] = resp.StatusCode
				return
			}
			results[name] = body
		}()
	}
	wg.Wait()

	for name, status := range failures {
		trace.SpanFromContext(ctx).SetStatus(codes.Error, fmt.Sprintf("%s widget answered %d", name, status))
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": name + " unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":         map[string]any{"name": "Ada", "plan": "pro"},
		"recommendations": results["recommendations"],
		"loyalty":         results["loyalty"],
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
