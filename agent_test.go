package main

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/testutil"
	"github.com/hashicorp/consul/testutil/retry"
	"github.com/pascaldekloe/goe/verify"
)

func testAgent(t *testing.T, cb func(*Config), agentCb func(*Agent)) *Agent {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	conf := DefaultConfig()
	conf.CoordinateUpdateInterval = 200 * time.Millisecond
	if cb != nil {
		cb(conf)
	}

	MaxRTT = 500 * time.Millisecond
	retryTime = 200 * time.Millisecond

	agent, err := NewAgent(conf, logger)
	if err != nil {
		t.Fatal(err)
	}
	if agentCb != nil {
		agentCb(agent)
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
	s, err := testutil.NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	agent := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.Tag = "test"
	}, nil)
	defer agent.Shutdown()

	// Lower these retry intervals
	agentTTL = 100 * time.Millisecond
	retryTime = 100 * time.Millisecond
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

// shardWatcher provides a hook for intercepting the computeWatchedNodes updates
// in order to check the value of them.
type shardWatcher struct {
	healthCh chan map[string]bool
	pingCh   chan []*api.Node
}

func newShardWatcher() *shardWatcher {
	return &shardWatcher{
		healthCh: make(chan map[string]bool, 1),
		pingCh:   make(chan []*api.Node, 1),
	}
}

func (s *shardWatcher) watchedNodeFunc(healthUpdate map[string]bool, pingUpdate []*api.Node) {
	s.healthCh <- healthUpdate
	s.pingCh <- pingUpdate
}

func (s *shardWatcher) verifyUpdates(t *testing.T, expectedHealthNodes, expectedPingNodes []string) {
	retry.Run(t, func(r *retry.R) {
		healthNodes := <-s.healthCh
		pingNodes := <-s.pingCh

		var actualHealthNodes []string
		for s, _ := range healthNodes {
			actualHealthNodes = append(actualHealthNodes, s)
		}
		sort.Strings(actualHealthNodes)
		var actualPingNodes []string
		for _, node := range pingNodes {
			actualPingNodes = append(actualPingNodes, node.Node)
		}

		verify.Values(r, "", actualHealthNodes, expectedHealthNodes)
		verify.Values(r, "", actualPingNodes, expectedPingNodes)
	})
}

func TestAgent_rebalanceHealthWatches(t *testing.T) {
	t.Parallel()
	s, err := testutil.NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}
	// Register 3 external nodes.
	for _, nodeName := range []string{"node1", "node2", "node3"} {
		meta := map[string]string{"external-node": "true"}
		if nodeName == "node2" {
			meta["external-probe"] = "true"
		}
		_, err := client.Catalog().Register(&api.CatalogRegistration{
			Node:       nodeName,
			Address:    "127.0.0.1",
			Datacenter: "dc1",
			NodeMeta:   meta,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Register one ESM agent to start.
	agentWatcher1 := newShardWatcher()
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent1"
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher1.watchedNodeFunc
	})
	defer agent1.Shutdown()

	// agent1 should be watching all nodes, only node2 has external-probe set
	agentWatcher1.verifyUpdates(t, []string{"node1", "node2", "node3"}, []string{"node2"})

	// Add a 2nd ESM agent.
	agentWatcher2 := newShardWatcher()
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent2"
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher2.watchedNodeFunc
	})
	defer agent2.Shutdown()

	// The node watches should be divided amongst node1/node2 now.
	agentWatcher1.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agentWatcher2.verifyUpdates(t, []string{"node2"}, []string{"node2"})

	// Add a 3rd ESM agent.
	agentWatcher3 := newShardWatcher()
	agent3 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent3"
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher3.watchedNodeFunc
	})
	defer agent3.Shutdown()

	// Each agent should have one node to watch.
	agentWatcher1.verifyUpdates(t, []string{"node1"}, []string{})
	agentWatcher2.verifyUpdates(t, []string{"node2"}, []string{"node2"})
	agentWatcher3.verifyUpdates(t, []string{"node3"}, []string{})

	// Shut down agent1.
	agent1.Shutdown()

	// Agents 2 and 3 should have re-divided the nodes.
	agentWatcher2.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agentWatcher3.verifyUpdates(t, []string{"node2"}, []string{"node2"})

	// Register a 4th external node.
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "node4",
		Address:    "127.0.0.1",
		Datacenter: "dc1",
		NodeMeta: map[string]string{
			"external-node": "true"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Agents 2 and 3 should each have 2 nodes to watch
	agentWatcher2.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agentWatcher3.verifyUpdates(t, []string{"node2", "node4"}, []string{"node2"})
}

func TestAgent_divideCoordinates(t *testing.T) {
	if os.Getenv("TRAVIS") == "true" {
		t.Skip("skip this test in Travis as pings aren't supported")
	}

	t.Parallel()
	s, err := testutil.NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	// Register 2 external nodes with external-probe = true.
	for _, nodeName := range []string{"node1", "node2"} {
		meta := map[string]string{
			"external-node":  "true",
			"external-probe": "true",
		}
		_, err := client.Catalog().Register(&api.CatalogRegistration{
			Node:       nodeName,
			Address:    "127.0.0.1",
			Datacenter: "dc1",
			NodeMeta:   meta,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Register two ESM agents.
	MaxRTT = 500 * time.Millisecond
	retryTime = 200 * time.Millisecond
	agentWatcher1 := newShardWatcher()
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent1"
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher1.watchedNodeFunc
	})
	defer agent1.Shutdown()

	agentWatcher2 := newShardWatcher()
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent2"
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher2.watchedNodeFunc
	})
	defer agent2.Shutdown()

	// Make sure the nodes get divided up correctly for watches.
	agentWatcher1.verifyUpdates(t, []string{"node1"}, []string{"node1"})
	agentWatcher2.verifyUpdates(t, []string{"node2"}, []string{"node2"})

	// Wait for the nodes to get probed and set to healthy.
	for _, node := range []string{"node1", "node2"} {
		retry.Run(t, func(r *retry.R) {
			expected := api.HealthChecks{
				{
					Node:    node,
					CheckID: externalCheckName,
					Name:    "External Node Status",
					Status:  api.HealthPassing,
					Output:  NodeAliveStatus,
				},
			}
			checks, _, err := client.Health().Node(node, nil)
			if err != nil {
				r.Fatal(err)
			}
			verify.Values(r, "", checks, expected)
		})
	}

	// Register a 3rd external node.
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "node3",
		Address:    "127.0.0.1",
		Datacenter: "dc1",
		NodeMeta: map[string]string{
			"external-node":  "true",
			"external-probe": "true",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for node3 to get picked up and set to healthy.
	agentWatcher1.verifyUpdates(t, []string{"node1", "node3"}, []string{"node1", "node3"})
	agentWatcher2.verifyUpdates(t, []string{"node2"}, []string{"node2"})
	retry.Run(t, func(r *retry.R) {
		expected := api.HealthChecks{
			{
				Node:    "node3",
				CheckID: externalCheckName,
				Name:    "External Node Status",
				Status:  api.HealthPassing,
				Output:  NodeAliveStatus,
			},
		}
		checks, _, err := client.Health().Node("node3", nil)
		if err != nil {
			r.Fatal(err)
		}
		verify.Values(r, "", checks, expected)
	})
}

func TestAgent_divideHealthChecks(t *testing.T) {
	t.Parallel()
	s, err := testutil.NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	// Register 2 external nodes with health checks.
	for _, nodeName := range []string{"node1", "node2"} {
		_, err := client.Catalog().Register(&api.CatalogRegistration{
			Node:       nodeName,
			Address:    "127.0.0.1",
			Datacenter: "dc1",
			NodeMeta: map[string]string{
				"external-node": "true",
			},
			Check: &api.AgentCheck{
				Node:    nodeName,
				CheckID: "ext-tcp",
				Name:    "tcp-test",
				Status:  api.HealthCritical,
				Definition: api.HealthCheckDefinition{
					TCP:      s.HTTPAddr,
					Interval: api.ReadableDuration(time.Second),
				},
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Register two ESM agents.
	agentWatcher1 := newShardWatcher()
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent1"
		c.CoordinateUpdateInterval = time.Second
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher1.watchedNodeFunc
	})
	defer agent1.Shutdown()

	agentWatcher2 := newShardWatcher()
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent2"
		c.CoordinateUpdateInterval = time.Second
	}, func(a *Agent) {
		a.watchedNodeFunc = agentWatcher2.watchedNodeFunc
	})
	defer agent2.Shutdown()

	// Make sure the nodes get divided up correctly for watches.
	agentWatcher1.verifyUpdates(t, []string{"node1"}, []string{})
	agentWatcher2.verifyUpdates(t, []string{"node2"}, []string{})

	// Make sure the health has been updated to passing
	for _, node := range []string{"node1", "node2"} {
		retry.Run(t, func(r *retry.R) {
			checks, _, err := client.Health().Node(node, nil)
			if err != nil {
				r.Fatal(err)
			}
			if len(checks) != 1 || checks[0].Status != api.HealthPassing {
				r.Fatalf("bad: %v", checks[0])
			}
		})
	}

	// Register a 3rd external node.
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "node3",
		Address:    "127.0.0.1",
		Datacenter: "dc1",
		NodeMeta: map[string]string{
			"external-node": "true",
		},
		Check: &api.AgentCheck{
			Node:    "node3",
			CheckID: "ext-tcp",
			Name:    "tcp-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				TCP:      s.HTTPAddr,
				Interval: api.ReadableDuration(time.Second),
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for node3 to get picked up and set to healthy.
	agentWatcher1.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agentWatcher2.verifyUpdates(t, []string{"node2"}, []string{})
	retry.Run(t, func(r *retry.R) {
		checks, _, err := client.Health().Node("node3", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("bad: %v", checks[0])
		}
	})
}
