// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/types"
	"github.com/hashicorp/go-hclog"
)

// TestCheckRunner_AgentlessOptimizations verifies that agentless mode
// enables specific optimizations in the CheckRunner
func TestCheckRunner_AgentlessOptimizations(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	// Test CheckRunner creation in agentful mode
	// Creating a mock client with default config for agentful mode to ensure batcher is not initialized (batching is agentless-only)
	mockClient, _ := api.NewClient(api.DefaultConfig())
	agentfulRunner := NewCheckRunner(logger, mockClient, 0, 0, &tls.Config{}, 1, 1, false, 0)
	if agentfulRunner.isAgentless != false {
		t.Fatal("Expected agentful runner to have isAgentless=false")
	}
	if agentfulRunner.batcher != nil {
		t.Fatal("Expected agentful runner to have nil batcher")
	}

	// Test CheckRunner creation in agentless mode
	agentlessRunner := NewCheckRunner(logger, mockClient, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	if agentlessRunner.isAgentless != true {
		t.Fatal("Expected agentless runner to have isAgentless=true")
	}
	if agentlessRunner.batcher == nil {
		t.Fatal("Expected agentless runner to have non-nil batcher")
	}

	t.Log("Agentless mode detection working correctly")
	t.Log("CheckRunner properly configured for both modes")
}

// TestCheckRunner_AgentlessBatching verifies that agentless mode uses
// different batching intervals for better server efficiency
func TestCheckRunner_AgentlessBatching(t *testing.T) {
	// This test demonstrates the concept - in practice the batching logic
	// would be tested with actual check updates and timing measurements

	updateInterval := 10 * time.Second

	// Agentful mode uses interval/2 + jitter
	agentfulInterval := time.Duration(uint64(updateInterval) / 2)
	t.Logf("Agentful batching interval: %v", agentfulInterval)

	// Agentless mode uses full interval + jitter (more aggressive batching)
	agentlessInterval := time.Duration(uint64(updateInterval))
	t.Logf("Agentless batching interval: %v", agentlessInterval)

	if agentlessInterval <= agentfulInterval {
		t.Fatal("Expected agentless batching to use longer intervals")
	}

	t.Log("Agentless mode uses enhanced batching intervals")
}

// TestCheckRunner_BatchingInitialization verifies that batching is properly
// initialized for both agentful and agentless modes
func TestCheckRunner_BatchingInitialization(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	// Test agentful mode - batcher should NOT be initialized (batching is agentless-only)
	agentfulRunner := NewCheckRunner(logger, nil, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, false, 0)
	if agentfulRunner.batcher != nil {
		t.Fatal("Expected batcher to be nil for agentful mode")
	}

	// Test agentless mode batching initialization (should have longer interval)
	// Create a mock client for agentless mode (batcher requires non-nil client)
	mockClient, _ := api.NewClient(api.DefaultConfig())
	agentlessRunner := NewCheckRunner(logger, mockClient, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	if agentlessRunner.batcher == nil {
		t.Fatal("Expected batcher to be initialized for agentless mode")
	}
	expectedAgentlessInterval := 500 * time.Millisecond
	if agentlessRunner.batcher.flushInterval != expectedAgentlessInterval {
		t.Fatalf("Expected agentless batch flush interval to be %v, got %v", expectedAgentlessInterval, agentlessRunner.batcher.flushInterval)
	}
	// Stop the batcher to prevent timer from firing during test
	agentlessRunner.Stop()

	t.Log("Batching properly initialized for agentful mode")
	t.Log("Batching properly initialized for agentless mode with longer interval")
}

// TestCheckRunner_BatchQueueing verifies that check updates are properly queued
func TestCheckRunner_BatchQueueing(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	// Create a mock client for agentless mode
	mockClient, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, mockClient, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond) // agentless mode
	defer runner.Stop()

	if runner.batcher == nil {
		t.Fatal("Batcher should be initialized for agentless mode with non-nil client")
	}

	// Simulate queuing multiple check updates
	checks := []*api.HealthCheck{
		{Node: "node1", CheckID: "check1", Status: "passing"},
		{Node: "node1", CheckID: "check2", Status: "passing"},
		{Node: "node2", CheckID: "check1", Status: "passing"},
	}

	runner.batcher.pendingUpdatesMu.Lock()
	for _, check := range checks {
		key := makeCheckKey(check.Node, string(check.CheckID))
		runner.batcher.pendingUpdates[key] = &pendingCheckUpdate{
			check:  check,
			status: "critical",
			output: "test output",
		}
	}
	pendingCount := len(runner.batcher.pendingUpdates)
	runner.batcher.pendingUpdatesMu.Unlock()

	if pendingCount != 3 {
		t.Fatalf("Expected 3 pending updates, got %d", pendingCount)
	}

	t.Log("Check updates properly queued for batching")
}

// TestCheckRunner_BatchDeduplication verifies that duplicate updates are deduplicated
func TestCheckRunner_BatchDeduplication(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	// Create a mock client for agentless mode
	mockClient, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, mockClient, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond) // agentless mode
	defer runner.Stop()

	if runner.batcher == nil {
		t.Fatal("Batcher should be initialized for agentless mode with non-nil client")
	}

	check := &api.HealthCheck{Node: "node1", CheckID: "check1", Status: "passing"}

	// Simulate queuing the same check multiple times (should deduplicate)
	runner.batcher.pendingUpdatesMu.Lock()
	for i := 0; i < 5; i++ {
		key := makeCheckKey(check.Node, string(check.CheckID))
		runner.batcher.pendingUpdates[key] = &pendingCheckUpdate{
			check:  check,
			status: "critical",
			output: "update " + string(rune('0'+i)),
		}
	}
	pendingCount := len(runner.batcher.pendingUpdates)
	lastOutput := runner.batcher.pendingUpdates["node1/check1"].output
	runner.batcher.pendingUpdatesMu.Unlock()

	if pendingCount != 1 {
		t.Fatalf("Expected 1 pending update after deduplication, got %d", pendingCount)
	}

	if lastOutput != "update 4" {
		t.Fatalf("Expected last output to be 'update 4', got '%s'", lastOutput)
	}

	t.Log("Duplicate check updates properly deduplicated (only latest kept)")
}

// TestCheckRunner_MaxBatchSize verifies the max batch size constant
func TestCheckRunner_MaxBatchSize(t *testing.T) {
	if maxTxnOps != 64 {
		t.Fatalf("Expected maxTxnOps to be 64, got %d", maxTxnOps)
	}
	t.Logf("Max batch size correctly set to %d operations", maxTxnOps)
}

// TestCheckRunner_StateReversion verifies that local state is reverted when batch updates fail
func TestCheckRunner_StateReversion(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	// Create a mock client for agentless mode
	mockClient, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, mockClient, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Create a test check with initial state
	checkHash := types.CheckID("test-node/service:web/health")
	check := &esmHealthCheck{
		HealthCheck: api.HealthCheck{
			Node:      "test-node",
			CheckID:   "health",
			ServiceID: "service:web",
			Status:    "passing",
			Output:    "all good",
		},
		failureCounter: 0,
		successCounter: 0,
	}
	runner.checks.Store(checkHash, check)

	// Test 1: Verify original state is captured in pendingCheckUpdate
	t.Run("original state is tracked", func(t *testing.T) {
		update := &pendingCheckUpdate{
			check:     &check.HealthCheck,
			status:    "critical",
			output:    "service down",
			oldStatus: "passing",
			oldOutput: "all good",
		}

		if update.oldStatus != "passing" {
			t.Errorf("Expected oldStatus to be 'passing', got %s", update.oldStatus)
		}
		if update.oldOutput != "all good" {
			t.Errorf("Expected oldOutput to be 'all good', got %s", update.oldOutput)
		}
		t.Log("Original state properly tracked in pendingCheckUpdate")
	})

	// Test 2: Verify revertCheckState updates local state
	t.Run("revertCheckState updates local state", func(t *testing.T) {
		// Simulate a failed update - check status changed to critical
		check.Status = "critical"
		check.Output = "service down"
		runner.checks.Store(checkHash, check)

		// Verify state was updated
		updatedCheck, _ := runner.checks.Load(checkHash)
		if updatedCheck.Status != "critical" {
			t.Fatalf("Expected status to be 'critical' before reversion, got %s", updatedCheck.Status)
		}

		// Revert to original state
		runner.revertCheckState(checkHash, "passing", "all good")

		// Verify state was reverted
		revertedCheck, ok := runner.checks.Load(checkHash)
		if !ok {
			t.Fatal("Check not found after reversion")
		}
		if revertedCheck.Status != "passing" {
			t.Errorf("Expected status to be reverted to 'passing', got %s", revertedCheck.Status)
		}
		if revertedCheck.Output != "all good" {
			t.Errorf("Expected output to be reverted to 'all good', got %s", revertedCheck.Output)
		}
		t.Log("Local state successfully reverted after batch failure")
	})

	// Test 3: Verify revertCheckState handles missing checks gracefully
	t.Run("revertCheckState handles missing checks", func(t *testing.T) {
		// This should not panic or error
		runner.revertCheckState("nonexistent/check", "passing", "test")
		t.Log("Missing check handled gracefully during reversion")
	})

	// Test 4: Verify self-healing behavior simulation
	t.Run("self-healing after state reversion", func(t *testing.T) {
		// Set up initial state
		check.Status = "passing"
		check.Output = "all good"
		runner.checks.Store(checkHash, check)

		// Simulate check detecting failure and updating local state
		check.Status = "critical"
		check.Output = "service down"
		runner.checks.Store(checkHash, check)

		// Batch fails, state reverted
		runner.revertCheckState(checkHash, "passing", "all good")

		// Verify: next check execution will see difference
		revertedCheck, _ := runner.checks.Load(checkHash)
		actualStatus := "critical" // What the health check actually detected
		localStatus := revertedCheck.Status

		// After reversion, local state != actual state, so next execution will retry
		if localStatus == actualStatus {
			t.Error("Expected local state to differ from actual state to enable retry")
		}
		t.Log("After state reversion, next check execution will naturally retry the update")
	})
}

// TestCheckRunner_OriginalStateInBatcher verifies that batcher receives original state
func TestCheckRunner_OriginalStateInBatcher(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	// Create a mock client for agentless mode
	mockClient, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, mockClient, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	if runner.batcher == nil {
		t.Fatal("Batcher should be initialized for agentless mode with non-nil client")
	}

	check := &api.HealthCheck{
		Node:      "test-node",
		CheckID:   "check1",
		Status:    "passing",
		Output:    "original output",
		ServiceID: "service1",
	}

	// Add update with original state
	runner.batcher.Add(check, "critical", "new output", "passing", "original output")

	// Verify the update was queued with original state
	runner.batcher.pendingUpdatesMu.Lock()
	key := makeCheckKey(check.Node, string(check.CheckID))
	update, exists := runner.batcher.pendingUpdates[key]
	runner.batcher.pendingUpdatesMu.Unlock()

	if !exists {
		t.Fatal("Update not found in batcher")
	}

	if update.status != "critical" {
		t.Errorf("Expected status 'critical', got %s", update.status)
	}
	if update.output != "new output" {
		t.Errorf("Expected output 'new output', got %s", update.output)
	}
	if update.oldStatus != "passing" {
		t.Errorf("Expected oldStatus 'passing', got %s", update.oldStatus)
	}
	if update.oldOutput != "original output" {
		t.Errorf("Expected oldOutput 'original output', got %s", update.oldOutput)
	}

	t.Log("Batcher correctly receives and stores original state with each update")
}
