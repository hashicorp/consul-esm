// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

// TestBatcher_TimerFlush tests the automatic flush triggered by the timer
func TestBatcher_TimerFlush(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	var flushCalled atomic.Bool
	processFunc := func(updates []*pendingCheckUpdate) {
		flushCalled.Store(true)
		if len(updates) != 1 {
			t.Errorf("Expected 1 update, got %d", len(updates))
		}
	}

	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  10,
		FlushInterval: 100 * time.Millisecond, // Short interval for testing
		Logger:        logger,
		ProcessFunc:   processFunc,
	})
	defer batcher.Stop()

	// Add a check update
	check := &api.HealthCheck{
		Node:    "node1",
		CheckID: "check1",
	}
	batcher.Add(check, "passing", "all good", "", "")

	// Wait for timer to trigger flush
	time.Sleep(200 * time.Millisecond)

	if !flushCalled.Load() {
		t.Fatal("Expected flush to be called by timer")
	}
}

// TestBatcher_MaxBatchSizeFlush tests immediate flush when batch size is reached
func TestBatcher_MaxBatchSizeFlush(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	flushCount := 0
	processFunc := func(updates []*pendingCheckUpdate) {
		flushCount++
	}

	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  3,
		FlushInterval: 1 * time.Hour, // Long interval - shouldn't trigger
		Logger:        logger,
		ProcessFunc:   processFunc,
	})
	defer batcher.Stop()

	// Add updates to different checks to avoid deduplication
	for i := 0; i < 3; i++ {
		check := &api.HealthCheck{
			Node:    "node1",
			CheckID: fmt.Sprintf("check%d", i), // Different checks
		}
		batcher.Add(check, "passing", "all good", "", "")
	}

	// Give it time to process
	time.Sleep(50 * time.Millisecond)

	if flushCount == 0 {
		t.Fatal("Expected at least one flush when max batch size reached")
	}
}

// TestBatcher_StopFlushes tests that Stop() flushes pending updates
func TestBatcher_StopFlushes(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	var flushCalled atomic.Bool
	processFunc := func(updates []*pendingCheckUpdate) {
		flushCalled.Store(true)
		if len(updates) != 2 {
			t.Errorf("Expected 2 updates, got %d", len(updates))
		}
	}

	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  10,
		FlushInterval: 1 * time.Hour, // Long interval
		Logger:        logger,
		ProcessFunc:   processFunc,
	})

	// Add check updates
	check1 := &api.HealthCheck{Node: "node1", CheckID: "check1"}
	check2 := &api.HealthCheck{Node: "node2", CheckID: "check2"}

	batcher.Add(check1, "passing", "ok", "", "")
	batcher.Add(check2, "critical", "fail", "", "")

	// Stop should flush pending updates
	batcher.Stop()

	if !flushCalled.Load() {
		t.Fatal("Expected flush to be called on Stop()")
	}
}

// TestBatcher_DeduplicationInQueue tests that duplicate updates are deduplicated
func TestBatcher_DeduplicationInQueue(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	var mu sync.Mutex
	var receivedUpdates []*pendingCheckUpdate
	processFunc := func(updates []*pendingCheckUpdate) {
		mu.Lock()
		receivedUpdates = updates
		mu.Unlock()
	}

	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  10,
		FlushInterval: 50 * time.Millisecond,
		Logger:        logger,
		ProcessFunc:   processFunc,
	})
	defer batcher.Stop()

	// Add same check multiple times with different status
	check := &api.HealthCheck{
		Node:      "node1",
		CheckID:   "check1",
		ServiceID: "service1",
	}

	batcher.Add(check, "passing", "first", "", "")
	batcher.Add(check, "warning", "second", "passing", "first")
	batcher.Add(check, "critical", "third", "warning", "second")

	// Wait for flush
	time.Sleep(150 * time.Millisecond)

	// Should only have 1 update (latest)
	mu.Lock()
	updateCount := len(receivedUpdates)
	var status, output string
	if updateCount > 0 {
		status = receivedUpdates[0].status
		output = receivedUpdates[0].output
	}
	mu.Unlock()

	if updateCount != 1 {
		t.Fatalf("Expected 1 deduplicated update, got %d", updateCount)
	}
	if status != "critical" {
		t.Errorf("Expected latest status 'critical', got '%s'", status)
	}
	if output != "third" {
		t.Errorf("Expected latest output 'third', got '%s'", output)
	}
}

// TestBatcher_AddAfterStop tests that Add() after Stop() doesn't panic
func TestBatcher_AddAfterStop(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	processFunc := func(updates []*pendingCheckUpdate) {}

	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  10,
		FlushInterval: 100 * time.Millisecond,
		Logger:        logger,
		ProcessFunc:   processFunc,
	})

	batcher.Stop()

	// This should not panic
	check := &api.HealthCheck{Node: "node1", CheckID: "check1"}
	batcher.Add(check, "passing", "ok", "", "")
}

// TestBatcher_ConcurrentAdds tests concurrent Add() calls
func TestBatcher_ConcurrentAdds(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:            "test",
		Level:           hclog.LevelFromString("INFO"),
		IncludeLocation: true,
		Output:          LOGOUT,
	})

	var mu sync.Mutex
	var receivedUpdates []*pendingCheckUpdate
	processFunc := func(updates []*pendingCheckUpdate) {
		mu.Lock()
		receivedUpdates = append(receivedUpdates, updates...)
		mu.Unlock()
	}

	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  100,
		FlushInterval: 200 * time.Millisecond,
		Logger:        logger,
		ProcessFunc:   processFunc,
	})
	defer batcher.Stop()

	// Launch multiple goroutines adding concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 5; j++ {
				check := &api.HealthCheck{
					Node:    "node1",
					CheckID: "check1",
				}
				batcher.Add(check, "passing", "ok", "", "")
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Wait for final flush
	time.Sleep(300 * time.Millisecond)

	// Should have received updates (deduplicated since same check)
	mu.Lock()
	updateCount := len(receivedUpdates)
	mu.Unlock()

	if updateCount == 0 {
		t.Fatal("Expected at least one update")
	}
}

// TestFlushLocked_ProcessFuncNil tests nil processFunc handling
func TestFlushLocked_ProcessFuncNil(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	batcher := &CheckUpdateBatcher{
		logger:         logger,
		maxBatchSize:   10,
		flushInterval:  100 * time.Millisecond,
		pendingUpdates: make(map[string]*pendingCheckUpdate),
		processFunc:    nil, // Nil process function
	}

	check := &api.HealthCheck{
		Node:    "node1",
		CheckID: "check1",
	}

	batcher.Add(check, "passing", "ok", "", "")

	// Should not panic with nil processFunc
	batcher.Flush()
}
