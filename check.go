// Copyright IBM Corp. 2017, 2025
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/armon/go-metrics"
	"github.com/armon/go-metrics/prometheus"
	consulchecks "github.com/hashicorp/consul/agent/checks"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/consul/types"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-multierror"
)

const externalCheckName = "externalNodeHealth"

// defaultInterval is the check interval to use if one is not set.
var defaultInterval = 30 * time.Second

// TODO: Evaluate the defaultBatchFlushInterval based on the feedback/observations
// defaultBatchFlushInterval is the default interval for flushing batched updates.
var defaultBatchFlushInterval = 500 * time.Millisecond

// maxTxnOps is the maximum number of operations allowed in a single transaction.
// Consul supports up to 64 operations per transaction as documented in the API docs
// https://developer.hashicorp.com/consul/api-docs/txn .
const maxTxnOps = 64

var ChecksGauges = []prometheus.GaugeDefinition{
	{
		Name: []string{"esm", "checks"},
		Help: "Total number of external checks being monitored",
	},
	{
		Name: []string{"esm", "checks", "healthy"},
		Help: "Number of external checks in passing state",
	},
	{
		Name: []string{"esm", "checks", "unhealthy"},
		Help: "Number of external checks in unhealthy state",
	},
}

var ChecksSummary = []prometheus.SummaryDefinition{
	{
		Name: []string{"esm", "checks", "fetch_and_update", "duration"},
		Help: "Measures the time taken to fetch and update health checks",
	},
}

type checkIDSet map[types.CheckID]bool

// pendingCheckUpdate represents a check update waiting to be batched
type pendingCheckUpdate struct {
	check          *api.HealthCheck
	status         string
	output         string
	originalStatus string // status before this update
	originalOutput string // output before this update
}

type CheckRunner struct {
	logger hclog.Logger
	client *api.Client

	// checks are unmodified checks as retrieved from Consul Catalog
	checks checkMap[types.CheckID, *esmHealthCheck]

	// checksHTTP & checksTCP are HTTP/TCP checks that are run by ESM.
	// They have potentially modified values from the Consul Catalog checks
	checksHTTP stopMap[types.CheckID, *consulchecks.CheckHTTP]
	checksTCP  stopMap[types.CheckID, *consulchecks.CheckTCP]

	checksCritical checkMap[types.CheckID, time.Time]

	// Used to track checks that are being deferred
	deferCheck checkMap[types.CheckID, *time.Timer]

	// Batcher handles batching of check updates (enabled for agentless mode)
	batcher *CheckUpdateBatcher

	CheckUpdateInterval time.Duration
	MinimumInterval     time.Duration

	tlsConfig *tls.Config

	PassingThreshold  int
	CriticalThreshold int
	isAgentless       bool
}

type esmHealthCheck struct {
	api.HealthCheck
	failureCounter int
	successCounter int
}

// checkMap is sync.Map with type safety
type checkMap[K comparable, V any] struct {
	sync.Map
}

func (m *checkMap[K, V]) Load(k K) (v V, ok bool) {
	_v, ok := m.Map.Load(k)
	if !ok {
		return v, ok
	}
	v, ok = _v.(V)
	return v, ok
}

func (m *checkMap[K, V]) LoadAndDelete(k K) (v V, ok bool) {
	_v, ok := m.Map.LoadAndDelete(k)
	if !ok {
		return v, ok
	}
	v, ok = _v.(V)
	return v, ok
}

func (m *checkMap[K, V]) Store(k K, v V) {
	m.Map.Store(k, v)
}

// extends checkMap to require a Stop method
type stop interface{ Stop() }

type stopMap[K comparable, V stop] struct {
	checkMap[K, V]
}

func (m *stopMap[K, V]) StopAll() {
	m.Map.Range(func(_, v any) bool {
		v.(V).Stop()
		return true
	})
}

func NewCheckRunner(logger hclog.Logger, client *api.Client, updateInterval,
	minimumInterval time.Duration, tlsConfig *tls.Config, passingThreshold int,
	criticalThreshold int, isAgentless bool, batchFlushInterval time.Duration,
) *CheckRunner {
	runner := &CheckRunner{
		logger:              logger,
		client:              client,
		CheckUpdateInterval: updateInterval,
		MinimumInterval:     minimumInterval,
		tlsConfig:           tlsConfig,
		PassingThreshold:    passingThreshold,
		CriticalThreshold:   criticalThreshold,
		isAgentless:         isAgentless,
	}

	if isAgentless {
		batchInterval := defaultBatchFlushInterval
		logger.Info("Configuring CheckRunner batch interval", "received_interval", batchFlushInterval, "is_agentless", isAgentless)
		// Use configured batch flush interval for agentless mode
		if batchFlushInterval > 0 {
			batchInterval = batchFlushInterval
			logger.Info("Using configured batch flush interval", "interval", batchInterval)
		} else {
			// Fallback to default if not configured
			batchInterval = defaultBatchFlushInterval
			logger.Info("Using default batch flush interval", "interval", batchInterval)
		}
		// Initialise batcher for agentless mode only
		runner.batcher = NewCheckUpdateBatcher(BatcherConfig{
			MaxBatchSize:  maxTxnOps,
			FlushInterval: batchInterval,
			Logger:        logger,
			ProcessFunc:   runner.processBatchedUpdates,
		})
	}

	return runner
}

func (c *CheckRunner) Stop() {
	// Stop batcher and flush any pending updates
	if c.batcher != nil {
		c.batcher.Stop()
	}

	c.checksHTTP.StopAll()
	c.checksTCP.StopAll()
}

// Update an HTTP check
func (c *CheckRunner) updateCheckHTTP(
	latestCheck *api.HealthCheck, checkHash types.CheckID,
	definition *api.HealthCheckDefinition, updated, added checkIDSet,
) bool {
	tlsConfig := c.tlsConfig.Clone()
	tlsConfig.InsecureSkipVerify = definition.TLSSkipVerify
	tlsConfig.ServerName = definition.TLSServerName

	http := &consulchecks.CheckHTTP{
		CheckID:         structs.CheckID{ID: checkHash},
		HTTP:            definition.HTTP,
		Header:          definition.Header,
		Method:          definition.Method,
		Interval:        definition.IntervalDuration,
		Timeout:         definition.TimeoutDuration,
		Logger:          c.logger,
		TLSClientConfig: tlsConfig,
		StatusHandler: consulchecks.NewStatusHandler(c, c.logger,
			c.PassingThreshold, c.CriticalThreshold, c.CriticalThreshold),
	}

	if check, checkExists := c.checks.Load(checkHash); checkExists {
		httpCheck, httpCheckExists := c.checksHTTP.Load(checkHash)
		if httpCheckExists &&
			httpCheck.HTTP == http.HTTP &&
			headersAlmostEqual(httpCheck.Header, http.Header) &&
			httpCheck.Method == http.Method &&
			httpCheck.TLSClientConfig.InsecureSkipVerify == http.TLSClientConfig.InsecureSkipVerify &&
			httpCheck.TLSClientConfig.ServerName == http.TLSClientConfig.ServerName &&
			httpCheck.Interval == http.Interval &&
			httpCheck.Timeout == http.Timeout &&
			check.Definition.DeregisterCriticalServiceAfter == definition.DeregisterCriticalServiceAfter {
			return false
		}

		c.logger.Info("Updating HTTP check", "checkHash", checkHash)

		if httpCheckExists {
			httpCheck.Stop()
		} else {
			tcpCheck, tcpCheckExists := c.checksTCP.Load(checkHash)
			if !tcpCheckExists {
				c.logger.Warn("Inconsistency check is not TCP and HTTP", "checkHash", checkHash)
				return false
			}
			tcpCheck.Stop()
			c.checksTCP.Delete(checkHash)
		}

		updated[checkHash] = true
	} else {
		c.logger.Debug("Added HTTP check", "checkHash", checkHash)
		added[checkHash] = true
	}

	http.Start()
	c.checksHTTP.Store(checkHash, http)

	return true
}

// Compares headers, skipping ones automatically added by Consul
// in consul/agent/checks/check.go
func headersAlmostEqual(h1, h2 map[string][]string) bool {
	skip := map[string]bool{"User-Agent": true, "Accept": true}
	for k1, v1 := range h1 {
		if skip[k1] {
			continue
		}
		v2 := h2[k1]
		if !reflect.DeepEqual(v1, v2) {
			return false
		}
	}
	return true
}

func (c *CheckRunner) updateCheckTCP(
	latestCheck *api.HealthCheck, checkHash types.CheckID,
	definition *api.HealthCheckDefinition, updated, added checkIDSet,
) bool {
	tcp := &consulchecks.CheckTCP{
		CheckID:  structs.CheckID{ID: checkHash},
		TCP:      definition.TCP,
		Interval: definition.IntervalDuration,
		Timeout:  definition.TimeoutDuration,
		Logger:   c.logger,
		StatusHandler: consulchecks.NewStatusHandler(c, c.logger,
			c.PassingThreshold, c.CriticalThreshold, c.CriticalThreshold),
	}

	if check, checkExists := c.checks.Load(checkHash); checkExists {
		tcpCheck, tcpCheckExists := c.checksTCP.Load(checkHash)
		if tcpCheckExists &&
			tcpCheck.TCP == tcp.TCP &&
			tcpCheck.Interval == tcp.Interval &&
			tcpCheck.Timeout == tcp.Timeout &&
			check.Definition.DeregisterCriticalServiceAfter == definition.DeregisterCriticalServiceAfter {
			return false
		}

		c.logger.Info("Updating TCP check", "checkHash", checkHash)

		if tcpCheckExists {
			tcpCheck.Stop()
		} else {
			httpCheck, httpCheckExists := c.checksHTTP.Load(checkHash)
			if !httpCheckExists {
				c.logger.Warn("Inconsistency check is not TCP and HTTP", "checkHash", checkHash)
				return false
			}
			httpCheck.Stop()
			c.checksHTTP.Delete(checkHash)
		}

		updated[checkHash] = true
	} else {

		c.logger.Debug("Added TCP check", "checkHash", checkHash)
		added[checkHash] = true
	}

	tcp.Start()
	c.checksTCP.Store(checkHash, tcp)

	return true
}

// UpdateChecks takes a list of checks from the catalog and updates
// our list of running checks to match.
func (c *CheckRunner) UpdateChecks(checks api.HealthChecks) {
	defer metrics.MeasureSince([]string{"checks", "update"}, time.Now())

	found := make(checkIDSet)
	added := make(checkIDSet)
	updated := make(checkIDSet)
	removed := make(checkIDSet)

	for _, check := range checks {
		// Skip the ping-based node check since we're managing that separately
		if check.CheckID == externalCheckName {
			continue
		}

		checkHash := hashCheck(check)

		// create a copy of the definition that will be modified
		definition := check.Definition
		if definition.IntervalDuration == 0 {
			definition.IntervalDuration = defaultInterval
		}

		// here we verify that the interval is not less then the minimum
		if definition.IntervalDuration < c.MinimumInterval {
			definition.IntervalDuration = c.MinimumInterval
		}

		anyUpdates := false

		if definition.HTTP != "" {
			anyUpdates = c.updateCheckHTTP(check, checkHash, &definition, updated, added)
		} else if definition.TCP != "" {
			anyUpdates = c.updateCheckTCP(check, checkHash, &definition, updated, added)
		} else {
			c.logger.Warn("check is not a valid HTTP or TCP check", "checkHash", checkHash)
			continue
		}

		// if we had to fix the interval and we had to update the service, put some trace out
		unmodifiedDef := check.Definition
		if anyUpdates && unmodifiedDef.IntervalDuration < c.MinimumInterval {
			c.logger.Warn("Check interval too low", "interval", unmodifiedDef.Interval, "check", check.Name)
		}

		found[checkHash] = true
		updatedCheck := &esmHealthCheck{
			*check,
			0,
			0,
		}
		if previousCheck, ok := c.checks.LoadAndDelete(checkHash); ok {
			updatedCheck.failureCounter = previousCheck.failureCounter
			updatedCheck.successCounter = previousCheck.successCounter
		}
		c.checks.Store(checkHash, updatedCheck)
	}

	// Look for removed checks
	c.checks.Range(func(_, _check any) bool {
		check := _check.(*esmHealthCheck)
		checkHash := hashCheck(&check.HealthCheck)
		if _, ok := found[checkHash]; !ok {
			c.logger.Debug("Deleting check %q", "checkHash", checkHash)
			c.checks.Delete(checkHash)
			c.checksCritical.Delete(checkHash)

			if httpCheck, httpCheckExists := c.checksHTTP.Load(checkHash); httpCheckExists {
				httpCheck.Stop()
				c.checksHTTP.Delete(checkHash)
			}
			if tcpCheck, tcpCheckExists := c.checksTCP.Load(checkHash); tcpCheckExists {
				tcpCheck.Stop()
				c.checksTCP.Delete(checkHash)
			}

			removed[checkHash] = true
		}
		return true
	})

	if len(added) > 0 || len(updated) > 0 || len(removed) > 0 {
		c.logger.Info("Updated checks", "count",
			len(checks), "found", len(found), "added", len(added), "updated", len(updated), "removed", len(removed))
	}
}

// ServiceExists is part of the consulchecks.CheckNotifier interface.
// It is currently used as part of Consul's alias service feature.
// This function is used to check for localality of service.
// Unsuppported at this time, so hardcoded false return.
func (c *CheckRunner) ServiceExists(serviceID structs.ServiceID) bool {
	return false
}

// UpdateCheck handles the output of an HTTP/TCP check and decides whether or not
// to push an update to the catalog.
func (c *CheckRunner) UpdateCheck(checkID structs.CheckID, status, output string) {
	checkHash := checkID.ID
	check, ok := c.checks.Load(checkHash)
	if !ok {
		return
	}

	// Do nothing if update is idempotent
	if check.Status == status && check.Output == output {
		if status == api.HealthCritical {
			if _, ok := c.checksCritical.Load(checkHash); !ok {
				c.checksCritical.Store(checkHash, time.Now())
			}
		}
		check.failureCounter = decrementCounter(check.failureCounter)
		check.successCounter = decrementCounter(check.successCounter)
		return
	}

	if status == api.HealthCritical {
		if check.failureCounter < c.CriticalThreshold {
			check.failureCounter++
			return
		}
		check.failureCounter = 0
	} else {
		if check.successCounter < c.PassingThreshold {
			check.successCounter++
			return
		}
		check.successCounter = 0
	}

	// Update the critical time tracking
	if status == api.HealthCritical {
		if _, ok := c.checksCritical.Load(checkHash); !ok {
			c.checksCritical.Store(checkHash, time.Now())
		}
	} else {
		c.checksCritical.Delete(checkHash)
	}

	// Defer a sync if the output has changed. This is an optimization around
	// frequent updates of output. Instead, we update the output internally,
	// and periodically do a write-back to the servers. If there is a status
	// change we do the write immediately.
	if c.CheckUpdateInterval > 0 && check.Status == status {
		check.Output = output
		if _, ok := c.deferCheck.Load(checkHash); !ok {
			// Enhanced batching for agentless mode - use longer intervals for better server efficiency
			var intv time.Duration
			if c.isAgentless {
				// Agentless mode: use more aggressive batching to reduce server load
				intv = time.Duration(uint64(c.CheckUpdateInterval)) + lib.RandomStagger(c.CheckUpdateInterval)
			} else {
				// Agent mode: use original batching logic
				intv = time.Duration(uint64(c.CheckUpdateInterval)/2) + lib.RandomStagger(c.CheckUpdateInterval)
			}
			deferSync := time.AfterFunc(intv, func() {
				// For deferred updates, we don't need original state since the check
				// has already stabilized at this status
				c.handleCheckUpdate(&check.HealthCheck, status, output, "", "")
				c.deferCheck.Delete(checkHash)
			})
			c.deferCheck.Store(checkHash, deferSync)
		}
		return
	}

	// For agentless mode: update local state immediately before batching.
	// This ensures anti-flapping logic and local state remain consistent
	// while server updates are batched for efficiency.
	// Store original state for potential reversion if batch fails.
	// For agentful mode: local state is updated after successful catalog update
	// (inside handleCheckUpdateImmediate) to maintain original behavior.
	originalStatus := check.Status
	originalOutput := check.Output
	if c.isAgentless {
		check.Status = status
		check.Output = output
	}

	c.handleCheckUpdate(&check.HealthCheck, status, output, originalStatus, originalOutput)
}

// handleCheckUpdate updates a check's status in Consul.
// In agentless mode, updates are batched to reduce HTTP connections.
// In agentful mode, updates are sent immediately (original behavior).
func (c *CheckRunner) handleCheckUpdate(check *api.HealthCheck, status, output, originalStatus, originalOutput string) {
	if c.batcher != nil {
		// Queue for batching. If the batcher has been stopped (e.g., during shutdown),
		// the update is silently dropped rather than falling through to the immediate path.
		c.batcher.Add(check, status, output, originalStatus, originalOutput)
	} else {
		// Agentful mode: send update immediately (original behavior)
		c.handleCheckUpdateImmediate(check, status, output)
	}
}

// handleCheckUpdateImmediate sends a check update to Consul immediately without batching.
// This is the exact original behavior preserved for agentful mode.
func (c *CheckRunner) handleCheckUpdateImmediate(check *api.HealthCheck, status, output string) {
	// Exit early if the check or node have been deregistered.
	// consistent mode reduces convergency time particularly when services have many updates in a short time
	checks, _, err := c.client.Health().Node(check.Node, &api.QueryOptions{
		Namespace:         check.Namespace,
		RequireConsistent: true,
	})
	if err != nil {
		c.logger.Warn("error retrieving existing node entry", "error", err)
		return
	}
	var existing *api.HealthCheck
	checkID := strings.TrimPrefix(string(check.CheckID), check.Node+"/")
	for _, check := range checks {
		if check.CheckID == checkID {
			existing = check
			break
		}
	}
	if existing == nil {
		return
	}

	existing.Status = status
	existing.Output = output

	c.logger.Info("Updating output and status for", "checkID", existing.CheckID)

	ops := api.TxnOps{
		&api.TxnOp{
			Check: &api.CheckTxnOp{
				Verb:  api.CheckCAS,
				Check: *existing,
			},
		},
	}
	metrics.IncrCounter([]string{"check", "txn"}, 1)
	ok, resp, _, err := c.client.Txn().Txn(ops, nil)
	if err != nil {
		c.logger.Warn("Error updating check status in Consul", "error", err)
		return
	}
	if len(resp.Errors) > 0 {
		var errs error
		for _, e := range resp.Errors {
			errs = multierror.Append(errs, errors.New(e.What))
		}
		c.logger.Warn("Error(s) returned from txn when updating check status in Consul", "error", errs)
		return
	}
	if !ok {
		c.logger.Warn("Failed to atomically update check status in Consul")
		return
	}

	c.logger.Trace("Registered check status to the catalog with ID", "checkId", strings.TrimPrefix(string(check.CheckID), check.Node+"/"))

	// Only update the local check state if we successfully updated the catalog
	check.Status = status
	check.Output = output
}

// processBatchedUpdates processes a slice of check updates in batches of maxTxnOps.
// This is called by the batcher when it's time to flush updates.
func (c *CheckRunner) processBatchedUpdates(updates []*pendingCheckUpdate) {
	if len(updates) == 0 {
		return
	}

	c.logger.Debug("Processing batched check updates", "totalUpdates", len(updates), "agentless", c.isAgentless)
	defer metrics.MeasureSince([]string{"check", "batch", "flush"}, time.Now())

	// First, fetch current state for all nodes involved
	nodeChecks := make(map[string]map[string]*api.HealthCheck) // node -> checkID -> check
	for _, update := range updates {
		if _, exists := nodeChecks[update.check.Node]; !exists {
			checks, _, err := c.client.Health().Node(update.check.Node, &api.QueryOptions{
				Namespace:         update.check.Namespace,
				RequireConsistent: true,
			})
			if err != nil {
				c.logger.Warn("error retrieving existing node entry for batch", "error", err, "node", update.check.Node)
				continue
			}
			nodeChecks[update.check.Node] = make(map[string]*api.HealthCheck)
			for _, check := range checks {
				nodeChecks[update.check.Node][check.CheckID] = check
			}
		}
	}

	// Build transaction operations
	var ops api.TxnOps
	var processedChecks []*pendingCheckUpdate

	for _, update := range updates {
		checkID := strings.TrimPrefix(string(update.check.CheckID), update.check.Node+"/")
		nodeMap, nodeExists := nodeChecks[update.check.Node]
		if !nodeExists {
			continue
		}
		existing, checkExists := nodeMap[checkID]
		if !checkExists {
			c.logger.Trace("Check no longer exists, skipping", "checkID", checkID)
			continue
		}

		existing.Status = update.status
		existing.Output = update.output

		ops = append(ops, &api.TxnOp{
			Check: &api.CheckTxnOp{
				Verb:  api.CheckCAS,
				Check: *existing,
			},
		})
		processedChecks = append(processedChecks, update)

		// Flush when we hit the batch size limit
		if len(ops) >= maxTxnOps {
			c.executeBatchTransaction(ops, processedChecks)
			ops = nil
			processedChecks = nil
		}
	}

	// Flush any remaining operations
	if len(ops) > 0 {
		c.executeBatchTransaction(ops, processedChecks)
	}
}

// executeBatchTransaction executes a batch of check updates in a single transaction.
func (c *CheckRunner) executeBatchTransaction(ops api.TxnOps, updates []*pendingCheckUpdate) {
	if len(ops) == 0 {
		return
	}

	metrics.IncrCounter([]string{"check", "txn"}, 1)
	metrics.SetGauge([]string{"check", "txn", "batch_size"}, float32(len(ops)))

	c.logger.Info("Executing batched check update transaction", "batchSize", len(ops), "agentless", c.isAgentless)

	// Agentless mode optimizations for enhanced server communication
	var queryOpts *api.QueryOptions
	if c.isAgentless {
		queryOpts = &api.QueryOptions{
			RequireConsistent: true,
		}
	}

	ok, resp, _, err := c.client.Txn().Txn(ops, queryOpts)
	if err != nil {
		if c.isAgentless {
			c.logger.Warn("Error updating check status in Consul (agentless mode, batched)", "error", err, "batchSize", len(ops))
		} else {
			c.logger.Warn("Error updating check status in Consul (batched)", "error", err, "batchSize", len(ops))
		}
		return
	}

	if len(resp.Errors) > 0 {
		// Check if errors are due to stale index (CAS failures)
		staleIndexErrors := 0
		for _, e := range resp.Errors {
			if strings.Contains(e.What, "index is stale") {
				staleIndexErrors++
			}
		}

		// If we have stale index errors, retry those specific operations
		if staleIndexErrors > 0 {
			c.logger.Debug("Detected stale index errors in batch, retrying failed operations",
				"staleErrors", staleIndexErrors, "totalErrors", len(resp.Errors))
			c.retryFailedBatchOperations(updates, resp.Errors)
		} else {
			var errs error
			for _, e := range resp.Errors {
				errs = multierror.Append(errs, errors.New(e.What))
			}
			c.logger.Warn("Error(s) returned from batched txn when updating check status", "error", errs, "failedOps", len(resp.Errors))
		}
		return
	}

	if !ok {
		c.logger.Warn("Failed to atomically update batched check status in Consul")
		return
	}

	// Note: Local state is already updated in UpdateCheck() before queuing.
	// This ensures anti-flapping logic works correctly regardless of batch timing.

	c.logger.Debug("Successfully executed batched check update", "batchSize", len(ops))
}

// retryFailedBatchOperations retries check updates that failed due to stale index.
func (c *CheckRunner) retryFailedBatchOperations(updates []*pendingCheckUpdate, errors api.TxnErrors) {
	// Build a map of failed operation indices
	failedIndices := make(map[int]string)
	for _, e := range errors {
		if strings.Contains(e.What, "index is stale") {
			failedIndices[e.OpIndex] = e.What
		}
	}

	if len(failedIndices) == 0 {
		return
	}

	c.logger.Info("Retrying failed batch operations with fresh state", "count", len(failedIndices))
	metrics.IncrCounter([]string{"check", "txn", "retry"}, float32(len(failedIndices)))

	// Retry each failed update individually with fresh state
	for idx, errMsg := range failedIndices {
		if idx >= len(updates) {
			c.logger.Warn("Invalid error index in transaction response", "index", idx, "updatesCount", len(updates))
			continue
		}

		update := updates[idx]
		c.logger.Trace("Retrying check update", "node", update.check.Node, "checkID", update.check.CheckID, "error", errMsg)

		// Fetch fresh state for this specific check
		checks, _, err := c.client.Health().Node(update.check.Node, &api.QueryOptions{
			Namespace:         update.check.Namespace,
			RequireConsistent: true,
		})
		if err != nil {
			c.logger.Warn("error retrieving node for retry", "error", err, "node", update.check.Node)
			continue
		}

		// Find the specific check
		checkID := strings.TrimPrefix(string(update.check.CheckID), update.check.Node+"/")
		var existing *api.HealthCheck
		for _, check := range checks {
			if check.CheckID == checkID {
				existing = check
				break
			}
		}

		if existing == nil {
			c.logger.Trace("Check no longer exists during retry, skipping", "checkID", checkID)
			continue
		}

		// Update with fresh ModifyIndex
		existing.Status = update.status
		existing.Output = update.output

		// Execute single-operation transaction with fresh state
		ops := api.TxnOps{
			&api.TxnOp{
				Check: &api.CheckTxnOp{
					Verb:  api.CheckCAS,
					Check: *existing,
				},
			},
		}

		var queryOpts *api.QueryOptions
		if c.isAgentless {
			queryOpts = &api.QueryOptions{
				RequireConsistent: true,
			}
		}

		ok, retryResp, _, err := c.client.Txn().Txn(ops, queryOpts)
		if err != nil {
			c.logger.Warn("error during retry", "error", err, "node", update.check.Node, "checkID", checkID)
			continue
		}

		if len(retryResp.Errors) > 0 {
			c.logger.Warn("retry still failed, reverting local state", "node", update.check.Node, "checkID", checkID, "error", retryResp.Errors[0].What)
			// Revert local state to original so next check execution can retry
			checkHash := hashCheck(update.check)
			c.revertCheckState(update.check.Node, checkHash, update.originalStatus, update.originalOutput)
			continue
		}

		if !ok {
			c.logger.Warn("retry transaction returned not ok", "node", update.check.Node, "checkID", checkID)
			continue
		}

		c.logger.Debug("Successfully retried check update", "node", update.check.Node, "checkID", checkID)
	}
}

// reapServices is a long running goroutine that looks for checks that have been
// critical too long and deregisters their associated services.
func (c *CheckRunner) reapServices(shutdownCh <-chan struct{}) {
	for {
		select {
		case <-time.After(30 * time.Second):
			c.reapServicesInternal()

		case <-shutdownCh:
			return
		}
	}
}

// reapServicesInternal does a single pass, looking for services to reap.
func (c *CheckRunner) reapServicesInternal() {
	type uniqueID struct {
		node, service string
	}

	reaped := make(map[uniqueID]bool)
	c.checksCritical.Range(func(_checkID, _criticalTime any) bool {
		checkID := _checkID.(types.CheckID)
		criticalTime := _criticalTime.(time.Time)

		check, ok := c.checks.Load(checkID)
		if !ok {
			return true
		}

		ID := uniqueID{node: check.Node, service: check.ServiceID}
		// There's nothing to do if there's no service.
		if ID.service == "" {
			return true
		}
		// There might be multiple checks for one service, so
		// we don't need to reap multiple times.
		if reaped[ID] {
			return true
		}

		timeout := check.Definition.DeregisterCriticalServiceAfterDuration
		if timeout > 0 && timeout < time.Since(criticalTime) {
			c.client.Catalog().Deregister(&api.CatalogDeregistration{
				Node:      ID.node,
				ServiceID: ID.service,
			}, nil)
			c.logger.Info("agent has been critical for too long, deregistered service", "checkID", checkID,
				"nodeID", ID.node,
				"serviceID", ID.service,
				"duration", time.Since(criticalTime),
				"timeout", timeout)
			reaped[ID] = true
		}
		return true
	})
}

// revertCheckState reverts the local check state to previous values after a failed batch update.
// This ensures the next check execution will detect the difference and retry the update.
func (c *CheckRunner) revertCheckState(node string, checkID types.CheckID, originalStatus, originalOutput string) {
	checkHash := checkID
	check, ok := c.checks.Load(checkHash)
	if !ok {
		c.logger.Debug("Check not found for state reversion", "checkID", checkID)
		return
	}

	c.logger.Debug("Reverting check state after batch failure",
		"checkID", checkID,
		"currentStatus", check.Status,
		"revertToStatus", originalStatus)

	check.Status = originalStatus
	check.Output = originalOutput
	c.checks.Store(checkHash, check)
}

func hashCheck(check *api.HealthCheck) types.CheckID {
	if check.ServiceID != "" {
		return types.CheckID(fmt.Sprintf("%s/%s/%s", check.Node, check.ServiceID, check.CheckID))
	}
	return types.CheckID(fmt.Sprintf("%s/%s", check.Node, check.CheckID))
}

func decrementCounter(count int) int {
	if count == 0 {
		return 0
	}
	return count - 1
}
