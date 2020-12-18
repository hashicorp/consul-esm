package main

import (
	"fmt"
	"github.com/hashicorp/go-hclog"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func testAgent(t *testing.T, cb func(*Config)) *Agent {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
	})
	conf, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
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
	go func() {
		time.Sleep(time.Second)
		if err := agent.client.Agent().ServiceDeregister(serviceID); err != nil {
			t.Fatal(err)
		}
	}()

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
	retry.RunWith(&retry.Timer{Timeout: 10 * time.Second, Wait: 20 * time.Millisecond}, t, ensureDeregistered)

	// Wait for the agent to re-register the service and TTL check
	retry.Run(t, ensureRegistered)

	// Stop the ESM agent
	agent.Shutdown()

	// Make sure the service and check are gone
	retry.RunWith(&retry.Timer{Timeout: 15 * time.Second, Wait: 50 * time.Millisecond}, t, ensureDeregistered)
}

func TestAgent_shouldUpdateNodeStatus(t *testing.T) {
	t.Parallel()
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

	conf, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range cases {

		agent := Agent{
			config:            conf,
			knownNodeStatuses: make(map[string]lastKnownStatus),
		}

		// set up an existing node and configs for testing
		agent.updateLastKnownNodeStatus("existing", "healthy")
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

func TestAgent_VerifyConsulCompatibility(t *testing.T) {
	// Smoke test to test the compatibility with the current Consul version
	// pinned in go dependency.
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

	err = agent.VerifyConsulCompatibility()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestAgent_uniqueInstanceID(t *testing.T) {
	t.Parallel()

	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Register first ESM instance
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "unique-instance-id-1"
	})
	defer agent1.Shutdown()

	// Make sure the first ESM service is registered
	retry.Run(t, func(r *retry.R) {
		services, _, err := agent1.client.Catalog().Service(agent1.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 1 {
			r.Fatalf("1 service should be registered: %v", services)
		}
		if got, want := services[0].ServiceID, agent1.serviceID(); got != want {
			r.Fatalf("got service id %q, want %q", got, want)
		}
	})

	// Register second ESM instance
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "unique-instance-id-2"
	})
	defer agent2.Shutdown()

	// Make sure second ESM service is registered
	retry.Run(t, func(r *retry.R) {
		services, _, err := agent2.client.Catalog().Service(agent2.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 2 {
			r.Fatalf("2 service should be registered, got: %v", services)
		}
		if got, want := services[1].ServiceID, agent2.serviceID(); got != want {
			r.Fatalf("got service id %q, want %q", got, want)
		}
	})
}

func TestAgent_notUniqueInstanceIDFails(t *testing.T) {
	t.Parallel()
	notUniqueInstanceID := "not-unique-instance-id"

	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Register first ESM instance
	agent := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = notUniqueInstanceID
	})
	defer agent.Shutdown()

	// Make sure the ESM service is registered
	ensureRegistered := func(r *retry.R) {
		services, _, err := agent.client.Catalog().Service(agent.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 1 {
			r.Fatalf("1 service should be registered: %v", services)
		}
		if got, want := services[0].ServiceID, agent.serviceID(); got != want {
			r.Fatalf("got service id %q, want %q", got, want)
		}
	}
	retry.Run(t, ensureRegistered)

	// Create second ESM service with same instance ID
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
	})
	conf, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	conf.InstanceID = notUniqueInstanceID
	conf.HTTPAddr = s.HTTPAddr

	duplicateAgent, err := NewAgent(conf, logger)
	if err != nil {
		t.Fatal(err)
	}

	err = duplicateAgent.Run()
	defer duplicateAgent.Shutdown()

	if err == nil {
		t.Fatal("Failed to error when registering ESM service with same instance ID")
	}

	switch e := err.(type) {
	case *alreadyExistsError:
	default:
		t.Fatalf("Unexpected error type. Wanted an alreadyExistsError type. Error: '%v'", e)
	}
}
