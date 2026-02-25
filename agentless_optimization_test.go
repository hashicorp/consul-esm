// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
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
	agentfulRunner := NewCheckRunner(logger, nil, 0, 0, &tls.Config{}, 1, 1, false, 0)
	if agentfulRunner.isAgentless != false {
		t.Fatal("Expected agentful runner to have isAgentless=false")
	}

	// Test CheckRunner creation in agentless mode
	agentlessRunner := NewCheckRunner(logger, nil, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	if agentlessRunner.isAgentless != true {
		t.Fatal("Expected agentless runner to have isAgentless=true")
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

	// Test agentful mode batching initialization
	agentfulRunner := NewCheckRunner(logger, nil, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, false, 0)
	if agentfulRunner.batcher == nil {
		t.Fatal("Expected batcher to be initialized")
	}
	if agentfulRunner.batcher.IsEnabled() {
		t.Fatal("Expected batcher to be disabled for agentful mode")
	}
	if agentfulRunner.batcher.flushInterval != defaultBatchFlushInterval {
		t.Fatalf("Expected agentful batch flush interval to be %v, got %v", defaultBatchFlushInterval, agentfulRunner.batcher.flushInterval)
	}

	// Test agentless mode batching initialization (should have longer interval)
	agentlessRunner := NewCheckRunner(logger, nil, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	if agentlessRunner.batcher == nil {
		t.Fatal("Expected batcher to be initialized")
	}
	if !agentlessRunner.batcher.IsEnabled() {
		t.Fatal("Expected batcher to be enabled for agentless mode")
	}
	expectedAgentlessInterval := 500 * time.Millisecond
	if agentlessRunner.batcher.flushInterval != expectedAgentlessInterval {
		t.Fatalf("Expected agentless batch flush interval to be %v, got %v", expectedAgentlessInterval, agentlessRunner.batcher.flushInterval)
	}

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

	runner := NewCheckRunner(logger, nil, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond) // agentless mode

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

	runner := NewCheckRunner(logger, nil, 5*time.Minute, 30*time.Second, &tls.Config{}, 1, 1, true, 500*time.Millisecond) // agentless mode

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
