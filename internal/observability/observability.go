package observability

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/sahal/parmesan/internal/config"
)

type Runtime struct {
	service    string
	registry   *prometheus.Registry
	httpReqs   *prometheus.CounterVec
	httpLat    *prometheus.HistogramVec
	events     *prometheus.CounterVec
	durations  *prometheus.HistogramVec
	inProgress *prometheus.GaugeVec
}

var (
	mu      sync.RWMutex
	current *Runtime
)

func newRuntime(service string) *Runtime {
	rt := &Runtime{
		service:  service,
		registry: prometheus.NewRegistry(),
		httpReqs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "parmesan_http_requests_total", Help: "HTTP requests by service, method, route, and status."}, []string{"service", "method", "route", "status"}),
		httpLat:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "parmesan_http_request_duration_seconds", Help: "HTTP request duration seconds by service, method, and route.", Buckets: prometheus.DefBuckets}, []string{"service", "method", "route"}),
		events:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "parmesan_runtime_events_total", Help: "Runtime event totals by service, subsystem, event, and status."}, []string{"service", "subsystem", "event", "status"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "parmesan_runtime_duration_seconds",
			Help:    "Runtime duration seconds by service, subsystem, and operation.",
			Buckets: prometheus.DefBuckets,
		}, []string{"service", "subsystem", "operation"}),
		inProgress: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "parmesan_runtime_in_progress",
			Help: "In-progress runtime operations by service, subsystem, and operation.",
		}, []string{"service", "subsystem", "operation"}),
	}
	rt.registry.MustRegister(rt.httpReqs, rt.httpLat, rt.events, rt.durations, rt.inProgress)
	return rt
}

func Init(ctx context.Context, cfg config.Config) (func(context.Context) error, error) {
	rt := newRuntime(cfg.ServiceName)

	shutdown := func(context.Context) error { return nil }
	if endpoint := strings.TrimSpace(cfg.Observability.OTLPEndpoint); endpoint != "" {
		headers := parseHeaders(cfg.Observability.OTLPHeaders)
		exporter, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")),
			otlptracehttp.WithHeaders(headers),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}
		provider := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceName(cfg.ServiceName),
			)),
		)
		otel.SetTracerProvider(provider)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		shutdown = provider.Shutdown
	}

	mu.Lock()
	current = rt
	mu.Unlock()
	return shutdown, nil
}

func Current() *Runtime {
	mu.RLock()
	defer mu.RUnlock()
	if current == nil {
		return newRuntime("unknown")
	}
	return current
}

func (r *Runtime) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Runtime) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		route := req.Pattern
		if route == "" {
			route = req.URL.Path
		}
		ctx, spanDone := r.StartSpan(req.Context(), "http", route,
			attribute.String("http.method", req.Method),
			attribute.String("http.route", route),
		)
		req = req.WithContext(ctx)
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, req)
		r.httpReqs.WithLabelValues(r.service, req.Method, route, statusString(ww.status)).Inc()
		r.httpLat.WithLabelValues(r.service, req.Method, route).Observe(time.Since(start).Seconds())
		spanDone(statusString(ww.status))
	})
}

func (r *Runtime) StartSpan(ctx context.Context, subsystem, name string, attrs ...attribute.KeyValue) (context.Context, func(string)) {
	tr := otel.Tracer("github.com/sahal/parmesan/" + subsystem)
	ctx, span := tr.Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	r.inProgress.WithLabelValues(r.service, subsystem, name).Inc()
	start := time.Now()
	return ctx, func(status string) {
		r.inProgress.WithLabelValues(r.service, subsystem, name).Dec()
		r.events.WithLabelValues(r.service, subsystem, name, first(status, "ok")).Inc()
		r.durations.WithLabelValues(r.service, subsystem, name).Observe(time.Since(start).Seconds())
		span.End()
	}
}

func statusString(v int) string {
	if v < 100 {
		return "0"
	}
	return http.StatusText(v / 100 * 100)
}

func parseHeaders(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key != "" && val != "" {
			out[key] = val
		}
	}
	return out
}

func first(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}
