package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul/agent"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/consul/types"
)

type CheckRunner struct {
	sync.RWMutex

	logger *log.Logger
	client *api.Client

	checks     map[types.CheckID]*api.HealthCheck
	checksHTTP map[types.CheckID]*agent.CheckHTTP
	checksTCP  map[types.CheckID]*agent.CheckTCP

	// Used to track checks that are being deferred
	deferCheck map[types.CheckID]*time.Timer

	CheckUpdateInterval time.Duration
}

func NewCheckRunner(logger *log.Logger, client *api.Client, updateInterval time.Duration) *CheckRunner {
	return &CheckRunner{
		logger:              logger,
		client:              client,
		checks:              make(map[types.CheckID]*api.HealthCheck),
		checksHTTP:          make(map[types.CheckID]*agent.CheckHTTP),
		checksTCP:           make(map[types.CheckID]*agent.CheckTCP),
		deferCheck:          make(map[types.CheckID]*time.Timer),
		CheckUpdateInterval: updateInterval,
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

// UpdateChecks takes a list of checks from the catalog and updates
// our list of running checks to match.
func (c *CheckRunner) UpdateChecks(checks api.HealthChecks) {
	c.Lock()
	defer c.Unlock()

	found := make(map[types.CheckID]struct{})

	for _, check := range checks {
		checkHash := types.CheckID(checkHash(check))
		found[checkHash] = struct{}{}
		if _, ok := c.checks[checkHash]; ok {
			continue
		}

		interval, _ := time.ParseDuration(check.Interval)
		timeout, _ := time.ParseDuration(check.Timeout)
		if interval < agent.MinInterval {
			c.logger.Printf("[WARN] check '%s' has interval below minimum of %v",
				check.CheckID, agent.MinInterval)
			interval = agent.MinInterval
		}

		if check.HTTP != "" {
			http := &agent.CheckHTTP{
				Notify:        c,
				CheckID:       checkHash,
				HTTP:          check.HTTP,
				Header:        check.Header,
				Method:        check.Method,
				TLSSkipVerify: check.TLSSkipVerify,
				Interval:      interval,
				Timeout:       timeout,
				Logger:        c.logger,
			}

			http.Start()
			c.checksHTTP[checkHash] = http
		} else if check.TCP != "" {
			tcp := &agent.CheckTCP{
				Notify:   c,
				CheckID:  checkHash,
				TCP:      check.TCP,
				Interval: interval,
				Timeout:  timeout,
				Logger:   c.logger,
			}

			tcp.Start()
			c.checksTCP[checkHash] = tcp
		} else {
			c.logger.Printf("[WARN] check %q is not a valid HTTP or TCP check", checkHash)
			continue
		}

		c.checks[checkHash] = check
	}

	// Look for removed checks
	for _, check := range c.checks {
		checkHash := types.CheckID(checkHash(check))
		if _, ok := found[checkHash]; !ok {
			delete(c.checks, checkHash)
			if check.HTTP != "" {
				httpCheck := c.checksHTTP[checkHash]
				httpCheck.Stop()
				delete(c.checksHTTP, checkHash)
			} else {
				tcpCheck := c.checksTCP[checkHash]
				tcpCheck.Stop()
				delete(c.checksHTTP, checkHash)
			}
		}
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
				c.handleCheckUpdate(check, status, output)
				delete(c.deferCheck, checkID)
				c.Unlock()
			})
			c.deferCheck[checkID] = deferSync
		}
		return
	}

	c.handleCheckUpdate(check, status, output)
}

// handleCheckUpdate writes a check's status to the catalog and updates the local check state.
// Should only be called when the lock is held.
func (c *CheckRunner) handleCheckUpdate(check *api.HealthCheck, status, output string) {
	reg := &api.CatalogRegistration{
		Node: check.Node,
		Check: &api.AgentCheck{
			Node:        check.Node,
			CheckID:     strings.TrimPrefix(string(check.CheckID), check.Node+"/"),
			Name:        check.Name,
			Status:      status,
			Notes:       check.Notes,
			Output:      output,
			ServiceID:   check.ServiceID,
			ServiceName: check.ServiceName,
			HTTP:        check.HTTP,
			TCP:         check.TCP,
			Timeout:     check.Timeout,
			Interval:    check.Interval,
		},
		SkipNodeUpdate: true,
	}
	_, err := c.client.Catalog().Register(reg, nil)
	if err != nil {
		c.logger.Printf("[ERR] Error updating check status in Consul: %v", err)
		return
	}

	// Only update the local check state if we successfully updated the catalog
	check.Status = status
	check.Output = output
}

func checkHash(check *api.HealthCheck) types.CheckID {
	return types.CheckID(fmt.Sprintf("%s/%s", check.Node, check.CheckID))
}
