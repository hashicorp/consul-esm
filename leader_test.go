package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/testutil"
	"github.com/hashicorp/consul/testutil/retry"
	"github.com/pascaldekloe/goe/verify"
)

func (a *Agent) verifyUpdates(t *testing.T, expectedHealthNodes, expectedProbeNodes []string) {
	serviceID := fmt.Sprintf("%s:%s", a.config.Service, a.id)
	retry.Run(t, func(r *retry.R) {
		// Get the KV entry for this agent's node list.
		kv, _, err := a.client.KV().Get(a.kvNodeListPath()+serviceID, nil)
		if err != nil {
			r.Fatalf("error querying for node watch list: %v", err)
		}

		if kv == nil {
			r.Fatalf("nil kv entry")
		}

		var nodeList NodeWatchList
		if err := json.Unmarshal(kv.Value, &nodeList); err != nil {
			r.Fatalf("error deserializing node list: %v", err)
		}

		verify.Values(r, "", nodeList.Nodes, expectedHealthNodes)
		verify.Values(r, "", nodeList.Probes, expectedProbeNodes)

		// Now ensure the check runner is watching the correct checks.
		checks, _, err := a.client.Health().State(api.HealthAny, nil)
		if err != nil {
			r.Fatalf("error querying for health check info: %v", err)
		}

		// Combine the node lists.
		ourChecks := make(api.HealthChecks, 0)
		ourNodes := make(map[string]bool)
		for _, node := range append(expectedHealthNodes, expectedProbeNodes...) {
			ourNodes[node] = true
		}
		for _, c := range checks {
			if ourNodes[c.Node] && c.CheckID != externalCheckName {
				ourChecks = append(ourChecks, c)
			}
		}

		// Make sure the check runner is watching all the health checks on the
		// expected nodes and nothing else.
		a.checkRunner.RLock()
		defer a.checkRunner.RUnlock()
		for _, check := range ourChecks {
			hash := checkHash(check)
			if _, ok := a.checkRunner.checks[hash]; !ok {
				r.Fatalf("missing check %v", hash)
			}
		}
		if len(ourChecks) != len(a.checkRunner.checks) {
			r.Fatalf("checks do not match: %+v, %+v", ourChecks, a.checkRunner.checks)
		}
	})
}

func TestLeader_rebalanceHealthWatches(t *testing.T) {
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
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent1"
	})
	defer agent1.Shutdown()

	// agent1 should be watching all nodes, only node2 has external-probe set
	agent1.verifyUpdates(t, []string{"node1", "node3"}, []string{"node2"})

	// Add a 2nd ESM agent.
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent2"
	})
	defer agent2.Shutdown()

	// The node watches should be divided amongst node1/node2 now.
	agent1.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agent2.verifyUpdates(t, []string{}, []string{"node2"})

	// Add a 3rd ESM agent.
	agent3 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent3"
	})
	defer agent3.Shutdown()

	// Each agent should have one node to watch.
	agent1.verifyUpdates(t, []string{"node1"}, []string{})
	agent2.verifyUpdates(t, []string{}, []string{"node2"})
	agent3.verifyUpdates(t, []string{"node3"}, []string{})

	// Shut down agent1.
	agent1.Shutdown()

	// Agents 2 and 3 should have re-divided the nodes.
	agent2.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agent3.verifyUpdates(t, []string{}, []string{"node2"})

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
	agent2.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agent3.verifyUpdates(t, []string{"node4"}, []string{"node2"})
}

func TestLeader_divideCoordinates(t *testing.T) {
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
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent1"
	})
	defer agent1.Shutdown()

	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent2"
	})
	defer agent2.Shutdown()

	// Make sure the nodes get divided up correctly for watches.
	agent1.verifyUpdates(t, []string{}, []string{"node1"})
	agent2.verifyUpdates(t, []string{}, []string{"node2"})

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
	agent1.verifyUpdates(t, []string{}, []string{"node1", "node3"})
	agent2.verifyUpdates(t, []string{}, []string{"node2"})
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

func TestLeader_divideHealthChecks(t *testing.T) {
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
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent1"
		c.CoordinateUpdateInterval = time.Second
	})
	defer agent1.Shutdown()

	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.id = "agent2"
		c.CoordinateUpdateInterval = time.Second
	})
	defer agent2.Shutdown()

	// Make sure the nodes get divided up correctly for watches.
	agent1.verifyUpdates(t, []string{"node1"}, []string{})
	agent2.verifyUpdates(t, []string{"node2"}, []string{})

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
	agent1.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agent2.verifyUpdates(t, []string{"node2"}, []string{})
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
