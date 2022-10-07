package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func (a *Agent) verifyUpdates(t *testing.T, expectedHealthNodes, expectedProbeNodes []string) {
	serviceID := fmt.Sprintf("%s:%s", a.config.Service, a.id)
	retry.RunWith(&retry.Timer{Timeout: 10 * time.Second, Wait: time.Second}, t, func(r *retry.R) {
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

		bothEmpty := nodeList.Nodes == nil && len(expectedHealthNodes) == 0
		equal := reflect.DeepEqual(nodeList.Nodes, expectedHealthNodes)
		if !(bothEmpty || equal) {
			r.Fatalf("Nodes unequal: want(%v) got(%v)",
				expectedHealthNodes, nodeList.Nodes)
		}
		bothEmpty = nodeList.Probes == nil && len(expectedProbeNodes) == 0
		equal = reflect.DeepEqual(nodeList.Probes, expectedProbeNodes)
		if !(bothEmpty || equal) {
			r.Fatalf("Probes unequal: want(%v) got(%v)",
				expectedProbeNodes, nodeList.Probes)
		}

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
			hash := hashCheck(check)
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
	s, err := NewTestServer(t)
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
		c.InstanceID = "agent1"
	})
	defer agent1.Shutdown()

	// agent1 should be watching all nodes, only node2 has external-probe set
	agent1.verifyUpdates(t, []string{"node1", "node3"}, []string{"node2"})

	// Add a 2nd ESM agent.
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "agent2"
	})
	defer agent2.Shutdown()

	// The node watches should be divided amongst node1/node2 now.
	agent1.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agent2.verifyUpdates(t, []string{}, []string{"node2"})

	// Add a 3rd ESM agent.
	agent3 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "agent3"
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
			"external-node": "true",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Agents 2 and 3 should each have 2 nodes to watch
	agent2.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	agent3.verifyUpdates(t, []string{"node4"}, []string{"node2"})
}

func TestLeader_divideCoordinates(t *testing.T) {
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
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "agent1"
	})
	defer agent1.Shutdown()

	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "agent2"
	})
	defer agent2.Shutdown()

	// Make sure the nodes get divided up correctly for watches.
	agent1.verifyUpdates(t, []string{}, []string{"node1"})
	agent2.verifyUpdates(t, []string{}, []string{"node2"})

	// Wait for the nodes to get probed and set to healthy.
	for _, node := range []string{"node1", "node2"} {
		retry.Run(t, func(r *retry.R) {
			checks, _, err := client.Health().Node(node, nil)
			if err != nil {
				r.Fatal(err)
			}
			expected := &api.HealthCheck{
				Node:    node,
				CheckID: externalCheckName,
				Name:    "External Node Status",
				Status:  api.HealthPassing,
				Output:  NodeAliveStatus,
			}
			if len(checks) != 1 {
				r.Fatal("Bad number of checks; wanted 1, got ", len(checks))
			}
			if err := compareHealthCheck(checks[0], expected); err != nil {
				r.Fatal(err)
			}
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
		checks, _, err := client.Health().Node("node3", nil)
		if err != nil {
			r.Fatal(err)
		}
		expected := &api.HealthCheck{
			Node:    "node3",
			CheckID: externalCheckName,
			Name:    "External Node Status",
			Status:  api.HealthPassing,
			Output:  NodeAliveStatus,
		}
		if len(checks) != 1 {
			r.Fatal("Bad number of checks; wanted 1, got ", len(checks))
		}
		if err := compareHealthCheck(checks[0], expected); err != nil {
			r.Fatal(err)
		}
	})
}

func TestLeader_divideHealthChecks(t *testing.T) {
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
					TCP:              s.HTTPAddr,
					IntervalDuration: time.Second,
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
		c.InstanceID = "agent1"
		c.CoordinateUpdateInterval = time.Second
	})
	defer agent1.Shutdown()

	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "agent2"
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
				TCP:              s.HTTPAddr,
				IntervalDuration: time.Second,
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// We can't really tell which agent will pick up node3 with 100% certainty
	// and we've already established that rebalancing worked higher up
	//
	// agent1.verifyUpdates(t, []string{"node1", "node3"}, []string{})
	// agent2.verifyUpdates(t, []string{"node2"}, []string{})

	// Wait for node3 to get picked up and set to healthy.
	retry.RunWith(&retry.Timer{Timeout: 15 * time.Second, Wait: time.Second}, t, func(r *retry.R) {
		checks, _, err := client.Health().Node("node3", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != api.HealthPassing {
			r.Fatalf("bad: %v", checks[0])
		}
	})
}

func TestLeader_nodeLists(t *testing.T) {
	nodes := []*api.Node{
		{
			Node: "node1",
			Meta: map[string]string{"external-probe": "true"},
		},
		{
			Node: "node2",
			Meta: map[string]string{"external-probe": "false"},
		},
		{
			Node: "node3",
			Meta: map[string]string{"external-probe": "false"},
		},
	}
	insts := []*api.ServiceEntry{
		{Service: &api.AgentService{ID: "service1"}},
		{Service: &api.AgentService{ID: "service2"}},
	}
	// base test
	health, ping := nodeLists(nodes, insts)
	if len(health) != 2 {
		t.Fatalf("wrong # healthy nodes returned; want 2, got %d", len(health))
	}
	if len(ping) != 1 {
		t.Fatalf("wrong # ping nodes returned; want 1, got %d", len(ping))
	}
	// divide-by-0 test (GH-43)
	insts = []*api.ServiceEntry{}
	health, ping = nodeLists(nodes, insts)
	if len(health) != 0 || len(ping) != 0 {
		t.Fatalf("wrong # nodes returned; want 0, got %d (health), %d (ping)",
			len(health), len(ping))
	}
}

const namespacesJSON = `[
  { "Name": "default", "Description": "Builtin Default Namespace" },
  { "Name": "foo", "Description": "foo" }
]`

const healthserviceJSON = `[
  { "Service": {"ID": "one", "Namespace": "foo" } },
  { "Service": {"ID": "two", "Namespace": "default" } }
]`

func Test_namespacesList(t *testing.T) {
	testcase := ""
	ts := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch testcase {
			case "ent":
				fmt.Fprint(w, namespacesJSON)
			case "oss":
				http.NotFound(w, r)
			case "err":
				http.Error(w, "use a french-press", http.StatusTeapot)
			default:
				t.Fatal("unknown test case:", testcase)
			}
		}))
	defer ts.Close()

	// client, err := api.NewClient(&api.Config{Address: "127.0.0.1:8500"})
	client, err := api.NewClient(&api.Config{Address: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	// simulate enterprise consul
	testcase = "ent"
	nss, err := namespacesList(client)
	if err != nil {
		t.Fatal("unexpected error:", err)
	}
	if len(nss) != 2 || nss[0].Name != "default" || nss[1].Name != "foo" {
		t.Fatalf("bad value for namespace names: %#v\n", nss)
	}
	// simulate oss consul
	testcase = "oss"
	nss, err = namespacesList(client)
	if err != nil {
		t.Fatal("unexpected error:", err)
	}
	if len(nss) != 1 || nss[0].Name != "" {
		t.Fatalf("bad value for namespace names: %#v\n", len(nss))
	}
	// simulate other random error
	testcase = "err"
	nss, err = namespacesList(client)
	if err == nil {
		t.Fatal("unexpected error:", err)
	}
	if nss != nil {
		t.Fatalf("bad value for namespace names: %#v\n", len(nss))
	}
}

func Test_getServiceInstances(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			uri := r.RequestURI
			switch { // ignore anything that doesn't require a return body
			case strings.Contains(uri, "status/leader"):
				fmt.Fprint(w, `"127.0.0.1"`)
			case strings.Contains(uri, "namespace"):
				fmt.Fprint(w, namespacesJSON)
			case strings.Contains(uri, "health/service"):
				fmt.Fprint(w, healthserviceJSON)
			}
		}))
	defer ts.Close()

	agent := testAgent(t, func(c *Config) {
		c.HTTPAddr = ts.URL
		c.InstanceID = "test-agent"
	})
	defer agent.Shutdown()

	opts := &api.QueryOptions{}
	serviceInstances, err := agent.getServiceInstances(opts)
	if err != nil {
		t.Fatal(err)
	}
	// 4 because test data has 2 namespaces each with 2 services
	if len(serviceInstances) != 4 {
		t.Fatal("Wrong number of services", len(serviceInstances))
	}
	for _, si := range serviceInstances {
		sv := si.Service
		switch {
		case sv.ID == "one" && sv.Namespace == "foo":
		case sv.ID == "one" && sv.Namespace == "default":
		case sv.ID == "two" && sv.Namespace == "foo":
		case sv.ID == "two" && sv.Namespace == "default":
		default:
			t.Fatalf("Unknown service: %#v\n", si.Service)
		}
	}
}
