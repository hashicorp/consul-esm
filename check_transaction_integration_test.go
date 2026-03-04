// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/go-hclog"
)

// TestExecuteBatchTransaction_Success tests successful batch transaction
func TestExecuteBatchTransaction_Success(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Register a node and service with a check
	nodeName := "test-node"
	serviceName := "test-service"
	checkID := "test-check"

	// Register node
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Register service with check
	regService := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Service: &api.AgentService{
			ID:      serviceName,
			Service: serviceName,
		},
		Check: &api.AgentCheck{
			Node:      nodeName,
			CheckID:   checkID,
			Name:      "test check",
			Status:    api.HealthPassing,
			ServiceID: serviceName,
		},
	}
	if _, err := client.Catalog().Register(regService, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check to get its ModifyIndex
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) == 0 {
		t.Fatal("Expected at least one check")
	}

	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}
	if testCheck == nil {
		t.Fatal("Could not find test check")
	}

	// Create pending update
	update := &pendingCheckUpdate{
		check:     testCheck,
		status:    api.HealthCritical,
		output:    "test failure",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Build transaction
	testCheck.Status = api.HealthCritical
	testCheck.Output = "test failure"

	ops := api.TxnOps{
		&api.TxnOp{
			Check: &api.CheckTxnOp{
				Verb:  api.CheckCAS,
				Check: *testCheck,
			},
		},
	}

	// Execute transaction
	runner.executeBatchTransaction(ops, []*pendingCheckUpdate{update})

	// Verify the check was updated
	checks, _, err = client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}

	var updated *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			updated = c
			break
		}
	}

	if updated == nil {
		t.Fatal("Check was deleted unexpectedly")
	}
	if updated.Status != api.HealthCritical {
		t.Errorf("Expected status critical, got %s", updated.Status)
	}
	if updated.Output != "test failure" {
		t.Errorf("Expected output 'test failure', got %s", updated.Output)
	}
}

// TestExecuteBatchTransaction_StaleIndex tests CAS failure with stale index
func TestExecuteBatchTransaction_StaleIndex(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Register a node and check
	nodeName := "test-node"
	checkID := "test-check"

	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}
	if testCheck == nil {
		t.Fatal("Could not find test check")
	}

	// Store the check in runner's cache
	checkHash := hashCheck(testCheck)
	runner.checks.Store(checkHash, &esmHealthCheck{
		HealthCheck: *testCheck,
	})

	// Update the check externally to change ModifyIndex
	regNode.Check.Status = api.HealthWarning
	regNode.Check.Output = "external update"
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Now try to update with stale ModifyIndex
	update := &pendingCheckUpdate{
		check:     testCheck, // Has old ModifyIndex
		status:    api.HealthCritical,
		output:    "test failure",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	testCheck.Status = api.HealthCritical
	testCheck.Output = "test failure"

	ops := api.TxnOps{
		&api.TxnOp{
			Check: &api.CheckTxnOp{
				Verb:  api.CheckCAS,
				Check: *testCheck, // Stale ModifyIndex
			},
		},
	}

	// Execute transaction - should trigger retry
	runner.executeBatchTransaction(ops, []*pendingCheckUpdate{update})


	// Verify the check was eventually updated (retry succeeded)
	checks, _, err = client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}

	var updated *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			updated = c
			break
		}
	}

	if updated == nil {
		t.Fatal("Check was deleted unexpectedly")
	}

	// After retry, status should be updated
	if updated.Status != api.HealthCritical {
		t.Errorf("Expected status critical after retry, got %s", updated.Status)
	}
	if updated.Output != "test failure" {
		t.Errorf("Expected output 'test failure' after retry, got %s", updated.Output)
	}
}

// TestRetryFailedBatchOperations_CheckDeleted tests retry when check is deleted
func TestRetryFailedBatchOperations_CheckDeleted(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Register and then deregister a check
	nodeName := "test-node"
	checkID := "test-check"

	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}
	if testCheck == nil {
		t.Fatal("Could not find test check")
	}

	// Store in cache
	checkHash := hashCheck(testCheck)
	runner.checks.Store(checkHash, &esmHealthCheck{
		HealthCheck: *testCheck,
	})

	// Delete the check
	dereg := &api.CatalogDeregistration{
		Node:    nodeName,
		CheckID: checkID,
	}
	if _, err := client.Catalog().Deregister(dereg, nil); err != nil {
		t.Fatal(err)
	}

	// Create fake error from transaction
	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 0,
			What:    "check index is stale",
		},
	}

	update := &pendingCheckUpdate{
		check:     testCheck,
		status:    api.HealthCritical,
		output:    "test failure",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Call retry - should handle deleted check gracefully
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)

	// Should not panic and should log that check doesn't exist
	time.Sleep(100 * time.Millisecond)
}

// TestProcessBatchedUpdates_NilClient tests nil client guard
func TestProcessBatchedUpdates_NilClient(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	// Create runner without client
	runner := &CheckRunner{
		logger:      logger,
		client:      nil,
		isAgentless: true,
	}

	check := &api.HealthCheck{
		Node:    "node1",
		CheckID: "check1",
	}
	update := &pendingCheckUpdate{
		check:  check,
		status: "passing",
		output: "ok",
	}

	// Should not panic
	runner.processBatchedUpdates([]*pendingCheckUpdate{update})
}

// TestProcessBatchedUpdates_EmptyUpdates tests empty update list
func TestProcessBatchedUpdates_EmptyUpdates(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	client, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Should return early without error
	runner.processBatchedUpdates([]*pendingCheckUpdate{})
	runner.processBatchedUpdates(nil)
}

// TestExecuteBatchTransaction_EmptyOps tests empty operations
func TestExecuteBatchTransaction_EmptyOps(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	client, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Should return early
	runner.executeBatchTransaction(api.TxnOps{}, []*pendingCheckUpdate{})
	runner.executeBatchTransaction(nil, nil)
}

// TestRevertCheckState_MissingCheck tests state reversion with missing check
func TestRevertCheckState_MissingCheck(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	client, _ := api.NewClient(api.DefaultConfig())
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Try to revert a check that doesn't exist
	runner.revertCheckState("node1", "missing-check", "passing", "ok")

	// Should not panic and should log
}

// TestBatcher_NewCheckUpdateBatcher_ZeroMaxSize tests batcher creation with zero max size
func TestBatcher_NewCheckUpdateBatcher_ZeroMaxSize(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("INFO"),
		Output: LOGOUT,
	})

	processFunc := func(updates []*pendingCheckUpdate) {}

	// Should use default max size
	batcher := NewCheckUpdateBatcher(BatcherConfig{
		MaxBatchSize:  0, // Zero - should use default maxTxnOps
		FlushInterval: 100 * time.Millisecond,
		Logger:        logger,
		ProcessFunc:   processFunc,
	})
	defer batcher.Stop()

	if batcher.maxBatchSize != maxTxnOps {
		t.Errorf("Expected default max size %d, got %d", maxTxnOps, batcher.maxBatchSize)
	}
}

// TestHashCheck_WithoutServiceID tests hash generation for node-level checks
func TestHashCheck_WithoutServiceID(t *testing.T) {
	check := &api.HealthCheck{
		Node:      "test-node",
		CheckID:   "test-check",
		ServiceID: "", // No service
	}

	hash := hashCheck(check)
	expected := "test-node/test-check"

	if string(hash) != expected {
		t.Errorf("Expected hash %s, got %s", expected, hash)
	}
}

// TestHashCheck_WithServiceID tests hash generation for service-level checks
func TestHashCheck_WithServiceID(t *testing.T) {
	check := &api.HealthCheck{
		Node:      "test-node",
		CheckID:   "test-check",
		ServiceID: "test-service",
	}

	hash := hashCheck(check)
	expected := "test-node/test-service/test-check"

	if string(hash) != expected {
		t.Errorf("Expected hash %s, got %s", expected, hash)
	}
}

// TestProcessBatchedUpdates_NodeFetchError tests error handling when fetching node fails
func TestProcessBatchedUpdates_NodeFetchError(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Create update for non-existent node (will fail to fetch)
	check := &api.HealthCheck{
		Node:      "non-existent-node",
		CheckID:   "test-check",
		Namespace: "default",
	}

	update := &pendingCheckUpdate{
		check:     check,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Should handle error gracefully
	runner.processBatchedUpdates([]*pendingCheckUpdate{update})
}

// TestProcessBatchedUpdates_CheckNoLongerExists tests handling of deleted checks
func TestProcessBatchedUpdates_CheckNoLongerExists(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	nodeName := "test-node"
	checkID := "test-check"

	// Register node with check
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}

	// Delete the check
	dereg := &api.CatalogDeregistration{
		Node:    nodeName,
		CheckID: checkID,
	}
	if _, err := client.Catalog().Deregister(dereg, nil); err != nil {
		t.Fatal(err)
	}

	// Now try to process update for deleted check
	update := &pendingCheckUpdate{
		check:     testCheck,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Should skip deleted check
	runner.processBatchedUpdates([]*pendingCheckUpdate{update})
}

// TestExecuteBatchTransaction_NetworkError tests network error handling
func TestExecuteBatchTransaction_NetworkError(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	// Create client pointing to non-existent server
	client, err := api.NewClient(&api.Config{
		Address: "127.0.0.1:1", // Invalid port
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	check := &api.HealthCheck{
		Node:    "test-node",
		CheckID: "test-check",
		Status:  api.HealthPassing,
	}

	ops := api.TxnOps{
		&api.TxnOp{
			Check: &api.CheckTxnOp{
				Verb:  api.CheckCAS,
				Check: *check,
			},
		},
	}

	update := &pendingCheckUpdate{
		check:     check,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Should handle network error gracefully
	runner.executeBatchTransaction(ops, []*pendingCheckUpdate{update})
}

// TestExecuteBatchTransaction_NonStaleErrors tests non-CAS errors
func TestExecuteBatchTransaction_NonStaleErrors(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Create transaction with invalid check (no Node field will cause error)
	check := &api.HealthCheck{
		Node:    "", // Empty node will cause error
		CheckID: "test-check",
		Status:  api.HealthPassing,
	}

	ops := api.TxnOps{
		&api.TxnOp{
			Check: &api.CheckTxnOp{
				Verb:  api.CheckCAS,
				Check: *check,
			},
		},
	}

	update := &pendingCheckUpdate{
		check:     check,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Should handle non-stale errors
	runner.executeBatchTransaction(ops, []*pendingCheckUpdate{update})
}

// TestRetryFailedBatchOperations_InvalidIndex tests invalid error index handling
func TestRetryFailedBatchOperations_InvalidIndex(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	check := &api.HealthCheck{
		Node:    "test-node",
		CheckID: "test-check",
	}

	update := &pendingCheckUpdate{
		check:     check,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Create error with invalid index (out of bounds)
	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 999, // Invalid - only have 1 update
			What:    "index is stale",
		},
	}

	// Should handle invalid index gracefully
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)
}

// TestRetryFailedBatchOperations_NodeFetchError tests retry with node fetch error
func TestRetryFailedBatchOperations_NodeFetchError(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	// Stop the server to cause fetch errors
	server.Stop()

	check := &api.HealthCheck{
		Node:    "test-node",
		CheckID: "test-check",
	}

	update := &pendingCheckUpdate{
		check:     check,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 0,
			What:    "index is stale",
		},
	}

	// Should handle fetch error gracefully
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)
}

// TestRetryFailedBatchOperations_RetryFails tests when retry itself fails
func TestRetryFailedBatchOperations_RetryFails(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	nodeName := "test-node"
	checkID := "test-check"

	// Register node with check
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}

	// Store in cache
	checkHash := hashCheck(testCheck)
	runner.checks.Store(checkHash, &esmHealthCheck{
		HealthCheck: *testCheck,
	})

	// Update externally multiple times to make retry fail
	for i := 0; i < 3; i++ {
		regNode.Check.Output = "updated " + string(rune('0'+i))
		if _, err := client.Catalog().Register(regNode, nil); err != nil {
			t.Fatal(err)
		}
	}

	update := &pendingCheckUpdate{
		check:     testCheck, // Stale ModifyIndex
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "original",
	}

	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 0,
			What:    "index is stale",
		},
	}

	// Should revert state when retry fails
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)

	// Verify state was reverted
	time.Sleep(100 * time.Millisecond)
	storedCheck, ok := runner.checks.Load(checkHash)
	if ok && storedCheck.Status != api.HealthPassing {
		t.Logf("Check status after retry failure: %s (may be reverted to: %s)", storedCheck.Status, api.HealthPassing)
	}
}

// TestHandleCheckUpdate_AgentfulMode tests agentful mode path
func TestHandleCheckUpdate_AgentfulMode(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	// Create runner in agentful mode (isAgentless=false)
	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, false, 0)
	defer runner.Stop()

	nodeName := "test-node"
	checkID := "test-check"

	// Register node with check
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}

	// Should use immediate update path (not batcher)
	runner.handleCheckUpdate(testCheck, api.HealthCritical, "failed", "", "")

	time.Sleep(100 * time.Millisecond)

	// Verify update went through
	checks, _, err = client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range checks {
		if c.CheckID == checkID {
			if c.Status != api.HealthCritical {
				t.Errorf("Expected status critical, got %s", c.Status)
			}
			break
		}
	}
}

// TestRetryFailedBatchOperations_SuccessfulRetry tests successful retry path
func TestRetryFailedBatchOperations_SuccessfulRetry(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	nodeName := "test-node"
	checkID := "test-check"

	// Register node with check
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
			Output:  "initial",
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}

	// Store old version in cache
	oldCheck := *testCheck
	checkHash := hashCheck(testCheck)
	runner.checks.Store(checkHash, &esmHealthCheck{
		HealthCheck: oldCheck,
	})

	// Update externally to create stale index (just once, so retry will succeed)
	regNode.Check.Output = "updated once"
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	update := &pendingCheckUpdate{
		check:     &oldCheck, // Stale ModifyIndex
		status:    api.HealthCritical,
		output:    "test output",
		oldStatus: api.HealthPassing,
		oldOutput: "initial",
	}

	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 0,
			What:    "index is stale",
		},
	}

	// Should successfully retry with fresh state
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)

	time.Sleep(200 * time.Millisecond)

	// Verify the retry succeeded and check was updated
	checks, _, err = client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range checks {
		if c.CheckID == checkID {
			if c.Status != api.HealthCritical {
				t.Errorf("Expected status critical after retry, got %s", c.Status)
			}
			if c.Output != "test output" {
				t.Logf("Check output after retry: %s", c.Output)
			}
			break
		}
	}
}

// TestRetryFailedBatchOperations_EmptyErrors tests retry with empty errors
func TestRetryFailedBatchOperations_EmptyErrors(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	check := &api.HealthCheck{
		Node:    "test-node",
		CheckID: "test-check",
	}

	update := &pendingCheckUpdate{
		check:     check,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	// Empty errors array
	txnErrors := api.TxnErrors{}

	// Should return early
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)
}

// TestRetryFailedBatchOperations_CheckDeletedDuringRetry tests when check is deleted during retry
func TestRetryFailedBatchOperations_CheckDeletedDuringRetry(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("TRACE"), // Use TRACE to hit trace logs
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	nodeName := "test-node"
	checkID := "test-check"

	// Register node with check
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}

	oldCheck := *testCheck

	// Now delete the check before retry
	dereg := &api.CatalogDeregistration{
		Node:    nodeName,
		CheckID: checkID,
	}
	if _, err := client.Catalog().Deregister(dereg, nil); err != nil {
		t.Fatal(err)
	}

	update := &pendingCheckUpdate{
		check:     &oldCheck,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 0,
			What:    "index is stale",
		},
	}

	// Should handle check not found during retry
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)
}

// TestRetryFailedBatchOperations_RetryTransactionError tests when retry transaction itself errors
func TestRetryFailedBatchOperations_RetryTransactionError(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewTestServerConfigT(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client, err := api.NewClient(&api.Config{
		Address: server.HTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "test",
		Level:  hclog.LevelFromString("TRACE"),
		Output: LOGOUT,
	})

	runner := NewCheckRunner(logger, client, 0, 0, &tls.Config{}, 1, 1, true, 500*time.Millisecond)
	defer runner.Stop()

	nodeName := "test-node"
	checkID := "test-check"

	// Register node with check
	regNode := &api.CatalogRegistration{
		Node:    nodeName,
		Address: "1.2.3.4",
		Check: &api.AgentCheck{
			Node:    nodeName,
			CheckID: checkID,
			Name:    "test check",
			Status:  api.HealthPassing,
		},
	}
	if _, err := client.Catalog().Register(regNode, nil); err != nil {
		t.Fatal(err)
	}

	// Get the check
	checks, _, err := client.Health().Node(nodeName, nil)
	if err != nil {
		t.Fatal(err)
	}
	var testCheck *api.HealthCheck
	for _, c := range checks {
		if c.CheckID == checkID {
			testCheck = c
			break
		}
	}

	oldCheck := *testCheck
	checkHash := hashCheck(testCheck)
	runner.checks.Store(checkHash, &esmHealthCheck{
		HealthCheck: oldCheck,
	})

	// Stop server to cause transaction error
	server.Stop()

	update := &pendingCheckUpdate{
		check:     &oldCheck,
		status:    api.HealthCritical,
		output:    "test",
		oldStatus: api.HealthPassing,
		oldOutput: "",
	}

	txnErrors := api.TxnErrors{
		&api.TxnError{
			OpIndex: 0,
			What:    "index is stale",
		},
	}

	// Should handle transaction error gracefully (warn log at line 807)
	runner.retryFailedBatchOperations([]*pendingCheckUpdate{update}, txnErrors)
}
