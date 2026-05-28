package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ErrTracerInit is returned when the OTLP exporter cannot be configured.
// The caller should fall back to a no-op provider and continue.
var ErrTracerInit = errors.New("telemetry: tracer provider init failed")

// InitTracerProvider configures an OTel TracerProvider and sets it as the
// global. If endpoint is empty, a no-op provider is used (zero overhead, no
// export). If endpoint is set, an OTLP gRPC exporter is configured.
// The returned shutdown function must be called before process exit.
func InitTracerProvider(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		noopTP := noop.NewTracerProvider()
		otel.SetTracerProvider(noopTP)
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		// Fall back to no-op; return ErrTracerInit for caller to log as WARN.
		noopTP := noop.NewTracerProvider()
		otel.SetTracerProvider(noopTP)
		return func(context.Context) error { return nil },
			fmt.Errorf("%w: %s", ErrTracerInit, err)
	}

	res, _ := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceName("jira-poller")),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// noopTracerProvider is used internally; exposed for CT-7.
var _ trace.TracerProvider = noop.NewTracerProvider()
