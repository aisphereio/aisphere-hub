// Package observability centralizes hub-level logx/errorx/metrics helpers.
//
// Business modules should use this package instead of hand-rolling
// logx.FromContext / logger.WithContext / metrics labels in every method.
// The goal is consistent low-cardinality metrics and safe structured logs
// without leaking tokens, authorization codes, or secrets.
package observability

import (
	"context"
	"strconv"
	"time"

	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

const (
	MetricOperationsTotal           = "hub_operations_total"
	MetricOperationDurationSeconds  = "hub_operation_duration_seconds"
	MetricAuthnMiddlewareTotal      = "hub_authn_middleware_total"
	MetricAuthnMiddlewareDuration   = "hub_authn_middleware_duration_seconds"
	MetricComponentConfigured       = "hub_component_configured"
	MetricComponentReady            = "hub_component_ready"
	MetricComponentInitTotal        = "hub_component_init_total"
	MetricComponentInitDurationSecs = "hub_component_init_duration_seconds"
)

// RegisterMetrics registers all hub-owned metrics. metricsx registration is
// idempotent, so callers can safely invoke this from main, data.NewResources,
// and tests.
func RegisterMetrics(manager metricsx.Manager) {
	manager = metricsx.Ensure(manager)
	manager.NewCounter(MetricOperationsTotal, "Total hub business operations")
	manager.NewHistogram(MetricOperationDurationSeconds, "Hub business operation latency in seconds", metricsx.DefaultBuckets...)
	manager.NewCounter(MetricAuthnMiddlewareTotal, "Total HTTP/gRPC authn middleware decisions")
	manager.NewHistogram(MetricAuthnMiddlewareDuration, "HTTP/gRPC authn middleware latency in seconds", metricsx.DefaultBuckets...)
	manager.NewGauge(MetricComponentConfigured, "Whether a hub component is configured/enabled")
	manager.NewGauge(MetricComponentReady, "Whether a hub component is ready")
	manager.NewCounter(MetricComponentInitTotal, "Total hub component initialization attempts")
	manager.NewHistogram(MetricComponentInitDurationSecs, "Hub component initialization latency in seconds", metricsx.DefaultBuckets...)
}

// Begin binds a logger and standard fields to ctx. It is the recommended
// primitive for usecase/repo methods so all nested logs inherit component and
// operation labels.
func Begin(ctx context.Context, fallback logx.Logger, component, operation string, fields ...logx.Field) (context.Context, logx.Logger, time.Time) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := logx.FromContextOr(ctx, fallback)
	if logger == nil {
		logger = logx.DefaultLogger()
	}
	baseFields := []logx.Field{
		logx.String("component", component),
		logx.String("operation", operation),
	}
	baseFields = append(baseFields, fields...)
	logger = logger.Named(component).With(baseFields...)
	ctx = logx.Inject(ctx, logger, baseFields...)
	logger.Debug("operation started")
	return ctx, logger, time.Now()
}

// End records operation metrics and emits a structured finish log. Success is
// logged at debug level to avoid noisy production logs; failures are warnings.
func End(ctx context.Context, logger logx.Logger, manager metricsx.Manager, component, operation string, start time.Time, err error, fields ...logx.Field) {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = logx.FromContext(ctx)
	}
	manager = metricsx.FromContextOr(ctx, manager)
	status := Status(err)
	code := ErrorCode(err)
	elapsed := time.Since(start)
	labels := []string{"component", component, "operation", operation, "status", status, "error_code", code}
	manager.IncrementCounter(ctx, MetricOperationsTotal, labels...)
	manager.RecordHistogram(ctx, MetricOperationDurationSeconds, elapsed.Seconds(), labels...)

	logFields := []logx.Field{
		logx.String("status", status),
		logx.String("error_code", code),
		logx.Duration("elapsed", elapsed),
	}
	logFields = append(logFields, fields...)
	if err != nil {
		logFields = append(logFields, logx.Err(err))
		logger.Warn("operation failed", logFields...)
		return
	}
	logger.Debug("operation finished", logFields...)
}

// MiddlewareDecision records an authn middleware decision for HTTP/gRPC.
func MiddlewareDecision(ctx context.Context, manager metricsx.Manager, transport, decision string, start time.Time, err error) {
	manager = metricsx.FromContextOr(ctx, manager)
	labels := []string{
		"transport", transport,
		"decision", decision,
		"status", Status(err),
		"error_code", ErrorCode(err),
	}
	manager.IncrementCounter(ctx, MetricAuthnMiddlewareTotal, labels...)
	manager.RecordHistogram(ctx, MetricAuthnMiddlewareDuration, time.Since(start).Seconds(), labels...)
}

// ComponentConfigured sets a component enabled/disabled gauge.
func ComponentConfigured(manager metricsx.Manager, component string, enabled bool) {
	manager = metricsx.Ensure(manager)
	manager.SetGauge(MetricComponentConfigured, boolFloat(enabled), "component", component)
}

// ComponentReady sets a component readiness gauge.
func ComponentReady(manager metricsx.Manager, component string, ready bool) {
	manager = metricsx.Ensure(manager)
	manager.SetGauge(MetricComponentReady, boolFloat(ready), "component", component)
}

// ComponentInit records component init attempt metrics.
func ComponentInit(ctx context.Context, manager metricsx.Manager, component string, start time.Time, err error) {
	manager = metricsx.FromContextOr(ctx, manager)
	labels := []string{"component", component, "status", Status(err), "error_code", ErrorCode(err)}
	manager.IncrementCounter(ctx, MetricComponentInitTotal, labels...)
	manager.RecordHistogram(ctx, MetricComponentInitDurationSecs, time.Since(start).Seconds(), labels...)
	ComponentReady(manager, component, err == nil)
}

// Status maps err to a stable status label.
func Status(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

// ErrorCode returns a low-cardinality error code label. Unknown non-kernel
// errors collapse to UNKNOWN.
func ErrorCode(err error) string {
	if err == nil {
		return "none"
	}
	if e, ok := errorx.As(err); ok && e.Code() != "" {
		return string(e.Code())
	}
	return "UNKNOWN"
}

// BoolLabel returns "true" or "false" for low-cardinality labels.
func BoolLabel(v bool) string { return strconv.FormatBool(v) }

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
