package main

import (
	"log"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func TestCoordinate_updateNodeCoordinate(t *testing.T) {
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

	// Register an external node with an initially critical http check
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   map[string]string{"external-node": "true"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	agent := &Agent{client: client, logger: log.New(LOGOUT, "", log.LstdFlags)}
	agent.updateNodeCoordinate(&api.Node{Node: "external"}, 1*time.Second)

	var coords []*api.CoordinateEntry
	retry.RunWith(&retry.Timer{Timeout: 15 * time.Second, Wait: time.Second}, t, func(r *retry.R) {
		coords, _, err = client.Coordinate().Node("external", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(coords) == 0 {
			r.Fatalf("expected coords, got %v", coords)
		}
	})
}

func TestCoordinate_updateNodeCheck(t *testing.T) {
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

	// Register an external node with an initially critical http check
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   map[string]string{"external-node": "true"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	agent := &Agent{client: client, logger: log.New(LOGOUT, "", log.LstdFlags)}
	if err := agent.updateFailedNode(&api.Node{Node: "external"}, client.KV(), "testkey", nil); err != nil {
		t.Fatal(err)
	}

	// Verify the critical time was written to the right key
	kvPair, _, err := client.KV().Get("testkey", nil)
	if err != nil {
		t.Fatal(err)
	}
	var criticalStart time.Time
	if err := criticalStart.GobDecode(kvPair.Value); err != nil {
		t.Fatal(err)
	}
	if diff := time.Now().Sub(criticalStart); diff > time.Second {
		t.Fatalf("critical time should be less than 1s, got %v", diff.String())
	}

	// Verify the external check has been set correctly
	checks, _, err := client.Health().Node("external", nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := &api.HealthCheck{
		Node:    "external",
		CheckID: externalCheckName,
		Name:    "External Node Status",
		Status:  api.HealthCritical,
		Output:  NodeCriticalStatus,
	}
	if len(checks) != 1 {
		t.Fatal("Bad number of checks; wanted 1, got ", len(checks))
	}
	if err := compareHealthCheck(checks[0], expected); err != nil {
		t.Fatal(err)
	}

	// Call updateHealthyNode to reset the node's health
	if err := agent.updateHealthyNode(&api.Node{Node: "external"}, client.KV(), "testkey", kvPair); err != nil {
		t.Fatal(err)
	}

	// The critical time key should be cleared
	kvPair, _, err = client.KV().Get("testkey", nil)
	if err != nil {
		t.Fatal(err)
	}
	if kvPair != nil {
		t.Fatalf("testkey should be nil: %v", kvPair)
	}

	// Verify the health status has been updated
	expected.Status = api.HealthPassing
	expected.Output = NodeAliveStatus
	checks, _, err = client.Health().Node("external", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatal("Bad number of checks; wanted 1, got ", len(checks))
	}
	if err := compareHealthCheck(checks[0], expected); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinate_reapFailedNode(t *testing.T) {
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

	// Register an external node with an initially critical http check
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   map[string]string{"external-node": "true"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		client: client,
		config: DefaultConfig(),
		logger: log.New(LOGOUT, "", log.LstdFlags),
	}
	agent.config.NodeReconnectTimeout = 200 * time.Millisecond

	// Set the node status to failing
	if err := agent.updateFailedNode(&api.Node{Node: "external"}, client.KV(), "testkey", nil); err != nil {
		t.Fatal(err)
	}

	// Verify it's been registered as failing
	checks, _, err := client.Health().Node("external", nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := &api.HealthCheck{
		Node:        "external",
		CheckID:     externalCheckName,
		Name:        "External Node Status",
		Status:      api.HealthCritical,
		Output:      NodeCriticalStatus,
		CreateIndex: checks[0].CreateIndex,
		ModifyIndex: checks[0].ModifyIndex,
	}
	if len(checks) != 1 {
		t.Fatal("Bad number of checks; wanted 1, got ", len(checks))
	}
	if err := compareHealthCheck(checks[0], expected); err != nil {
		t.Fatal(err)
	}

	time.Sleep(agent.config.NodeReconnectTimeout)

	kvPair, _, err := client.KV().Get("testkey", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Call updateFailedNode again to reap the node
	if err := agent.updateFailedNode(&api.Node{Node: "external"}, client.KV(), "testkey", kvPair); err != nil {
		t.Fatal(err)
	}

	// The node should be deregistered
	node, _, err := client.Catalog().Node("external", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node != nil {
		t.Fatalf("bad: %v", node)
	}

	// KV key should be deleted
	kvPair, _, err = client.KV().Get("testkey", nil)
	if err != nil {
		t.Fatal(err)
	}
	if kvPair != nil {
		t.Fatalf("bad: %v", kvPair)
	}
}

func TestCoordinate_parallelPings(t *testing.T) {
	if os.Getenv("TRAVIS") == "true" {
		t.Skip("skip this test in Travis as pings aren't supported")
	}

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

	// Register 5 external nodes to be pinged.
	nodes := []string{"node1", "node2", "node3", "node4", "node5"}
	for _, nodeName := range nodes {
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

	// Register an ESM agent.
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.CoordinateUpdateInterval = 1 * time.Second
	})
	defer agent1.Shutdown()

	// Wait twice the CoordinateUpdateInterval then verify the nodes
	// all have the external health check set to healthy.
	time.Sleep(2 * agent1.config.CoordinateUpdateInterval)
	retry.Run(t, func(r *retry.R) {
		for _, node := range nodes {
			checks, _, err := client.Health().Node(node, nil)
			if err != nil {
				r.Fatal(err)
			}
			expected := &api.HealthCheck{
				Node:        node,
				CheckID:     externalCheckName,
				Name:        "External Node Status",
				Status:      api.HealthPassing,
				Output:      NodeAliveStatus,
				CreateIndex: checks[0].CreateIndex,
				ModifyIndex: checks[0].ModifyIndex,
			}
			if len(checks) != 1 {
				r.Fatal("Bad number of checks; wanted 1, got ", len(checks))
			}
			if err := compareHealthCheck(checks[0], expected); err != nil {
				r.Fatal(err)
			}
		}
	})
}
