package main

import (
	"log"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func TestCheck_HTTP(t *testing.T) {
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

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0, false)
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
	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0, false)
	defer runner.Stop()

	// Register an external node with an initially critical tcp check.
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

	// Update the check definition with an invalid tcp url.
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

func TestCheck_Monitor(t *testing.T) {
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

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0, true)
	defer runner.Stop()

	// Register an external node with an initially critical monitor check.
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-monitor",
			Name:    "monitor-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				ScriptArgs:       []string{"echo", s.HTTPAddr},
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

	// Update the check definition with a script exit code for failing.
	nodeRegistration.Check.Definition.ScriptArgs = []string{"sh", "-c", "sleep 1 && exit 2"}
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

func TestCheck_SwitchCheckTypes(t *testing.T) {
	// checks can switch types so long as it has same node, serviceID, checkID

	// set up
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

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0, true)
	defer runner.Stop()

	// Define 3 types of nodes
	nodeMeta := map[string]string{"external-node": "true"}
	httpNode := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-check",
			Name:    "http-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				HTTP:             "http://" + s.HTTPAddr + "/v1/status/leader",
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	tcpNode := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-check",
			Name:    "tcp-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				TCP:              s.HTTPAddr,
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	monitorNode := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-check",
			Name:    "monitor-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				ScriptArgs:       []string{"echo", s.HTTPAddr},
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}

	// ordered cases such that each combination of x->y for http, tcp, and monitor are covered
	cases := []struct {
		desc                     string
		nodeRegistration         *api.CatalogRegistration
		expectedNumHTTPChecks    int
		expectedNumTCPChecks     int
		expectedNumMonitorChecks int
	}{
		{"http", httpNode, 1, 0, 0},
		{"http->tcp", tcpNode, 0, 1, 0},
		{"tcp->monitor", monitorNode, 0, 0, 1},
		{"monitor->http", httpNode, 1, 0, 0},
		{"http->monitor", monitorNode, 0, 0, 1},
		{"monitor->tcp", tcpNode, 0, 1, 0},
		{"tcp->http", httpNode, 1, 0, 0},
	}

	for _, c := range cases {
		_, err = client.Catalog().Register(c.nodeRegistration, nil)
		if err != nil {
			t.Fatal(err)
		}

		checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			t.Fatal(err)
		}

		runner.UpdateChecks(checks)

		actualNumHTTPChecks := len(runner.checksHTTP)
		actualNumTCPChecks := len(runner.checksTCP)
		actualNumMonitorChecks := len(runner.checksMonitor)

		if actualNumHTTPChecks != c.expectedNumHTTPChecks {
			t.Fatalf("'%v'. Expected %d but actual %d HTTP checks / %d TCP checks, %d Monitor checks",
				c.desc, c.expectedNumHTTPChecks, actualNumHTTPChecks, actualNumTCPChecks, actualNumMonitorChecks)
		}
		if actualNumTCPChecks != c.expectedNumTCPChecks {
			t.Fatalf("'%v'. Expected %d but actual %d TCP checks / %d HTTP checks, %d Monitor checks",
				c.desc, c.expectedNumTCPChecks, actualNumTCPChecks, actualNumHTTPChecks, actualNumMonitorChecks)
		}
		if actualNumMonitorChecks != c.expectedNumMonitorChecks {
			t.Fatalf("'%v'. Expected %d but actual %d Monitor checks / %d TCP checks, %d HTTP checks",
				c.desc, c.expectedNumMonitorChecks, actualNumMonitorChecks, actualNumTCPChecks, actualNumHTTPChecks)
		}
	}
}

func TestCheck_EnableLocalScriptChecks(t *testing.T) {
	// set up
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

	logger := log.New(LOGOUT, "", 0)
	runner := NewCheckRunner(logger, client, 0, false)
	defer runner.Stop()

	nodeMeta := map[string]string{"external-node": "true"}
	httpNode := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-check",
			Name:    "http-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				HTTP:             "http://" + s.HTTPAddr + "/v1/status/leader",
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}
	monitorNode := &api.CatalogRegistration{
		Node:       "external",
		Address:    "service.local",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:    "external",
			CheckID: "ext-check",
			Name:    "monitor-test",
			Status:  api.HealthCritical,
			Definition: api.HealthCheckDefinition{
				ScriptArgs:       []string{"echo", s.HTTPAddr},
				IntervalDuration: 50 * time.Millisecond,
			},
		},
	}

	cases := []struct {
		desc                     string
		nodeRegistration         *api.CatalogRegistration
		expectedNumChecks        int
		expectedNumMonitorChecks int
		enableLocalScriptChecks  bool
	}{
		{"Newly created monitor check should not run", monitorNode, 0, 0, false},
		{"Existing http check changes to monitor check should not run (Pt 1/2 Set up)", httpNode, 1, 0, false},
		{"Existing http check changes to monitor check should not run (Pt 2/2)", monitorNode, 0, 0, false},
		{"Monitor check when changing from enable local script checks to disable (Pt 1/2 Set up)", monitorNode, 1, 1, true},
		{"Monitor check when changing from enable local script checks to disable (Pt 2/2)", monitorNode, 0, 0, false},
	}

	for _, c := range cases {
		if _, err := client.Catalog().Register(c.nodeRegistration, nil); err != nil {
			t.Fatal(err)
		}

		checks, _, err := client.Health().State(api.HealthAny, &api.QueryOptions{NodeMeta: nodeMeta})
		if err != nil {
			t.Fatal(err)
		}

		runner.enableLocalScriptChecks = c.enableLocalScriptChecks
		runner.UpdateChecks(checks)

		if len(runner.checksMonitor) != c.expectedNumMonitorChecks {
			t.Fatalf("'%s': Expected %d monitor checks but actual %d", c.desc, c.expectedNumMonitorChecks, len(runner.checksMonitor))
		}
		if len(runner.checks) != c.expectedNumChecks {
			t.Fatalf("'%s': Expected %d checks in general but actual %d", c.desc, c.expectedNumChecks, len(runner.checks))
		}
	}
}
