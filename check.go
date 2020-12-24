package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/armon/go-metrics"
	consulchecks "github.com/hashicorp/consul/agent/checks"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/consul/types"
	"github.com/hashicorp/go-multierror"
)

const externalCheckName = "externalNodeHealth"

var (
	// defaultInterval is the check interval to use if one is not set.
	defaultInterval = 30 * time.Second
)

type checkIDSet map[types.CheckID]bool

type CheckRunner struct {
	sync.RWMutex

	logger *log.Logger
	client *api.Client

	// checks are unmodified checks as retrieved from Consul Catalog
	checks map[types.CheckID]*esmHealthCheck

	// checksHTTP & checksTCP are HTTP/TCP checks that are run by ESM.
	// They have potentially modified values from the Consul Catalog checks
	checksHTTP map[types.CheckID]*consulchecks.CheckHTTP
	checksTCP  map[types.CheckID]*consulchecks.CheckTCP

	checksCritical map[types.CheckID]time.Time

	// Used to track checks that are being deferred
	deferCheck map[types.CheckID]*time.Timer

	CheckUpdateInterval time.Duration
	MinimumInterval     time.Duration

	tlsConfig *tls.Config

	PassingThreshold  int
	CriticalThreshold int
}

type esmHealthCheck struct {
	api.HealthCheck
	failureCounter int
	successCounter int
}

func NewCheckRunner(logger *log.Logger, client *api.Client, updateInterval,
	minimumInterval time.Duration, tlsConfig *tls.Config, passingThreshold int,
	criticalThreshold int) *CheckRunner {
	return &CheckRunner{
		logger:              logger,
		client:              client,
		checks:              make(map[types.CheckID]*esmHealthCheck),
		checksHTTP:          make(map[types.CheckID]*consulchecks.CheckHTTP),
		checksTCP:           make(map[types.CheckID]*consulchecks.CheckTCP),
		checksCritical:      make(map[types.CheckID]time.Time),
		deferCheck:          make(map[types.CheckID]*time.Timer),
		CheckUpdateInterval: updateInterval,
		MinimumInterval:     minimumInterval,
		tlsConfig:           tlsConfig,
		PassingThreshold:    passingThreshold,
		CriticalThreshold:   criticalThreshold,
	}
}

func (c *CheckRunner) Stop() {
	c.Lock()
	defer c.Unlock()

	for _, check := range c.checksHTTP {
		check.Stop()
	}

	for _, check := range c.checksTCP {
		check.Stop()
	}
}

// Update an HTTP check
func (c *CheckRunner) updateCheckHTTP(latestCheck *api.HealthCheck, checkHash types.CheckID,
	definition *api.HealthCheckDefinition, updated, added checkIDSet) bool {
	tlsConfig := c.tlsConfig.Clone()
	tlsConfig.InsecureSkipVerify = definition.TLSSkipVerify

	http := &consulchecks.CheckHTTP{
		Notify:          c,
		CheckID:         checkHash,
		HTTP:            definition.HTTP,
		Header:          definition.Header,
		Method:          definition.Method,
		Interval:        definition.IntervalDuration,
		Timeout:         definition.TimeoutDuration,
		Logger:          c.logger,
		TLSClientConfig: tlsConfig,
	}

	if check, checkExists := c.checks[checkHash]; checkExists {
		httpCheck, httpCheckExists := c.checksHTTP[checkHash]
		if httpCheckExists &&
			httpCheck.HTTP == http.HTTP &&
			reflect.DeepEqual(httpCheck.Header, http.Header) &&
			httpCheck.Method == http.Method &&
			httpCheck.TLSClientConfig.InsecureSkipVerify == http.TLSClientConfig.InsecureSkipVerify &&
			httpCheck.Interval == http.Interval &&
			httpCheck.Timeout == http.Timeout &&
			check.Definition.DeregisterCriticalServiceAfter == definition.DeregisterCriticalServiceAfter {
			return false
		}

		c.logger.Printf("[INFO] Updating HTTP check %q", checkHash)

		if httpCheckExists {
			httpCheck.Stop()
		} else {
			tcpCheck, tcpCheckExists := c.checksTCP[checkHash]
			if !tcpCheckExists {
				c.logger.Printf("[WARN] Inconsistency check %q - is not TCP and HTTP", checkHash)
				return false
			}
			tcpCheck.Stop()
			delete(c.checksTCP, checkHash)
		}

		updated[checkHash] = true
	} else {
		added[checkHash] = true
	}

	http.Start()
	c.checksHTTP[checkHash] = http

	return true
}

func (c *CheckRunner) updateCheckTCP(latestCheck *api.HealthCheck, checkHash types.CheckID,
	definition *api.HealthCheckDefinition, updated, added checkIDSet) bool {
	tcp := &consulchecks.CheckTCP{
		Notify:   c,
		CheckID:  checkHash,
		TCP:      definition.TCP,
		Interval: definition.IntervalDuration,
		Timeout:  definition.TimeoutDuration,
		Logger:   c.logger,
	}

	if check, checkExists := c.checks[checkHash]; checkExists {
		tcpCheck, tcpCheckExists := c.checksTCP[checkHash]
		if tcpCheckExists &&
			tcpCheck.TCP == tcp.TCP &&
			tcpCheck.Interval == tcp.Interval &&
			tcpCheck.Timeout == tcp.Timeout &&
			check.Definition.DeregisterCriticalServiceAfter == definition.DeregisterCriticalServiceAfter {
			return false
		}

		c.logger.Printf("[INFO] Updating TCP check %q", checkHash)

		if tcpCheckExists {
			tcpCheck.Stop()
		} else {
			httpCheck, httpCheckExists := c.checksHTTP[checkHash]
			if !httpCheckExists {
				c.logger.Printf("[WARN] Inconsistency check %q - is not TCP and HTTP", checkHash)
				return false
			}
			httpCheck.Stop()
			delete(c.checksHTTP, checkHash)
		}

		updated[checkHash] = true
	} else {
		added[checkHash] = true
	}

	tcp.Start()
	c.checksTCP[checkHash] = tcp

	return true
}

// UpdateChecks takes a list of checks from the catalog and updates
// our list of running checks to match.
func (c *CheckRunner) UpdateChecks(checks api.HealthChecks) {
	defer metrics.MeasureSince([]string{"checks", "update"}, time.Now())
	c.Lock()
	defer c.Unlock()

	found := make(checkIDSet)

	added := make(checkIDSet)
	updated := make(checkIDSet)
	removed := make(checkIDSet)

	for _, check := range checks {
		// Skip the ping-based node check since we're managing that separately
		if check.CheckID == externalCheckName {
			continue
		}

		checkHash := checkHash(check)

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
			c.logger.Printf("[WARN] check %q is not a valid HTTP or TCP check", checkHash)
			continue
		}

		// if we had to fix the interval and we had to update the service, put some trace out
		unmodifiedDef := check.Definition
		if anyUpdates && unmodifiedDef.IntervalDuration < c.MinimumInterval {
			c.logger.Printf("[WARN] Check interval too low at %v for check %s", unmodifiedDef.Interval, check.Name)
		}

		found[checkHash] = true
		c.checks[checkHash] = &esmHealthCheck{
			*check,
			0,
			0,
		}
	}

	// Look for removed checks
	for _, check := range c.checks {
		checkHash := checkHash(&check.HealthCheck)
		if _, ok := found[checkHash]; !ok {
			delete(c.checks, checkHash)
			delete(c.checksCritical, checkHash)

			if httpCheck, httpCheckExists := c.checksHTTP[checkHash]; httpCheckExists {
				httpCheck.Stop()
				delete(c.checksHTTP, checkHash)
			}
			if tcpCheck, tcpCheckExists := c.checksTCP[checkHash]; tcpCheckExists {
				tcpCheck.Stop()
				delete(c.checksTCP, checkHash)
			}

			removed[checkHash] = true
		}
	}

	if len(added) > 0 || len(updated) > 0 || len(removed) > 0 {
		c.logger.Printf("[INFO] Updated %d checks, found %d, added %d, updated %d, removed %d",
			len(checks), len(found), len(added), len(updated), len(removed))
	}
}

// UpdateCheck handles the output of an HTTP/TCP check and decides whether or not
// to push an update to the catalog.
func (c *CheckRunner) UpdateCheck(checkID types.CheckID, status, output string) {
	c.Lock()
	defer c.Unlock()

	check, ok := c.checks[checkID]
	if !ok {
		return
	}

	if status == api.HealthCritical {
		check.successCounter = 0
		if check.failureCounter < c.CriticalThreshold {
			check.failureCounter++
			return
		}

	} else {
		check.failureCounter = 0
		if check.successCounter < c.PassingThreshold {
			check.successCounter++
			return
		}

	}

	// Update the critical time tracking
	if status == api.HealthCritical {
		if _, ok := c.checksCritical[checkID]; !ok {
			c.checksCritical[checkID] = time.Now()
		}
	} else {
		delete(c.checksCritical, checkID)
	}

	// Do nothing if update is idempotent
	if check.Status == status && check.Output == output {
		return
	}
	// Defer a sync if the output has changed. This is an optimization around
	// frequent updates of output. Instead, we update the output internally,
	// and periodically do a write-back to the servers. If there is a status
	// change we do the write immediately.
	if c.CheckUpdateInterval > 0 && check.Status == status {
		check.Output = output
		if _, ok := c.deferCheck[checkID]; !ok {
			intv := time.Duration(uint64(c.CheckUpdateInterval)/2) + lib.RandomStagger(c.CheckUpdateInterval)
			deferSync := time.AfterFunc(intv, func() {
				c.Lock()
				c.handleCheckUpdate(&check.HealthCheck, status, output)
				delete(c.deferCheck, checkID)
				c.Unlock()
			})
			c.deferCheck[checkID] = deferSync
		}
		return
	}

	c.handleCheckUpdate(&check.HealthCheck, status, output)
}

// handleCheckUpdate writes a check's status to the catalog and updates the local check state.
// Should only be called when the lock is held.
func (c *CheckRunner) handleCheckUpdate(check *api.HealthCheck, status, output string) {
	// Exit early if the check or node have been deregistered.
	// consistent mode reduces convergency time particularly when services have many updates in a short time
	checks, _, err := c.client.Health().Node(check.Node, &api.QueryOptions{RequireConsistent: true})
	if err != nil {
		c.logger.Printf("[WARN] error retrieving existing node entry: %v", err)
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

	c.logger.Printf("[INFO] Updating output and status for %q", existing.CheckID)

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
		c.logger.Printf("[WARN] Error updating check status in Consul: %v", err)
		return
	}
	if len(resp.Errors) > 0 {
		var errs error
		for _, e := range resp.Errors {
			errs = multierror.Append(errs, errors.New(e.What))
		}
		c.logger.Printf("[WARN] Error(s) returned from txn when updating check status in Consul: %v", errs)
		return
	}
	if !ok {
		c.logger.Printf("[WARN] Failed to atomically update check status in Consul")
		return
	}

	c.logger.Printf("[TRACE] Registered check status to the catalog with ID %v", strings.TrimPrefix(string(check.CheckID), check.Node+"/"))

	// Only update the local check state if we successfully updated the catalog
	check.Status = status
	check.Output = output
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
	c.Lock()
	defer c.Unlock()

	reaped := make(map[string]bool)
	for checkID, criticalTime := range c.checksCritical {
		check := c.checks[checkID]
		serviceID := check.ServiceID

		// There's nothing to do if there's no service.
		if serviceID == "" {
			continue
		}

		// There might be multiple checks for one service, so
		// we don't need to reap multiple times.
		if reaped[serviceID] {
			continue
		}

		timeout := check.Definition.DeregisterCriticalServiceAfterDuration
		if timeout > 0 && timeout < time.Since(criticalTime) {
			c.client.Catalog().Deregister(&api.CatalogDeregistration{
				Node:      check.Node,
				ServiceID: serviceID,
			}, nil)
			c.logger.Printf("[INFO] agent: Check %q for service %q has been critical for too long (%s | timeout: %s); deregistered service",
				checkID, serviceID,
				time.Since(criticalTime), timeout)
			reaped[serviceID] = true
		}
	}
}

func checkHash(check *api.HealthCheck) types.CheckID {
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
