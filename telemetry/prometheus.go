package telemetry

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/hashicorp/consul-esm/config"
	"go.opentelemetry.io/otel/api/metric"
	"go.opentelemetry.io/otel/exporters/metric/prometheus"
	"go.opentelemetry.io/otel/sdk/metric/controller/pull"
)

type prometheusController struct {
	server *http.Server
}

// NewPrometheus instantiates a metric sink for Prometheus with an HTTP server
// serving a `/metrics` endpoint for scraping.
func NewPrometheus(c *config.PrometheusConfig) (metric.Provider, Controller, error) {
	log.Printf("[DEBUG] (telemetry) configuring Prometheus sink")

	exporter, err := prometheus.NewExportPipeline(prometheus.Config{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize prometheus exporter %v", err)
	}

	exporter.SetController(prometheus.Config{}, pull.WithCachePeriod(*c.CachePeriod))

	mux := http.NewServeMux()
	addr := fmt.Sprintf(":%d", *c.Port)
	server := http.Server{Addr: addr, Handler: mux}
	mux.HandleFunc("/metrics", exporter.ServeHTTP)

	go func(server *http.Server) {
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("failed to run Prometheus /metrics endpoint: %v", err)
		}
	}(&server)

	log.Printf("[DEBUG] (telemetry) prometheus initialized, serving /metrics on port %d", *c.Port)
	return exporter.Provider(), &prometheusController{&server}, nil
}

func (p *prometheusController) Stop() {
	err := p.server.Shutdown(context.Background())
	if err != nil {
		log.Printf("[WARN] (telemetry) error shutting down HTTP server for "+
			"prometheus exporter: %s", err)
	}
}
