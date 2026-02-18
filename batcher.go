// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

// CheckUpdateBatcher handles batching of check updates to reduce HTTP connections.
// It's designed to be mode-agnostic and can be configured for different batch behaviors.
type CheckUpdateBatcher struct {
	logger hclog.Logger

	// Configuration
	maxBatchSize  int
	flushInterval time.Duration
	enabled       bool

	// State
	pendingUpdates   map[string]*pendingCheckUpdate
	pendingUpdatesMu sync.Mutex
	flushTimer       *time.Timer

	// Callback for processing batches
	processFunc func([]*pendingCheckUpdate)
}

// BatcherConfig configures the batching behavior
type BatcherConfig struct {
	MaxBatchSize  int
	FlushInterval time.Duration
	Enabled       bool
	Logger        hclog.Logger
	ProcessFunc   func([]*pendingCheckUpdate)
}

// NewCheckUpdateBatcher creates a new batcher with the given configuration
func NewCheckUpdateBatcher(config BatcherConfig) *CheckUpdateBatcher {
	if config.MaxBatchSize == 0 {
		config.MaxBatchSize = maxTxnOps
	}
	if config.FlushInterval == 0 {
		config.FlushInterval = defaultBatchFlushInterval
	}

	return &CheckUpdateBatcher{
		logger:         config.Logger,
		maxBatchSize:   config.MaxBatchSize,
		flushInterval:  config.FlushInterval,
		enabled:        config.Enabled,
		pendingUpdates: make(map[string]*pendingCheckUpdate),
		processFunc:    config.ProcessFunc,
	}
}

// Add queues a check update for batching.
// Returns true if the update was batched, false if batching is disabled.
func (b *CheckUpdateBatcher) Add(check *api.HealthCheck, status, output string) bool {
	if !b.enabled {
		return false
	}

	b.pendingUpdatesMu.Lock()
	defer b.pendingUpdatesMu.Unlock()

	// Create a unique key for this check
	checkKey := makeCheckKey(check.Node, string(check.CheckID))

	// Store the update (overwrites any previous pending update for this check - deduplication)
	b.pendingUpdates[checkKey] = &pendingCheckUpdate{
		check:  check,
		status: status,
		output: output,
	}

	b.logger.Trace("Queued check update for batching", "checkID", check.CheckID, "pendingCount", len(b.pendingUpdates))

	// Flush immediately if we've reached the max batch size
	if len(b.pendingUpdates) >= b.maxBatchSize {
		b.logger.Debug("Batch size limit reached, flushing immediately", "batchSize", len(b.pendingUpdates))
		b.flushLocked()
		return true
	}

	// Start timer only if not already running
	// This ensures batch flushes at regular intervals regardless of update rate
	if b.flushTimer == nil {
		b.flushTimer = time.AfterFunc(b.flushInterval, b.Flush)
		b.logger.Trace("Started batch flush timer", "interval", b.flushInterval)
	}

	return true
}

// Flush flushes all pending check updates.
// This is the public version that acquires the lock.
func (b *CheckUpdateBatcher) Flush() {
	if !b.enabled {
		return
	}

	b.pendingUpdatesMu.Lock()
	defer b.pendingUpdatesMu.Unlock()
	b.flushLocked()
}

// flushLocked flushes all pending check updates.
// Caller must hold pendingUpdatesMu lock.
func (b *CheckUpdateBatcher) flushLocked() {
	// Stop the timer if it's running
	if b.flushTimer != nil {
		b.flushTimer.Stop()
		b.flushTimer = nil
	}

	if len(b.pendingUpdates) == 0 {
		return
	}

	// Collect all pending updates
	updates := make([]*pendingCheckUpdate, 0, len(b.pendingUpdates))
	for _, update := range b.pendingUpdates {
		updates = append(updates, update)
	}

	// Clear pending updates
	b.pendingUpdates = make(map[string]*pendingCheckUpdate)

	// Process updates (must release lock during processing to avoid deadlocks)
	b.pendingUpdatesMu.Unlock()
	if b.processFunc != nil {
		b.processFunc(updates)
	}
	b.pendingUpdatesMu.Lock()
}

// Stop stops the batcher and flushes any pending updates
func (b *CheckUpdateBatcher) Stop() {
	if !b.enabled {
		return
	}

	// Force flush all pending updates on shutdown, bypassing minimum batch size
	b.pendingUpdatesMu.Lock()
	defer b.pendingUpdatesMu.Unlock()

	// Stop the timer
	if b.flushTimer != nil {
		b.flushTimer.Stop()
		b.flushTimer = nil
	}

	if len(b.pendingUpdates) == 0 {
		return
	}

	// Collect and flush all pending updates (force flush on shutdown)
	updates := make([]*pendingCheckUpdate, 0, len(b.pendingUpdates))
	for _, update := range b.pendingUpdates {
		updates = append(updates, update)
	}
	b.pendingUpdates = make(map[string]*pendingCheckUpdate)

	b.pendingUpdatesMu.Unlock()
	if b.processFunc != nil {
		b.processFunc(updates)
	}
	b.pendingUpdatesMu.Lock()
}

// IsEnabled returns whether batching is enabled
func (b *CheckUpdateBatcher) IsEnabled() bool {
	return b.enabled
}

// makeCheckKey creates a unique key for a check
func makeCheckKey(node, checkID string) string {
	return node + "/" + checkID
}
