package main

import (
	"log"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func TestCheck_HTTP(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0)
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
	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0)
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
