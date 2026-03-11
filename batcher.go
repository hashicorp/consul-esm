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
	Logger        hclog.Logger
	ProcessFunc   func([]*pendingCheckUpdate)
}

// NewCheckUpdateBatcher creates a new batcher with the given configuration
func NewCheckUpdateBatcher(config BatcherConfig) *CheckUpdateBatcher {
	if config.MaxBatchSize == 0 {
		config.MaxBatchSize = maxTxnOps
	}

	return &CheckUpdateBatcher{
		logger:         config.Logger,
		maxBatchSize:   config.MaxBatchSize,
		flushInterval:  config.FlushInterval,
		pendingUpdates: make(map[string]*pendingCheckUpdate),
		processFunc:    config.ProcessFunc,
	}
}

// Add queues a check update for batching.
func (b *CheckUpdateBatcher) Add(check *api.HealthCheck, status, output, oldStatus, oldOutput string) {
	b.pendingUpdatesMu.Lock()

	// Create a unique key for this check
	checkKey := makeCheckKey(check.Node, string(check.CheckID))

	// Store the update (overwrites any previous pending update for this check - deduplication)
	b.pendingUpdates[checkKey] = &pendingCheckUpdate{
		check:     check,
		status:    status,
		output:    output,
		oldStatus: oldStatus,
		oldOutput: oldOutput,
	}

	b.logger.Trace("Queued check update for batching", "checkID", check.CheckID, "pendingCount", len(b.pendingUpdates))

	// Flush immediately if we've reached the max batch size
	if len(b.pendingUpdates) >= b.maxBatchSize {
		b.logger.Debug("Batch size limit reached, flushing immediately", "batchSize", len(b.pendingUpdates))
		updates := b.drainLocked()
		b.pendingUpdatesMu.Unlock()
		b.processUpdates(updates)
		return
	}

	// Start timer only if not already running
	// This ensures batch flushes at regular intervals regardless of update rate
	if b.flushTimer == nil {
		b.flushTimer = time.AfterFunc(b.flushInterval, b.Flush)
		b.logger.Trace("Started batch flush timer", "interval", b.flushInterval)
	}

	b.pendingUpdatesMu.Unlock()
}

// Flush flushes all pending check updates.
// This is the public version that acquires the lock.
func (b *CheckUpdateBatcher) Flush() {
	b.pendingUpdatesMu.Lock()
	updates := b.drainLocked()
	b.pendingUpdatesMu.Unlock()
	b.processUpdates(updates)
}

func (b *CheckUpdateBatcher) drainLocked() []*pendingCheckUpdate {
	// Stop the timer if it's running
	if b.flushTimer != nil {
		b.flushTimer.Stop()
		b.flushTimer = nil
	}

	if len(b.pendingUpdates) == 0 {
		return nil
	}

	// Collect all pending updates
	updates := make([]*pendingCheckUpdate, 0, len(b.pendingUpdates))
	for _, update := range b.pendingUpdates {
		updates = append(updates, update)
	}

	// Clear pending updates
	b.pendingUpdates = make(map[string]*pendingCheckUpdate)

	return updates
}

func (b *CheckUpdateBatcher) processUpdates(updates []*pendingCheckUpdate) {
	if len(updates) > 0 && b.processFunc != nil {
		b.logger.Debug("Processing batch of check updates", "batchSize", len(updates))
		b.processFunc(updates)
	}
}

// Stop stops the batcher and flushes any pending updates
func (b *CheckUpdateBatcher) Stop() {
	// Force flush all pending updates on shutdown
	b.pendingUpdatesMu.Lock()
	updates := b.drainLocked()
	b.pendingUpdatesMu.Unlock()
	b.processUpdates(updates)
}

// makeCheckKey creates a unique key for a check
func makeCheckKey(node, checkID string) string {
	return node + "/" + checkID
}
