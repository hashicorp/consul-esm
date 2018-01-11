package main

import (
	"log"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/testutil"
	"github.com/hashicorp/consul/testutil/retry"
	"github.com/pascaldekloe/goe/verify"
)

func TestCoordinate_updateNodeCoordinate(t *testing.T) {
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

	agent := &Agent{client: client}
	agent.updateNodeCoordinate(&api.Node{Node: "external"}, 1*time.Second)

	var coords []*api.CoordinateEntry
	retry.RunWith(&retry.Timer{Timeout: 15 * time.Second, Wait: time.Second}, t, func(r *retry.R) {
		coords, _, err = client.Coordinate().Node("external", nil)
		if err != nil {
			r.Fatal(err)
		}
	})
}

func TestCoordinate_updateNodeCheck(t *testing.T) {
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

	agent := &Agent{client: client}
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
	expected := api.HealthChecks{
		{
			Node:    "external",
			CheckID: externalCheckName,
			Name:    "External Node Status",
			Status:  api.HealthCritical,
			Output:  NodeCriticalStatus,
		},
	}
	verify.Values(t, "", checks, expected)

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
	expected[0].Status = api.HealthPassing
	expected[0].Output = NodeAliveStatus
	checks, _, err = client.Health().Node("external", nil)
	if err != nil {
		t.Fatal(err)
	}
	verify.Values(t, "", checks, expected)
}

func TestCoordinate_reapFailedNode(t *testing.T) {
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
		logger: log.New(os.Stdout, "", log.LstdFlags),
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
	expected := api.HealthChecks{
		{
			Node:    "external",
			CheckID: externalCheckName,
			Name:    "External Node Status",
			Status:  api.HealthCritical,
			Output:  NodeCriticalStatus,
		},
	}
	verify.Values(t, "", checks, expected)

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
