// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// TestBatching_Integration_AgentlessMode tests batching with real Consul API calls
// in agentless mode. It verifies that multiple check updates are batched together.
func TestBatching_Integration_AgentlessMode(t *testing.T) {
	t.Parallel()

	// Start a real Consul test server
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Create a Consul API client
	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm-batch-test",
		Level:           hclog.LevelFromString("DEBUG"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	// Create CheckRunner in AGENTLESS mode with batching enabled
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 1*time.Second)
	defer runner.Stop()

	// Verify batching is enabled
	require.True(t, runner.batcher.IsEnabled(), "Batching should be enabled in agentless mode")

	// Register multiple external nodes with health checks
	numNodes := 10
	nodeMeta := map[string]string{"external-node": "true"}

	for i := 0; i < numNodes; i++ {
		nodeName := fmt.Sprintf("external-node-%d", i)
		checkID := fmt.Sprintf("health-check-%d", i)

		nodeRegistration := &api.CatalogRegistration{
			Node:       nodeName,
			Address:    fmt.Sprintf("10.0.0.%d", i+1),
			Datacenter: "dc1",
			NodeMeta:   nodeMeta,
			Check: &api.AgentCheck{
				Node:      nodeName,
				CheckID:   checkID,
				Name:      "Health Check",
				Status:    api.HealthCritical,
				ServiceID: "",
			},
		}

		_, err = client.Catalog().Register(nodeRegistration, nil)
		require.NoError(t, err, "Failed to register node %s", nodeName)
	}

	// Wait for registrations to propagate
	time.Sleep(100 * time.Millisecond)

	// Track how many times processBatchedUpdates is called
	var batchCallCount int32
	var totalUpdatesProcessed int32

	// Wrap the original process function to track calls
	originalProcessFunc := runner.batcher.processFunc
	runner.batcher.processFunc = func(updates []*pendingCheckUpdate) {
		atomic.AddInt32(&batchCallCount, 1)
		atomic.AddInt32(&totalUpdatesProcessed, int32(len(updates)))
		t.Logf("📦 Batch %d: Processing %d updates", atomic.LoadInt32(&batchCallCount), len(updates))
		originalProcessFunc(updates)
	}

	// Simulate check updates for all nodes (should be batched)
	for i := 0; i < numNodes; i++ {
		nodeName := fmt.Sprintf("external-node-%d", i)
		checkID := fmt.Sprintf("health-check-%d", i)

		check := &api.HealthCheck{
			Node:      nodeName,
			CheckID:   checkID,
			Name:      "Health Check",
			Status:    api.HealthCritical,
			ServiceID: "",
			Namespace: "",
		}

		// This should queue the update for batching (not send immediately)
		runner.handleCheckUpdate(check, api.HealthPassing, "All systems operational")
	}

	// Check that updates are queued but not yet flushed
	runner.batcher.pendingUpdatesMu.Lock()
	pendingCount := len(runner.batcher.pendingUpdates)
	runner.batcher.pendingUpdatesMu.Unlock()

	t.Logf("📊 Pending updates in queue: %d", pendingCount)
	require.Equal(t, numNodes, pendingCount, "All updates should be queued")

	// Wait for the batch to be flushed (batch flush interval = 1000ms for agentless)
	time.Sleep(1500 * time.Millisecond)

	// Verify that batching occurred
	batchCalls := atomic.LoadInt32(&batchCallCount)
	updatesProcessed := atomic.LoadInt32(&totalUpdatesProcessed)

	t.Logf("Total batch calls: %d", batchCalls)
	t.Logf("Total updates processed: %d", updatesProcessed)

	// We should have 1 batch call (all updates in one batch since < 64)
	require.Equal(t, int32(1), batchCalls, "Expected 1 batch call")
	require.Equal(t, int32(numNodes), updatesProcessed, "All updates should be processed")

	// Verify the updates made it to Consul
	retry.Run(t, func(r *retry.R) {
		for i := 0; i < numNodes; i++ {
			nodeName := fmt.Sprintf("external-node-%d", i)
			checks, _, err := client.Health().Node(nodeName, nil)
			if err != nil {
				r.Fatalf("Failed to query node %s: %v", nodeName, err)
			}

			found := false
			for _, check := range checks {
				checkID := fmt.Sprintf("health-check-%d", i)
				if check.CheckID == checkID {
					found = true
					if check.Status != api.HealthPassing {
						r.Fatalf("Node %s check status is %s, expected passing", nodeName, check.Status)
					}
					if check.Output != "All systems operational" {
						r.Fatalf("Node %s check output is %q, expected 'All systems operational'", nodeName, check.Output)
					}
				}
			}
			if !found {
				r.Fatalf("Check not found for node %s", nodeName)
			}
		}
	})

	t.Log("All checks successfully updated in Consul via batching!")
}

// TestBatching_Integration_AgentfulMode tests that batching is DISABLED in agentful mode
// and updates are sent immediately
func TestBatching_Integration_AgentfulMode(t *testing.T) {
	t.Parallel()

	// Start a real Consul test server
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Create a Consul API client
	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm-agentful-test",
		Level:           hclog.LevelFromString("DEBUG"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	// Create CheckRunner in AGENTFUL mode (batching disabled)
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, false, 0)
	defer runner.Stop()

	// Verify batching is DISABLED
	require.False(t, runner.batcher.IsEnabled(), "Batching should be disabled in agentful mode")

	// Register an external node with a health check
	nodeMeta := map[string]string{"external-node": "true"}
	nodeRegistration := &api.CatalogRegistration{
		Node:       "external-agentful",
		Address:    "10.0.0.100",
		Datacenter: "dc1",
		NodeMeta:   nodeMeta,
		Check: &api.AgentCheck{
			Node:      "external-agentful",
			CheckID:   "health-check",
			Name:      "Health Check",
			Status:    api.HealthCritical,
			ServiceID: "",
		},
	}

	_, err = client.Catalog().Register(nodeRegistration, nil)
	require.NoError(t, err)

	// Wait for registration
	time.Sleep(100 * time.Millisecond)

	// Trigger a check update (should be sent immediately, not batched)
	check := &api.HealthCheck{
		Node:      "external-agentful",
		CheckID:   "health-check",
		Name:      "Health Check",
		Status:    api.HealthCritical,
		ServiceID: "",
		Namespace: "",
	}

	runner.handleCheckUpdate(check, api.HealthPassing, "Immediate update")

	// No need to wait for batch flush - should be immediate
	time.Sleep(100 * time.Millisecond)

	// Verify the update made it to Consul immediately
	retry.Run(t, func(r *retry.R) {
		checks, _, err := client.Health().Node("external-agentful", nil)
		if err != nil {
			r.Fatalf("Failed to query node: %v", err)
		}

		found := false
		for _, c := range checks {
			if c.CheckID == "health-check" {
				found = true
				if c.Status != api.HealthPassing {
					r.Fatalf("Check status is %s, expected passing", c.Status)
				}
				if c.Output != "Immediate update" {
					r.Fatalf("Check output is %q, expected 'Immediate update'", c.Output)
				}
			}
		}
		if !found {
			r.Fatalf("Check not found")
		}
	})

	t.Log("Agentful mode: Updates sent immediately without batching!")
}

// TestBatching_Integration_MaxBatchSize tests that when more than 64 updates
// are queued, they are split into multiple batches
func TestBatching_Integration_MaxBatchSize(t *testing.T) {
	t.Parallel()

	// Start a real Consul test server
	s, err := NewTestServer(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Create a Consul API client
	client, err := api.NewClient(&api.Config{Address: s.HTTPAddr})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "consul-esm-maxbatch-test",
		Level:           hclog.LevelFromString("DEBUG"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	// Create CheckRunner in agentless mode
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 1*time.Second)
	defer runner.Stop()

	// Register 100 nodes (more than max batch size of 64)
	numNodes := 100
	nodeMeta := map[string]string{"external-node": "true"}

	for i := 0; i < numNodes; i++ {
		nodeName := fmt.Sprintf("batch-node-%d", i)
		checkID := fmt.Sprintf("check-%d", i)

		nodeRegistration := &api.CatalogRegistration{
			Node:       nodeName,
			Address:    fmt.Sprintf("10.1.0.%d", i%256),
			Datacenter: "dc1",
			NodeMeta:   nodeMeta,
			Check: &api.AgentCheck{
				Node:      nodeName,
				CheckID:   checkID,
				Name:      "Health Check",
				Status:    api.HealthCritical,
				ServiceID: "",
			},
		}

		_, err = client.Catalog().Register(nodeRegistration, nil)
		require.NoError(t, err)
	}

	time.Sleep(100 * time.Millisecond)

	// Track batch calls
	var batchCallCount int32
	var batchSizes []int

	originalProcessFunc := runner.batcher.processFunc
	runner.batcher.processFunc = func(updates []*pendingCheckUpdate) {
		atomic.AddInt32(&batchCallCount, 1)
		batchSizes = append(batchSizes, len(updates))
		t.Logf("📦 Batch %d: Processing %d updates", atomic.LoadInt32(&batchCallCount), len(updates))
		originalProcessFunc(updates)
	}

	// Queue all 100 updates
	for i := 0; i < numNodes; i++ {
		nodeName := fmt.Sprintf("batch-node-%d", i)
		checkID := fmt.Sprintf("check-%d", i)

		check := &api.HealthCheck{
			Node:      nodeName,
			CheckID:   checkID,
			Name:      "Health Check",
			Status:    api.HealthCritical,
			ServiceID: "",
			Namespace: "",
		}

		runner.handleCheckUpdate(check, api.HealthPassing, "Batch test")
	}

	// Wait for batches to flush
	time.Sleep(2 * time.Second)

	// Verify we got multiple batches
	batchCalls := atomic.LoadInt32(&batchCallCount)
	t.Logf("Total batch calls: %d", batchCalls)
	t.Logf("Batch sizes: %v", batchSizes)

	// Should have 2 batches: first 64, then remaining 36
	require.Equal(t, int32(2), batchCalls, "Expected 2 batch calls for 100 updates")

	// Verify all updates made it to Consul
	successCount := 0
	retry.Run(t, func(r *retry.R) {
		successCount = 0
		for i := 0; i < numNodes; i++ {
			nodeName := fmt.Sprintf("batch-node-%d", i)
			checks, _, err := client.Health().Node(nodeName, nil)
			if err != nil {
				continue
			}

			for _, c := range checks {
				checkID := fmt.Sprintf("check-%d", i)
				if c.CheckID == checkID && c.Status == api.HealthPassing {
					successCount++
					break
				}
			}
		}

		if successCount != numNodes {
			r.Fatalf("Only %d/%d checks updated successfully", successCount, numNodes)
		}
	})

	t.Logf("All %d checks successfully updated across %d batches!", numNodes, batchCalls)
}
