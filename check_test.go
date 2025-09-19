// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"github.com/hashicorp/consul-esm/testutils/grpc"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	ts "github.com/hashicorp/consul-esm/testutils/grpc/testservice"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/stretchr/testify/assert"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestCheck_HTTP(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1)
	defer runner.Stop()

	// Register an external node with an initially critical http check.
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-http",
			Name:    "http-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				HTTP:             "http://" + s.HTTPAddr + "/v1/status/leader",
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to passing
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("expected: %v, got: %v", api.HealthPassing, checks[0].Status)
		}
	})

	// Re-register the check as critical initially
	// The catalog should eventually show the check as passing
	nodeRegistration.SkipNodeUpdate = true
	nodeRegistration.Check.Status = api.HealthCritical
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("expected: %v, got: %v", api.HealthPassing, checks[0].Status)
		}
	})

	// Update the check definition with an invalid http endpoint.
	nodeRegistration.Check.Definition.HTTP = "http://" + s.HTTPAddr + "/v1/nope"
	nodeRegistration.Check.Status = api.HealthPassing
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Wait for the health check to fail.
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if len(checks) != 1 || checks[0].Status != api.HealthCritical {
			r.Fatalf("expected: %v, got: %v", api.HealthCritical, checks[0].Status)
		}
	})
}

func TestCheck_TCP(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1)
	defer runner.Stop()

	// Register an external node with an initially critical http check
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-tcp",
			Name:    "tcp-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				TCP:              s.HTTPAddr,
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to passing
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("expected: %v, got: %v", api.HealthPassing, checks[0].Status)
		}
	})

	// Re-register the check as critical initially
	// The catalog should eventually show the check as passing
	nodeRegistration.SkipNodeUpdate = true
	nodeRegistration.Check.Status = api.HealthCritical
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to passing
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("expected: %v, got: %v", api.HealthPassing, checks[0].Status)
		}
	})

	// Update the check definition with an invalid http endpoint.
	nodeRegistration.Check.Definition.TCP = "127.0.0.1:22222"
	nodeRegistration.Check.Status = api.HealthPassing
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Wait for the health check to fail.
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if len(checks) != 1 || checks[0].Status != api.HealthCritical {
			r.Fatalf("expected: %v, got: %v", api.HealthCritical, checks[0].Status)
		}
	})
}
func TestCheck_GRPC_healthy(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	grpcServerAddr := grpc.RunTestServerWithHealthStatus(t, ts.NewServer(), healthpb.HealthCheckResponse_SERVING)

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1)
	defer runner.Stop()

	// Register an external node with an initially critical grpc check
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-grpc",
			Name:    "grpc-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				GRPC:             grpcServerAddr.String(),
				GRPCUseTLS:       false,
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to passing
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("expected: %v, got: %v", api.HealthPassing, checks[0].Status)
		}
	})

	// Re-register the check as critical initially
	// The catalog should eventually show the check as passing
	nodeRegistration.SkipNodeUpdate = true
	nodeRegistration.Check.Status = api.HealthCritical
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to passing
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("expected: %v, got: %v", api.HealthPassing, checks[0].Status)
		}
	})

	// Update the check definition with an invalid grpc endpoint.
	nodeRegistration.Check.Definition.GRPC = "127.0.0.1:22222"
	nodeRegistration.Check.Status = api.HealthPassing
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Wait for the health check to fail.
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if len(checks) != 1 || checks[0].Status != api.HealthCritical {
			r.Fatalf("expected: %v, got: %v", api.HealthCritical, checks[0].Status)
		}
	})
}

func TestCheck_GRPC_noValidTLS_unhealthy(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	grpcServerAddr := grpc.RunTestServerWithHealthStatus(t, ts.NewServer(), healthpb.HealthCheckResponse_SERVING)

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1)
	defer runner.Stop()

	// Register an external node with an initially passing grpc check and GRPCUseTLS enabled
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-grpc",
			Name:    "grpc-test",
			Status:  api.HealthPassing,
			Definition: api.HealthCheckDefinition{
				GRPC:             grpcServerAddr.String(),
				GRPCUseTLS:       true,
				TLSSkipVerify:    false,
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to critical as TLS validation is unsuccessful
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 {
			r.Fatalf("expected len: 1, got: %v", len(checks))
		}
		if checks[0].Status != api.HealthCritical {
			r.Fatalf("expected: %v, got: %v", api.HealthCritical, checks[0].Status)
		}
		if !strings.Contains(checks[0].Output, "tls") {
			r.Fatalf("expected to fail due to tls, got output: %v", checks[0].Output)
		}
	})
}

func TestCheck_GRPC_unhealthy(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	grpcServerAddr := grpc.RunTestServerWithHealthStatus(t, ts.NewServer(), healthpb.HealthCheckResponse_NOT_SERVING)

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1)
	defer runner.Stop()

	// Register an external node with an initially passing grpc check
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-grpc",
			Name:    "grpc-test",
			Status:  api.HealthPassing,
			Definition: api.HealthCheckDefinition{
				GRPC:             grpcServerAddr.String(),
				GRPCUseTLS:       false,
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	// Make sure the health has been updated to critical
	retry.Run(t, func(r *retry.R) {
		checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthCritical {
			r.Fatalf("expected: %v, got: %v", api.HealthCritical, checks[0].Status)
		}
	})
}

func TestCheck_MinimumInterval(t *testing.T) {
	// Confirm that a check's interval is at least the minimum interval

	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	minimumInterval := 2 * time.Second
	runner := NewCheckRunner(logger, client, 0, minimumInterval, &tls.Config{}, 0, 0)
	defer runner.Stop()

	// Make a check with an interval that is below the minimum required interval
	belowMinimumInterval := 1 * time.Second
	check := &api.HealthCheck{
		Node:    "external",
		CheckID: "below-minimum-interval",
		Name:    "below-minimum-interval-test",
		Status:  api.HealthCritical,
		Definition: api.HealthCheckDefinition{
			HTTP:             "http://localhost:8080",
			IntervalDuration: belowMinimumInterval,
		},
	}

	// run check
	checks := api.HealthChecks{check}
	runner.UpdateChecks(checks)

	// confirm that the original check's interval is unmodified
	originalCheck, ok := runner.checks.Load(hashCheck(check))
	if !ok {
		t.Fatalf("Check was not stored on runner.checks as expected. Checks: %v", runner.checks)
	}
	if originalCheck.Definition.IntervalDuration != belowMinimumInterval {
		t.Fatalf("Unprocessed check's interval was %v but should have remained unchanged at %v", originalCheck.Definition.IntervalDuration, belowMinimumInterval)
	}

	// confirm that esm's modified version of check's interval is updated
	esmCheck, ok := runner.checksHTTP.Load(hashCheck(check))
	if !ok {
		t.Fatalf("HTTP check was not stored on runner.checksHTTP as expected. Checks: %v", runner.checksHTTP)
	}
	if esmCheck.Interval != minimumInterval {
		t.Fatalf("Processed HTTP check's interval was %v but should have been updated to same as minimum interval %v", esmCheck.Interval, minimumInterval)
	}
}

func TestStopExistingChecks(t *testing.T) {
	t.Parallel()

	type fixture struct {
		runner *CheckRunner
	}

	setUp := func(t *testing.T) *fixture {

		s, err := NewTestServer(t)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Stop()

		client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
		if err != nil {
			t.Fatal(err)
		}

		logger := hclog.New(&hclog.LoggerOptions{
			Name:            "consul-esm",
			Level:           hclog.LevelFromString("INFO"),
			IncludeLocation: true,
			Output:          LOGOUT,
		})

		f := &fixture{
			runner: &CheckRunner{
				logger:          logger,
				client:          client,
				MinimumInterval: time.Second,
				tlsConfig:       &tls.Config{},
			},
		}

		t.Cleanup(func() {
			f.runner.Stop()
		})

		return f
	}

	t.Run("Check does not exist", func(t *testing.T) {
		f := setUp(t)

		stopped := f.runner.stopExistingChecks(&api.HealthCheckDefinition{}, "non-existing-check")
		require.False(t, stopped)
	})

	t.Run("Stopping existing http check", func(t *testing.T) {
		f := setUp(t)

		// add http check
		definition := api.HealthCheckDefinition{HTTP: "http://localhost:8080"}
		check := &api.HealthCheck{
			Node:       "external",
			CheckID:    "http-check-to-stop",
			Name:       "http-check-to-stop-test",
			Definition: definition,
		}

		f.runner.UpdateChecks(api.HealthChecks{check})

		// confirm that the check is stored
		_, httpCheckStored := f.runner.checksHTTP.Load(hashCheck(check))
		if !httpCheckStored {
			t.Fatalf("Check was not stored on runner.checksHTTP as expected. Checks: %v", f.runner.checksHTTP)
		}

		// stop the existing HTTP check
		stopped := f.runner.stopExistingChecks(&definition, hashCheck(check))
		_, httpCheckStored = f.runner.checksHTTP.Load(hashCheck(check))

		// check is stopped but not removed
		require.True(t, stopped)
		require.True(t, httpCheckStored)
	})

	t.Run("Stopping existing tcp check", func(t *testing.T) {
		f := setUp(t)

		// add tcp check
		definition := api.HealthCheckDefinition{TCP: "localhost:8080"}
		check := &api.HealthCheck{
			Node:       "external",
			CheckID:    "tcp-check-to-stop",
			Name:       "tcp-check-to-stop-test",
			Definition: definition,
		}

		f.runner.UpdateChecks(api.HealthChecks{check})

		// confirm that the check is stored
		_, tcpCheckStored := f.runner.checksTCP.Load(hashCheck(check))
		if !tcpCheckStored {
			t.Fatalf("Check was not stored on runner.checksTCP as expected. Checks: %v", f.runner.checksTCP)
		}

		// stop the existing TCP check
		stopped := f.runner.stopExistingChecks(&definition, hashCheck(check))
		_, tcpCheckStored = f.runner.checksTCP.Load(hashCheck(check))

		// check is stopped but not removed
		require.True(t, stopped)
		require.True(t, tcpCheckStored)
	})

	t.Run("Stopping existing grpc check", func(t *testing.T) {
		f := setUp(t)

		// add grpc check
		definition := api.HealthCheckDefinition{GRPC: "localhost:8080"}
		check := &api.HealthCheck{
			Node:       "external",
			CheckID:    "grpc-check-to-stop",
			Name:       "grpc-check-to-stop-test",
			Definition: definition,
		}

		f.runner.UpdateChecks(api.HealthChecks{check})

		// confirm that the check is stored
		_, grpcCheckStored := f.runner.checksGRPC.Load(hashCheck(check))
		if !grpcCheckStored {
			t.Fatalf("Check was not stored on runner.checksGRPC as expected. Checks: %v", f.runner.checksGRPC)
		}

		// stop the existing GRPC check
		stopped := f.runner.stopExistingChecks(&definition, hashCheck(check))
		_, grpcCheckStored = f.runner.checksGRPC.Load(hashCheck(check))

		// check is stopped but not removed
		require.True(t, stopped)
		require.True(t, grpcCheckStored)
	})

	t.Run("Stopping existing check of unexpected type", func(t *testing.T) {
		f := setUp(t)

		// add grpc check
		grpcDefinition := api.HealthCheckDefinition{GRPC: "localhost:8080"}
		check := &api.HealthCheck{
			Node:       "external",
			CheckID:    "check-to-stop",
			Name:       "check-to-stop-test",
			Definition: grpcDefinition,
		}

		f.runner.UpdateChecks(api.HealthChecks{check})

		// confirm that the check is stored
		_, grpcCheckStored := f.runner.checksGRPC.Load(hashCheck(check))
		if !grpcCheckStored {
			t.Fatalf("Check was not stored on runner.checksGRPC as expected. Checks: %v", f.runner.checksGRPC)
		}

		// stop the existing GRPC check
		tcpDefinition := api.HealthCheckDefinition{TCP: "localhost:8080"}
		stopped := f.runner.stopExistingChecks(&tcpDefinition, hashCheck(check))
		_, grpcCheckStored = f.runner.checksGRPC.Load(hashCheck(check))

		// check is removed as the type is unexpected
		require.True(t, stopped)
		require.False(t, grpcCheckStored)
	})
}

func TestTLSConfigsEqual(t *testing.T) {
	type testCase struct {
		c1, c2 *tls.Config
		equal  bool
	}
	testCases := []testCase{
		{
			c1:    &tls.Config{},
			c2:    &tls.Config{},
			equal: true,
		},
		{
			c1:    nil,
			c2:    nil,
			equal: true,
		},
		{
			c1:    &tls.Config{},
			c2:    nil,
			equal: false,
		},
		{
			c1:    nil,
			c2:    &tls.Config{},
			equal: false,
		},
		{
			c1:    &tls.Config{InsecureSkipVerify: false, ServerName: "test"},
			c2:    &tls.Config{InsecureSkipVerify: false, ServerName: "test"},
			equal: true,
		},
		{
			c1:    &tls.Config{InsecureSkipVerify: true, ServerName: "test"},
			c2:    &tls.Config{InsecureSkipVerify: false, ServerName: "test"},
			equal: false,
		},
		{
			c1:    &tls.Config{InsecureSkipVerify: true, ServerName: "test1"},
			c2:    &tls.Config{InsecureSkipVerify: true, ServerName: "test2"},
			equal: false,
		},
	}
	for _, tc := range testCases {
		switch eq := tlsConfigsEqual(tc.c1, tc.c2); tc.equal {
		case true:
			if !eq {
				t.Error("tls configs should be equal", tc.c1, tc.c2)
			}
		case false:
			if eq {
				t.Error("tls configs should NOT be equal", tc.c1, tc.c2)
			}
		}
	}
}

func TestCheck_NoFlapping(t *testing.T) {
	// Confirm that the status flapping protections work

	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})
	minimumInterval := 2 * time.Second
	runner := NewCheckRunner(logger, client, 0, minimumInterval, &tls.Config{}, 2, 2)
	defer runner.Stop()

	// Register an external node with an initially critical http check.
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-http",
			Name:    "http-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				HTTP:             "http://" + s.HTTPAddr + "/v1/status/leader",
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	_, err = client.Catalog().Register(nodeRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)

	hash := hashCheck(checks[0])
	id := structs.CheckID{ID: hash}

	originalCheck, ok := runner.checks.Load(hash)
	if !ok {
		t.Fatalf("Check was not stored on runner.checks as expected. Checks: %v", runner.checks)
	}

	// test consecutive checks: when threshold is met, the status will toggle from
	// critical => passing and counters will reset
	assert.Equal(t, 0, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthCritical, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthPassing, "")
	assert.Equal(t, 0, originalCheck.failureCounter)
	assert.Equal(t, 1, originalCheck.successCounter)
	assert.Equal(t, api.HealthCritical, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthPassing, "")
	assert.Equal(t, 0, originalCheck.failureCounter)
	assert.Equal(t, 2, originalCheck.successCounter)
	assert.Equal(t, api.HealthCritical, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthPassing, "")
	assert.Equal(t, 0, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthPassing, originalCheck.Status)

	// test non-consecutive checks: non-consecutive will increment and
	// decrement accordingly until threshold is crossed
	runner.UpdateCheck(id, api.HealthCritical, "")
	assert.Equal(t, 1, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthPassing, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthCritical, "")
	assert.Equal(t, 2, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthPassing, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthPassing, "")
	assert.Equal(t, 1, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthPassing, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthCritical, "")
	assert.Equal(t, 2, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthPassing, originalCheck.Status)

	runner.UpdateCheck(id, api.HealthCritical, "")
	assert.Equal(t, 0, originalCheck.failureCounter)
	assert.Equal(t, 0, originalCheck.successCounter)
	assert.Equal(t, api.HealthCritical, originalCheck.Status)

	// test if counter values are kept after calling
	// UpdateChecks(), which is called everytime there is
	// a change in the consul catalog
	runner.UpdateCheck(id, api.HealthPassing, "")
	assert.Equal(t, 0, originalCheck.failureCounter)
	assert.Equal(t, 1, originalCheck.successCounter)
	assert.Equal(t, api.HealthCritical, originalCheck.Status)

	runner.UpdateChecks(checks)
	currentCheck, ok := runner.checks.Load(hash)
	if !ok {
		t.Fatalf("Current check was not stored on runner.checks as expected. Checks: %v", runner.checks)
	}

	assert.Equal(t, 0, currentCheck.failureCounter)
	assert.Equal(t, 1, currentCheck.successCounter)
	assert.Equal(t, api.HealthCritical, currentCheck.Status)
}

func TestHeadersAlmostEqual(t *testing.T) {
	type headers map[string][]string
	type testCase struct {
		h1, h2 headers
		equal  bool
	}
	testCases := []testCase{
		{
			h1:    headers{},
			h2:    headers{},
			equal: true,
		},
		{
			h1:    headers{"foo": {"foo"}},
			h2:    headers{"bar": {"bar"}},
			equal: false,
		},
		{
			h1:    headers{"User-Agent": {"foo"}},
			h2:    headers{"Accept": {"bar"}},
			equal: true,
		},
		{
			h1:    headers{"foo": {"foo"}, "User-Agent": {"foo"}},
			h2:    headers{"Accept": {"bar"}},
			equal: false,
		},
		{
			h1:    headers{"foo": {"foo"}, "User-Agent": {"foo"}},
			h2:    headers{"foo": {"foo"}, "Accept": {"bar"}},
			equal: true,
		},
	}
	for _, tc := range testCases {
		switch eq := headersAlmostEqual(tc.h1, tc.h2); tc.equal {
		case true:
			if !eq {
				t.Error("headers should be equal", tc.h1, tc.h2)
			}
		case false:
			if eq {
				t.Error("headers should NOT be equal", tc.h1, tc.h2)
			}
		}
	}
}
