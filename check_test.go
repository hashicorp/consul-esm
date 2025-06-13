// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/stretchr/testify/assert"
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
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, "")
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
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, "")
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
	runner := NewCheckRunner(logger, client, 0, minimumInterval, &tls.Config{}, 0, 0, "")
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
	runner := NewCheckRunner(logger, client, 0, minimumInterval, &tls.Config{}, 2, 2, "")
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
