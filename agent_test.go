package main

import (
	"fmt"
	"log"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func testAgent(t *testing.T, cb func(*Config)) *Agent {
	logger := log.New(LOGOUT, "", log.LstdFlags)
	conf := DefaultConfig()
	conf.CoordinateUpdateInterval = 200 * time.Millisecond
	if cb != nil {
		cb(conf)
	}

	agent, err := NewAgent(conf, logger)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		if err := agent.Run(); err != nil {
			t.Fatal(err)
		}
	}()

	return agent
}

func TestAgent_registerServiceAndCheck(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	agent := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.Tag = "test"
	})
	defer agent.Shutdown()

	// Lower these retry intervals
	serviceID := fmt.Sprintf("%s:%s", agent.config.Service, agent.id)

	// Make sure the ESM service and TTL check are registered
	ensureRegistered := func(r *retry.R) {
		services, _, err := agent.client.Catalog().Service(agent.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 1 {
			r.Fatalf("bad: %v", services)
		}
		if got, want := services[0].ServiceID, serviceID; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := services[0].ServiceName, agent.config.Service; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := services[0].ServiceTags, []string{"test"}; !reflect.DeepEqual(got, want) {
			r.Fatalf("got %q, want %q", got, want)
		}

		checks, _, err := agent.client.Health().Checks(agent.config.Service, nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 {
			r.Fatalf("bad: %v", checks)
		}
		if got, want := checks[0].CheckID, fmt.Sprintf("%s:%s:agent-ttl", agent.config.Service, agent.id); got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].Name, "Consul External Service Monitor Alive"; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].Status, "passing"; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].ServiceID, fmt.Sprintf("%s:%s", agent.config.Service, agent.id); got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].ServiceName, agent.config.Service; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
	}
	retry.Run(t, ensureRegistered)

	// Deregister the service
	if err := agent.client.Agent().ServiceDeregister(serviceID); err != nil {
		t.Fatal(err)
	}

	// Make sure the service and check are deregistered
	ensureDeregistered := func(r *retry.R) {
		services, _, err := agent.client.Catalog().Service(agent.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 0 {
			r.Fatalf("bad: %v", services[0])
		}

		checks, _, err := agent.client.Health().Checks(agent.config.Service, nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 0 {
			r.Fatalf("bad: %v", checks)
		}
	}
	retry.Run(t, ensureDeregistered)

	// Wait for the agent to re-register the service and TTL check
	retry.Run(t, ensureRegistered)

	// Stop the ESM agent
	agent.Shutdown()

	// Make sure the service and check are gone
	retry.Run(t, ensureDeregistered)
}

func TestAgent_shouldUpdateNodeStatus(t *testing.T) {
	t.Parallel()

	agent := Agent{
		config:            DefaultConfig(),
		knownNodeStatuses: make(map[string]lastKnownStatus),
	}

	// add an existing node to agent for testing
	agent.updateLastKnownNodeStatus("existing", "healthy")

	// Warning: test cases are deliberately ordered for changing health statuses and allowing time for status to expire
	cases := []struct {
		scenario                  string
		node                      string
		status                    string
		nodeHealthRefreshInterval time.Duration
		expected                  bool
	}{
		{
			scenario:                  "Existing node, not expired status, same status: should not need to update",
			node:                      "existing",
			status:                    "healthy",
			nodeHealthRefreshInterval: 1 * time.Hour,
			expected:                  false,
		},
		{
			scenario:                  "Existing node, expired status, same status: should need to update",
			node:                      "existing",
			status:                    "healthy",
			nodeHealthRefreshInterval: 0 * time.Millisecond,
			expected:                  true,
		},
		{
			scenario:                  "Existing node, not expired status, different status: should need to update",
			node:                      "existing",
			status:                    "critical",
			nodeHealthRefreshInterval: 1 * time.Hour,
			expected:                  true,
		},
		{
			scenario:                  "New node: should need to update",
			node:                      "new node",
			status:                    "critical",
			nodeHealthRefreshInterval: 0 * time.Hour,
			expected:                  true,
		},
	}

	for _, tc := range cases {
		agent.config.NodeHealthRefreshInterval = tc.nodeHealthRefreshInterval

		actual := agent.shouldUpdateNodeStatus(tc.node, tc.status)
		if actual != tc.expected {
			t.Fatalf("%s - expected need to update '%t', got '%t'", tc.scenario, tc.expected, actual)
		}
	}
}

func TestAgent_LastKnownStatusIsExpired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		scenario  string
		statusAge time.Duration
		ttl       time.Duration
		expected  bool
	}{
		{
			scenario:  "Last known time is within TTL",
			statusAge: 5 * time.Minute,
			ttl:       1 * time.Hour,
			expected:  false,
		},
		{
			scenario:  "Last known time is beyond of TTL",
			statusAge: 1 * time.Hour,
			ttl:       5 * time.Minute,
			expected:  true,
		},
	}

	for _, tc := range cases {
		lastKnown := lastKnownStatus{
			status: "healthy",
			time:   time.Now().Add(-tc.statusAge),
		}

		actual := lastKnown.isExpired(tc.ttl, time.Now())

		if actual != tc.expected {
			t.Fatalf("%s - expected expired '%t', got '%t'", tc.scenario, tc.expected, actual)
		}
	}
}
