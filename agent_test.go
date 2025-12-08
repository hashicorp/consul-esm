// Copyright IBM Corp. 2017, 2025
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func setupMetricsSink() *metrics.InmemSink {
	interval := 1 * time.Second
	retention := 10 * time.Second
	sink := metrics.NewInmemSink(interval, retention)
	cfg := metrics.DefaultConfig("consul-esm")
	cfg.EnableHostname = false
	metrics.NewGlobal(cfg, sink)
	return sink
}

func testAgent(t *testing.T, cb func(*Config)) *Agent {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
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
			panic(err)
		}
	}()

	// make sure test agent is ready before continuing
	<-agent.ready

	return agent
}

func TestAgent_AgentLess(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	agent := testAgent(t, func(c *Config) {
		c.EnableAgentless = true
		c.HTTPAddr = s.HTTPAddr
		c.Tag = "test"
	})
	defer agent.Shutdown()
	// Lower these retry intervals
	serviceID := fmt.Sprintf("%s:%s", agent.config.Service, agent.id)
	nodeID := agent.agentlessNodeID()

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
		if got, want := services[0].ServiceMeta, map[string]string{"external-source": "consul-esm"}; !reflect.DeepEqual(got, want) {
			r.Fatalf("got %q, want %q", got, want)
		}

		// ESM heath is dependent on node health since it is agentless
		checks, _, err := agent.client.Health().Node(services[0].Node, nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 {
			r.Fatalf("bad: %v", checks)
		}
		if got, want := checks[0].CheckID, agent.agentlessCheckID(); got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].Name, "Consul External Service Monitor Alive"; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].Status, "critical"; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}

	}
	retry.Run(t, ensureRegistered)

	// Make sure the service and check are deregistered, we may need to try de-registering multiple times since ESM is re-registering the service
	ensureDeregistered := func(r *retry.R) {
		// Deregister the service
		time.Sleep(time.Second)
		dereg := &api.CatalogDeregistration{
			Node:      nodeID,
			ServiceID: serviceID,
		}
		// deregister the service from catalog
		if _, err := agent.client.Catalog().Deregister(dereg, nil); err != nil {
			panic(err)
		}

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

func TestAgent_AgentLessSessions(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	agent := testAgent(t, func(c *Config) {
		c.EnableAgentless = true
		c.HTTPAddr = s.HTTPAddr
		c.Tag = "test"
		// start node as healthy
		c.NodeMeta["initial-health"] = "passing"
	})
	defer agent.Shutdown()

	// ensure sessions
	ensureSessions := func(r *retry.R) {
		// ensure session is registered
		sessions, _, err := agent.client.Session().List(&api.QueryOptions{})
		if err != nil {
			r.Fatal(err)
		}

		if len(sessions) != 2 {
			r.Fatalf("got %d, want 2", len(sessions))
		}

		// check each session
		for _, session := range sessions {
			if session.Name != agent.agentlessSessionID() {
				continue
			}

			if len(session.NodeChecks) != 1 {
				r.Fatalf("bad: %v, want 1", len(session.NodeChecks))
			}

			if len(session.Checks) != 1 {
				r.Fatalf("bad: %v, want 1", len(session.NodeChecks))
			}

			if session.NodeChecks[0] != agent.agentlessCheckID() {
				r.Fatalf("bad: %v, want: %v", session.NodeChecks[0], agent.agentlessCheckID())
			}

			if session.Checks[0] != agent.agentlessCheckID() {
				r.Fatalf("bad: %v, want: %v", session.Checks[0], agent.agentlessCheckID())
			}
		}
	}

	// Make sure the session is registered
	retry.RunWith(&retry.Counter{Wait: 2 * time.Second, Count: 10}, t, ensureSessions)
	// Stop the ESM agent
	agent.Shutdown()
}

func TestAgent_registerServiceAndCheck(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer(t)
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
		if got, want := services[0].ServiceMeta, map[string]string{"external-source": "consul-esm"}; !reflect.DeepEqual(got, want) {
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
			panic(err)
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
	type testCase struct {
		name, agentJSON, serviceJSON string
		shouldPass                   bool
	}
	testVersion := func(t *testing.T, tc testCase) {
		ts := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				uri := r.RequestURI
				switch { // ignore anything that doesn't require a return body
				case strings.Contains(uri, "status/leader"):
					fmt.Fprint(w, `"127.0.0.1"`)
				case strings.Contains(uri, "agent/self"):
					fmt.Fprint(w, tc.agentJSON)
				case strings.Contains(uri, "catalog/service"):
					fmt.Fprint(w, tc.serviceJSON)
				}
			}))
		defer ts.Close()

		agent := testAgent(t, func(c *Config) {
			c.HTTPAddr = ts.URL
			c.InstanceID = "test-agent"
		})
		time.Sleep(time.Millisecond) // race with Run's go routines
		defer agent.Shutdown()

		err := agent.VerifyConsulCompatibility()
		switch {
		case tc.shouldPass && err != nil:
			t.Fatalf("unexpected error: %s", err)
		case !tc.shouldPass && err == nil:
			t.Fatalf("should be an error and wasn't: %#v", tc)
		}
	}
	testCases := []testCase{
		{
			name:        "good",
			agentJSON:   `{"Config": {"Version": "1.10.0"}}`,
			serviceJSON: `[{"ServiceMeta" : {"version": "1.10.0"}}]`,
			shouldPass:  true,
		},
		{
			name:        "bad-agent",
			agentJSON:   `{"Config": {"Version": "1.0.0"}}`,
			serviceJSON: `[{"ServiceMeta" : {"version": "1.10.0"}}]`,
		},
		{
			name:        "bad-server",
			agentJSON:   `{"Config": {"Version": "1.10.0"}}`,
			serviceJSON: `[{"ServiceMeta" : {"version": "1.0.0"}}]`,
		},
		{
			name:        "no-server-version-meta",
			agentJSON:   `{"Config": {"Version": "1.10.0"}}`,
			serviceJSON: `[{"ServiceMeta" : {}}]`,
			shouldPass:  true, // logs a warning
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testVersion(t, tc)
		})
	}
}

func TestAgent_uniqueInstanceID(t *testing.T) {
	t.Parallel()

	s, err := NewTestServer(t)
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

	s, err := NewTestServer(t)
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
		Output:          LOGOUT,
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

// XXX and YYY indicate values that need replacing
var xxxHealthCheck = api.HealthCheck{
	CheckID: "XXX_ck", Name: "XXX_ck1", ServiceID: "XXX1", ServiceName: "XXX",
	Namespace: "YYY",
	Node:      "foo", Status: "passing", Type: "http",
	Definition: api.HealthCheckDefinition{
		Interval: *api.NewReadableDuration(time.Second * 2),
		Timeout:  *api.NewReadableDuration(time.Second * 5),
		HTTP:     "https://www.hashicorp.com/robots.txt",
	},
}

func testHealthChecks(ns string) string {
	hc := xxxHealthCheck
	name := "test_svc"
	if ns != "" {
		name = ns + "_svc"
	}
	hc.ServiceName, hc.ServiceID = name, name+"1"
	hc.CheckID, hc.Name = name+"_ck", name+"_ck1"
	hc.Namespace = ns
	hcs := api.HealthChecks{&hc}
	j, err := json.Marshal(hcs)
	if err != nil {
		panic(err)
	}
	return string(j)
}

func testNamespaces() string {
	ns := []*api.Namespace{{Name: "default"}, {Name: "ns1"}, {Name: "ns2"}}
	j, err := json.Marshal(ns)
	if err != nil {
		panic(err)
	}
	return string(j)
}

func TestAgent_getHealthChecks(t *testing.T) {
	// case 1: w/o namespaces
	t.Run("no-namespaces", func(t *testing.T) {
		listener, err := net.Listen("tcp", ":0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ts := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/v1/status/leader":
					fmt.Fprint(w, `"`+addr+`"`)
				case "/v1/namespaces":
					w.WriteHeader(404)
					fmt.Fprint(w, "no namespaces in OSS version")
				case "/v1/health/state/any":
					fmt.Fprint(w, testHealthChecks(""))
				default:
					// t.Log("unhandled:", r.URL.EscapedPath())
				}
			}))
		ts.Listener = listener
		ts.Start()
		defer ts.Close()

		agent := testAgent(t, func(c *Config) {
			c.HTTPAddr = addr
			c.Tag = "test"
		})
		defer agent.Shutdown()

		ourNodes := map[string]bool{"foo": true}

		ourChecks, _ := agent.getHealthChecks(0, ourNodes)
		if len(ourChecks) != 1 {
			t.Error("should be 1 checks, got", len(ourChecks))
		}
		check := ourChecks[0]
		if check.CheckID != "test_svc_ck" {
			t.Error("Wrong check id:", check.CheckID)
		}
		if check.Namespace != "" {
			t.Error("Namespace should be empty, got:", check.Namespace)
		}
	})
	// case 2: w/ namespaces
	t.Run("with-namespaces", func(t *testing.T) {
		listener, err := net.Listen("tcp", ":0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ts := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/v1/status/leader":
					fmt.Fprint(w, `"`+addr+`"`)
				case "/v1/namespaces":
					fmt.Fprint(w, testNamespaces())
				case "/v1/health/state/any":
					namespace := r.URL.Query()["ns"][0]
					if namespace == "default" {
						fmt.Fprint(w, "[]")
					}
					fmt.Fprint(w, testHealthChecks(r.URL.Query()["ns"][0]))
				default:
					// t.Log("unhandled:", r.URL.EscapedPath())
				}
			}))
		ts.Listener = listener
		ts.Start()
		defer ts.Close()

		agent := testAgent(t, func(c *Config) {
			c.HTTPAddr = addr
			c.Tag = "test"
		})
		defer agent.Shutdown()

		ourNodes := map[string]bool{"foo": true}

		ourChecks, _ := agent.getHealthChecks(0, ourNodes)
		if len(ourChecks) != 2 {
			t.Error("should be 2 checks, got", len(ourChecks))
		}
		ns1check := ourChecks[0]
		ns2check := ourChecks[1]
		if ns1check.CheckID != "ns1_svc_ck" {
			t.Error("Wrong check id:", ns1check.CheckID)
		}
		if ns2check.CheckID != "ns2_svc_ck" {
			t.Error("Wrong check id:", ns1check.CheckID)
		}
	})
}

func TestAgent_PartitionOrEmpty(t *testing.T) {
	conf, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, partition, expected string
	}{
		{"No partition", "", ""},
		{"default partition", "default", "default"},
		{"admin partition", "admin", "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf.Partition = tc.partition

			agent := &Agent{
				config: conf,
			}

			assert.Equal(t, tc.expected, agent.PartitionOrEmpty())
		})
	}
}

func TestAgent_getHealthChecksWithPartition(t *testing.T) {
	testPartition := "test-partition"
	notUniqueInstanceID := "not-unique-instance-id"
	partitionQueryParamKey := "partition"
	t.Run("with-partition", func(t *testing.T) {
		listener, err := net.Listen("tcp", ":0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		testNs := map[string]bool{}
		ts := httptest.NewUnstartedServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/v1/status/leader":

					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
					fmt.Fprint(w, `"`+addr+`"`)
				case "/v1/namespaces":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
					fmt.Fprint(w, testNamespaces())
				case "/v1/health/state/any":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
					namespace := r.URL.Query()["ns"][0]
					testNs[namespace] = true
					if namespace == "default" {
						fmt.Fprint(w, "[]")
					}
					fmt.Fprint(w, testHealthChecks(r.URL.Query()["ns"][0]))
				case "/v1/agent/service/consul-esm:not-unique-instance-id":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
					// write status 404, to tell the service is not registered and proceed with registration
					w.WriteHeader(http.StatusNotFound)
				case "/v1/agent/service/register":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
					var svc api.AgentServiceRegistration
					err := json.NewDecoder(r.Body).Decode(&svc)
					require.NoError(t, err)
					assert.Equal(t, testPartition, svc.Partition)
					w.WriteHeader(http.StatusOK)
				case "/v1/kv/consul-esm/agents/consul-esm:not-unique-instance-id":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
				case "/v1/session/create":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
				case "/v1/agent/check/register":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
				case "/v1/agent/check/update/consul-esm:not-unique-instance-id:agent-ttl":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
				case "/v1/catalog/service/consul-esm":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))
				case "/v1/agent/services":
					assert.Equal(t, testPartition, r.URL.Query().Get(partitionQueryParamKey))

				default:
					t.Log("unhandled:", r.URL.EscapedPath())
				}
			}))
		ts.Listener = listener
		ts.Start()
		defer ts.Close()

		agent := testAgent(t, func(c *Config) {
			c.HTTPAddr = addr
			c.Tag = "test"
			c.Partition = testPartition
			c.InstanceID = notUniqueInstanceID
		})
		defer agent.Shutdown()

		ourNodes := map[string]bool{"foo": true}
		ourChecks, _ := agent.getHealthChecks(0, ourNodes)
		if len(ourChecks) != 2 {
			t.Error("should be 2 checks, got", len(ourChecks))
		}
		ns1check := ourChecks[0]
		ns2check := ourChecks[1]
		if ns1check.CheckID != "ns1_svc_ck" {
			t.Error("Wrong check id:", ns1check.CheckID)
		}
		if ns2check.CheckID != "ns2_svc_ck" {
			t.Error("Wrong check id:", ns1check.CheckID)
		}

		// test the state API is called for each namespace in an agent's partition
		assert.Len(t, testNs, 3)
	})
}

func TestAgent_HasPartition(t *testing.T) {
	conf, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, partition, expected string
	}{
		{"No partition", "", ""},
		{"default partition", "default", ""},
		{"admin partition", "admin", "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf.Partition = tc.partition

			agent := &Agent{
				config: conf,
			}

			actualPartition := ""
			agent.HasPartition(func(partition string) {
				actualPartition = partition
			})

			assert.Equal(t, tc.expected, actualPartition)
		})
	}
}

func TestAgent_ConsulQueryOptions(t *testing.T) {
	conf, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, partition, expected string
	}{
		{"No partition", "", ""},
		{"default partition", "default", ""},
		{"admin partition", "admin", "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf.Partition = tc.partition

			agent := &Agent{
				config: conf,
			}

			opts := agent.ConsulQueryOption()

			assert.Equal(t, tc.expected, opts.Partition)
		})
	}
}

func TestAgent_recordHealthCheckMetrics(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	conf, err := DefaultConfig()
	require.NoError(t, err)

	agent := &Agent{
		config: conf,
		logger: logger,
	}

	t.Run("handles empty inputs gracefully", func(t *testing.T) {
		sink := setupMetricsSink()

		start := time.Now()
		nodes := map[string]bool{}
		checks := api.HealthChecks{}

		agent.recordHealthCheckMetrics(start, nodes, checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")

			// Use the latest interval (there might be multiple from global metrics)
			intv := intervals[len(intervals)-1]

			durationKey := "consul-esm.esm.checks.fetch_and_update.duration"
			_, ok := intv.Samples[durationKey]
			require.True(r, ok, fmt.Sprintf("did not find duration sample %q", durationKey))

			countKey := "consul-esm.esm.checks.count"
			countMetric, ok := intv.Gauges[countKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", countKey))
			require.Equal(r, float32(0), countMetric.Value)

			healthyKey := "consul-esm.esm.checks.healthy"
			healthyMetric, ok := intv.Gauges[healthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", healthyKey))
			require.Equal(r, float32(0), healthyMetric.Value)

			unhealthyKey := "consul-esm.esm.checks.unhealthy"
			unhealthyMetric, ok := intv.Gauges[unhealthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", unhealthyKey))
			require.Equal(r, float32(0), unhealthyMetric.Value)

			nodesKey := "consul-esm.esm.nodes.monitored"
			nodesMetric, ok := intv.Gauges[nodesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", nodesKey))
			require.Equal(r, float32(0), nodesMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(0), servicesMetric.Value)
		})
	})

	t.Run("handles duplicate service IDs correctly", func(t *testing.T) {
		sink := setupMetricsSink()

		start := time.Now()
		nodes := map[string]bool{
			"node1": true,
			"node2": true,
		}
		checks := api.HealthChecks{
			{Node: "node1", ServiceID: "web", Status: api.HealthPassing},
			{Node: "node1", ServiceID: "web", Status: api.HealthPassing},
			{Node: "node2", ServiceID: "web", Status: api.HealthCritical},
			{Node: "node2", ServiceID: "", Status: api.HealthPassing},
		}

		agent.recordHealthCheckMetrics(start, nodes, checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")

			intv := intervals[len(intervals)-1]

			countKey := "consul-esm.esm.checks.count"
			countMetric, ok := intv.Gauges[countKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", countKey))
			require.Equal(r, float32(4), countMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(2), servicesMetric.Value)
		})
	})
}

func TestAgent_updateCheckMetrics(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "consul-esm",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	agent := &Agent{
		logger: logger,
	}

	t.Run("calculates check counts correctly", func(t *testing.T) {
		sink := setupMetricsSink()

		checks := api.HealthChecks{
			{Status: api.HealthPassing},
			{Status: api.HealthPassing},
			{Status: api.HealthCritical},
			{Status: api.HealthWarning},
		}

		agent.updateCheckMetrics(checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			countKey := "consul-esm.esm.checks.count"
			countMetric, ok := intv.Gauges[countKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", countKey))
			require.Equal(r, float32(4), countMetric.Value)

			// Check healthy count (passing)
			healthyKey := "consul-esm.esm.checks.healthy"
			healthyMetric, ok := intv.Gauges[healthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", healthyKey))
			require.Equal(r, float32(2), healthyMetric.Value)

			// Check unhealthy count (critical + warning)
			unhealthyKey := "consul-esm.esm.checks.unhealthy"
			unhealthyMetric, ok := intv.Gauges[unhealthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", unhealthyKey))
			require.Equal(r, float32(2), unhealthyMetric.Value)
		})
	})

	t.Run("handles empty check list", func(t *testing.T) {
		sink := setupMetricsSink()

		checks := api.HealthChecks{}

		agent.updateCheckMetrics(checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			countKey := "consul-esm.esm.checks.count"
			countMetric, ok := intv.Gauges[countKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", countKey))
			require.Equal(r, float32(0), countMetric.Value)

			healthyKey := "consul-esm.esm.checks.healthy"
			healthyMetric, ok := intv.Gauges[healthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", healthyKey))
			require.Equal(r, float32(0), healthyMetric.Value)

			unhealthyKey := "consul-esm.esm.checks.unhealthy"
			unhealthyMetric, ok := intv.Gauges[unhealthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", unhealthyKey))
			require.Equal(r, float32(0), unhealthyMetric.Value)
		})
	})

	t.Run("handles nil check list", func(t *testing.T) {
		sink := setupMetricsSink()

		agent.updateCheckMetrics(nil)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			countKey := "consul-esm.esm.checks.count"
			countMetric, ok := intv.Gauges[countKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", countKey))
			require.Equal(r, float32(0), countMetric.Value)

			healthyKey := "consul-esm.esm.checks.healthy"
			healthyMetric, ok := intv.Gauges[healthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", healthyKey))
			require.Equal(r, float32(0), healthyMetric.Value)

			unhealthyKey := "consul-esm.esm.checks.unhealthy"
			unhealthyMetric, ok := intv.Gauges[unhealthyKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", unhealthyKey))
			require.Equal(r, float32(0), unhealthyMetric.Value)
		})
	})
}

func TestAgent_updateServiceMetrics(t *testing.T) {

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "consul-esm",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	agent := &Agent{
		logger: logger,
	}

	t.Run("counts unique services correctly", func(t *testing.T) {
		sink := setupMetricsSink()

		nodes := map[string]bool{
			"node1": true,
			"node2": true,
		}

		checks := api.HealthChecks{
			{Node: "node1", ServiceID: "service1"},
			{Node: "node1", ServiceID: "service1"}, // Duplicate
			{Node: "node2", ServiceID: "service2"},
			{Node: "node2", ServiceID: ""}, // No service
		}

		agent.updateServiceMetrics(nodes, checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			nodesKey := "consul-esm.esm.nodes.monitored"
			nodesMetric, ok := intv.Gauges[nodesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", nodesKey))
			require.Equal(r, float32(2), nodesMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(2), servicesMetric.Value)
		})
	})

	t.Run("handles empty inputs", func(t *testing.T) {
		sink := setupMetricsSink()

		nodes := map[string]bool{}
		checks := api.HealthChecks{}

		agent.updateServiceMetrics(nodes, checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			nodesKey := "consul-esm.esm.nodes.monitored"
			nodesMetric, ok := intv.Gauges[nodesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", nodesKey))
			require.Equal(r, float32(0), nodesMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(0), servicesMetric.Value)
		})
	})

	t.Run("handles nil inputs", func(t *testing.T) {
		sink := setupMetricsSink()

		agent.updateServiceMetrics(nil, nil)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			nodesKey := "consul-esm.esm.nodes.monitored"
			nodesMetric, ok := intv.Gauges[nodesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", nodesKey))
			require.Equal(r, float32(0), nodesMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(0), servicesMetric.Value)
		})
	})

	t.Run("handles checks without services", func(t *testing.T) {
		sink := setupMetricsSink()

		nodes := map[string]bool{
			"node1": true,
		}

		checks := api.HealthChecks{
			{Node: "node1", ServiceID: ""},
			{Node: "node1", ServiceID: ""},
		}

		agent.updateServiceMetrics(nodes, checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			nodesKey := "consul-esm.esm.nodes.monitored"
			nodesMetric, ok := intv.Gauges[nodesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", nodesKey))
			require.Equal(r, float32(1), nodesMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(0), servicesMetric.Value)
		})
	})

	t.Run("handles same service on different nodes", func(t *testing.T) {
		sink := setupMetricsSink()

		nodes := map[string]bool{
			"node1": true,
			"node2": true,
		}

		checks := api.HealthChecks{
			{Node: "node1", ServiceID: "web"},
			{Node: "node2", ServiceID: "web"},
		}

		agent.updateServiceMetrics(nodes, checks)

		retry.Run(t, func(r *retry.R) {
			intervals := sink.Data()
			require.NotEmpty(r, intervals, "Expected at least one metrics interval")
			intv := intervals[len(intervals)-1]

			nodesKey := "consul-esm.esm.nodes.monitored"
			nodesMetric, ok := intv.Gauges[nodesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", nodesKey))
			require.Equal(r, float32(2), nodesMetric.Value)

			servicesKey := "consul-esm.esm.services.monitored"
			servicesMetric, ok := intv.Gauges[servicesKey]
			require.True(r, ok, fmt.Sprintf("did not find the key %q", servicesKey))
			require.Equal(r, float32(2), servicesMetric.Value)
		})
	})
}

func TestAgent_getPrometheusDefs(t *testing.T) {
	t.Run("returns correct definitions without prefix", func(t *testing.T) {
		config, err := DefaultConfig()
		require.NoError(t, err)
		config.Telemetry.MetricsPrefix = ""

		gauges, summaries := getPrometheusDefs(config)

		// Verify we get the expected number of gauge definitions
		expectedGaugeCount := len(AgentGauges) + len(MonitoredGauges) + len(LeaderGauges)
		require.Len(t, gauges, expectedGaugeCount, "Should have correct number of gauge definitions")

		// Verify we get the expected number of summary definitions
		expectedSummaryCount := len(ChecksSummary)
		require.Len(t, summaries, expectedSummaryCount, "Should have correct number of summary definitions")

		// Check specific gauge definitions are present
		gaugeNames := make(map[string]bool)
		for _, gauge := range gauges {
			gaugeNames[strings.Join(gauge.Name, ".")] = true
		}

		expectedGauges := []string{
			"esm.agent.isLeader",
			"esm.nodes.monitored",
			"esm.services.monitored",
			"esm.agents.healthy",
		}

		for _, expected := range expectedGauges {
			require.True(t, gaugeNames[expected], "Should contain gauge %s", expected)
		}

		// Check specific summary definitions are present
		summaryNames := make(map[string]bool)
		for _, summary := range summaries {
			summaryNames[strings.Join(summary.Name, ".")] = true
		}

		expectedSummaries := []string{
			"esm.checks.fetch_and_update.duration",
		}

		for _, expected := range expectedSummaries {
			require.True(t, summaryNames[expected], "Should contain summary %s", expected)
		}
	})

	t.Run("applies metrics prefix correctly", func(t *testing.T) {
		config, err := DefaultConfig()
		require.NoError(t, err)
		config.Telemetry.MetricsPrefix = "test-prefix"

		gauges, summaries := getPrometheusDefs(config)

		// Verify all gauges have the prefix
		for _, gauge := range gauges {
			require.True(t, len(gauge.Name) > 0, "Gauge should have a name")
			require.Equal(t, "test-prefix", gauge.Name[0], "Gauge should start with metrics prefix")
		}

		// Verify all summaries have the prefix
		for _, summary := range summaries {
			require.True(t, len(summary.Name) > 0, "Summary should have a name")
			require.Equal(t, "test-prefix", summary.Name[0], "Summary should start with metrics prefix")
		}
	})

	t.Run("handles empty metrics prefix", func(t *testing.T) {
		config, err := DefaultConfig()
		require.NoError(t, err)
		config.Telemetry.MetricsPrefix = ""

		gauges, summaries := getPrometheusDefs(config)

		// Verify gauges don't have unexpected prefixes
		for _, gauge := range gauges {
			require.True(t, len(gauge.Name) > 0, "Gauge should have a name")
			require.Equal(t, "esm", gauge.Name[0], "Gauge should start with 'esm' when no prefix is set")
		}

		// Verify summaries don't have unexpected prefixes
		for _, summary := range summaries {
			require.True(t, len(summary.Name) > 0, "Summary should have a name")
			require.Equal(t, "esm", summary.Name[0], "Summary should start with 'esm' when no prefix is set")
		}
	})

	t.Run("gauge definitions have help text", func(t *testing.T) {
		config, err := DefaultConfig()
		require.NoError(t, err)

		gauges, _ := getPrometheusDefs(config)

		for _, gauge := range gauges {
			require.NotEmpty(t, gauge.Help, "Gauge %s should have help text", strings.Join(gauge.Name, "."))
		}
	})

	t.Run("summary definitions have help text", func(t *testing.T) {
		config, err := DefaultConfig()
		require.NoError(t, err)

		_, summaries := getPrometheusDefs(config)

		for _, summary := range summaries {
			require.NotEmpty(t, summary.Help, "Summary %s should have help text", strings.Join(summary.Name, "."))
		}
	})
}
