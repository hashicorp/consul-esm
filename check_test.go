package main

import (
	"testing"

	"time"

	"log"

	"os"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/testutil"
)

func TestHTTPCheck(t *testing.T) {
	s, err := testutil.NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := log.New(os.Stdout, "", 0)
	runner := NewCheckRunner(logger, client, 0)

	// Register an external node with an initially critical http check
	nodeMeta := map[string]string{"external-node": "true"}
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:     "external",
			CheckID:  "ext-http",
			Name:     "http-test",
			Status:   api.HealthCritical,
			HTTP:     "http://" + s.HTTPAddr + "/v1/status/leader",
			Interval: "50ms",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)
	time.Sleep(time.Second)

	// Make sure the health has been updated to passing
	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if len(checks) != 1 || checks[0].Status != api.HealthPassing {
		t.Fatalf("bad: %v", checks[0])
	}
}

func TestTCPCheck(t *testing.T) {
	s, err := testutil.NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := log.New(os.Stdout, "", 0)
	runner := NewCheckRunner(logger, client, 0)

	// Register an external node with an initially critical http check
	nodeMeta := map[string]string{"external-node": "true"}
	_, err = client.Catalog().Register(&api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:     "external",
			CheckID:  "ext-tcp",
			Name:     "tcp-test",
			Status:   api.HealthCritical,
			TCP:      s.HTTPAddr,
			Interval: "50ms",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if err != nil {
		t.Fatal(err)
	}

	runner.UpdateChecks(checks)
	time.Sleep(time.Second)

	// Make sure the health has been updated to passing
	checks, _, err = client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
	if len(checks) != 1 || checks[0].Status != api.HealthPassing {
		t.Fatalf("bad: %v", checks[0])
	}
}
