package telemetry

import (
	"fmt"
	"log"

	"github.com/hashicorp/consul-esm/config"
	"go.opentelemetry.io/otel/api/metric"
	"go.opentelemetry.io/otel/exporters/metric/stdout"
	"go.opentelemetry.io/otel/sdk/metric/controller/push"
)

// NewStdout instantiates a metric sink to stdout.
func NewStdout(c *config.StdoutConfig) (metric.Provider, Controller, error) {
	log.Printf("[DEBUG] (telemetry) configuring stdout sink")

	cfg := stdout.Config{
		PrettyPrint:    *c.PrettyPrint,
		DoNotPrintTime: *c.DoNotPrintTime,
	}
	controller, err := stdout.NewExportPipeline(cfg, push.WithPeriod(*c.ReportingInterval))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize stdout exporter: %s", err)
	}

	log.Printf("[DEBUG] (telemetry) stdout initialized")
	return controller.Provider(), controller, nil
}
