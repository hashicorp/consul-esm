package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"context"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-uuid"
)

const (
	// agentTTL controls the TTL of the "agent alive" check, and also
	// determines how often we poll the agent to check on service
	// registration.
	agentTTL = 30 * time.Second

	// retryTime is used as a dead time before retrying a failed operation,
	// and is also used when trying to give up leadership to another
	// instance.
	retryTime = 10 * time.Second
)

type Agent struct {
	config *Config
	client *api.Client
	logger *log.Logger
	id     string
}

func NewAgent(config *Config, logger *log.Logger) (*Agent, error) {
	clientConf := config.ClientConfig()
	client, err := api.NewClient(clientConf)
	if err != nil {
		return nil, err
	}

	// Generate a unique ID for this agent so we can disambiguate different
	// instances on the same host.
	id, err := uuid.GenerateUUID()
	if err != nil {
		return nil, err
	}

	agent := Agent{
		config: config,
		client: client,
		id:     id,
		logger: logger,
	}

	for {
		leader, err := client.Status().Leader()
		if err != nil {
			logger.Printf("[ERR] error getting leader status: %q, retrying in %s...", err.Error(), retryTime.String())
		} else if leader == "" {
			logger.Printf("[INFO] waiting for cluster to elect a leader before starting, will retry in %s...", retryTime.String())
		} else {
			break
		}

		time.Sleep(retryTime)
	}

	return &agent, nil
}

func (a *Agent) Run(shutdownCh <-chan struct{}) error {
	// Do the initial service registration.
	serviceID := fmt.Sprintf("%s:%s", a.config.Service, a.id)
	if err := a.register(serviceID); err != nil {
		return err
	}

	// todo: compress this once things are more finalized
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		a.runRegister(serviceID, shutdownCh)
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runTTL(serviceID, shutdownCh)
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runHealthChecks(shutdownCh)
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.getExternalNodes(shutdownCh)
		wg.Done()
	}()

	// Wait for shutdown.
	<-shutdownCh
	wg.Wait()

	// Clean up.
	if err := a.client.Agent().ServiceDeregister(serviceID); err != nil {
		return err
	}

	return nil
}

// register is used to register this agent with Consul service discovery.
func (a *Agent) register(serviceID string) error {
	service := &api.AgentServiceRegistration{
		ID:   serviceID,
		Name: a.config.Service,
	}
	if err := a.client.Agent().ServiceRegister(service); err != nil {
		return err
	}

	return nil
}

// runRegister is a long-running goroutine that ensures this agent is registered
// with Consul's service discovery. It will run until the shutdownCh is closed.
func (a *Agent) runRegister(serviceID string, shutdownCh <-chan struct{}) {
	for {
	REGISTER_CHECK:
		select {
		case <-shutdownCh:
			return

		case <-time.After(agentTTL):
			services, err := a.client.Agent().Services()
			if err != nil {
				a.logger.Printf("[ERR] Failed to check services (will retry): %v", err)
				time.Sleep(retryTime)
				goto REGISTER_CHECK
			}

			if _, ok := services[serviceID]; !ok {
				a.logger.Printf("[WARN] Service registration was lost, reregistering")
				if err := a.register(serviceID); err != nil {
					a.logger.Printf("[ERR] Failed to reregister service (will retry): %v", err)
					time.Sleep(retryTime)
					goto REGISTER_CHECK
				}
			}
		}
	}
}

// runTTL is a long-running goroutine that registers an "agent alive" TTL health
// check and services it periodically. It will run until the shutdownCh is closed.
func (a *Agent) runTTL(serviceID string, shutdownCh <-chan struct{}) {
	ttlID := fmt.Sprintf("%s:agent-ttl", serviceID)

REGISTER:
	select {
	case <-shutdownCh:
		return
	default:
	}

	// Perform an initial registration with retries.
	check := &api.AgentCheckRegistration{
		ID:        ttlID,
		Name:      "Consul External Service Monitor Alive",
		Notes:     "This check is periodically updated as long as the agent is alive.",
		ServiceID: serviceID,
		AgentServiceCheck: api.AgentServiceCheck{
			TTL:    agentTTL.String(),
			Status: api.HealthPassing,
			DeregisterCriticalServiceAfter: a.config.DeregisterAfter.String(),
		},
	}
	if err := a.client.Agent().CheckRegister(check); err != nil {
		a.logger.Printf("[ERR] Failed to register TTL check (will retry): %v", err)
		time.Sleep(retryTime)
		goto REGISTER
	}

	// Wait for the shutdown signal and poke the TTL check.
	for {
		select {
		case <-shutdownCh:
			return

		case <-time.After(agentTTL / 2):
			if err := a.client.Agent().UpdateTTL(ttlID, "", api.HealthPassing); err != nil {
				a.logger.Printf("[ERR] Failed to refresh agent TTL check (will reregister): %v", err)
				time.Sleep(retryTime)
				goto REGISTER
			}
		}
	}
}

func (a *Agent) runHealthChecks(shutdownCh <-chan struct{}) {
	// Arrange to give up any held lock any time we exit the goroutine so
	// another agent can pick up without delay.
	var lock *api.Lock
	defer func() {
		if lock != nil {
			lock.Unlock()
		}
	}()

LEADER_WAIT:
	select {
	case <-shutdownCh:
		return
	default:
	}

	// Wait to get the leader lock before running snapshots.
	a.logger.Printf("[INFO] Waiting to obtain leadership...")
	if lock == nil {
		var err error
		lock, err = a.client.LockKey(a.config.LeaderKey)
		if err != nil {
			a.logger.Printf("[ERR] Error trying to create leader lock (will retry): %v", err)
			time.Sleep(retryTime)
			goto LEADER_WAIT
		}
	}

	leaderCh, err := lock.Lock(shutdownCh)
	if err != nil {
		if err == api.ErrLockHeld {
			a.logger.Printf("[ERR] Unable to use leader lock that was held previously and presumed lost, giving up the lock (will retry): %v", err)
			lock.Unlock()
			time.Sleep(retryTime)
			goto LEADER_WAIT
		} else {
			a.logger.Printf("[ERR] Error trying to get leader lock (will retry): %v", err)
			time.Sleep(retryTime)
			goto LEADER_WAIT
		}
	}
	if leaderCh == nil {
		// This is how the Lock() call lets us know that it quit because
		// we closed the shutdown channel.
		return
	}
	a.logger.Printf("[INFO] Obtained leadership")

	// Set up a watch for any health checks on external nodes.
	updateCh := make(chan api.HealthChecks, 0)
	checkRunner := NewCheckRunner(a.logger, a.client, a.config.CheckUpdateInterval)
	go a.watchHealthChecks(updateCh, leaderCh)
	go checkRunner.reapServices(leaderCh)

	for {
		// Wait for the next event.
		select {
		case checks := <-updateCh:
			checkRunner.UpdateChecks(checks)
		case <-leaderCh:
			a.logger.Printf("[WARN] Lost leadership, stopping checks")
			checkRunner.Stop()
			goto LEADER_WAIT
		case <-shutdownCh:
			return
		}
	}
}

// watchHealthChecks does a blocking query to the Consul api to get
// all health checks on nodes marked with the external node metadata
// identifier and sends any updates through the given updateCh.
func (a *Agent) watchHealthChecks(updateCh chan api.HealthChecks, shutdownCh <-chan struct{}) {
	opts := &api.QueryOptions{
		NodeMeta: a.config.NodeMeta,
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-shutdownCh
		cancelFunc()
	}()

	firstRun := make(chan struct{}, 1)
	firstRun <- struct{}{}
	for {
		select {
		case <-shutdownCh:
			return
		case <-firstRun:
			// Skip the wait on the first run.
		case <-time.After(retryTime):
			// Sleep here to limit how much load we put on the Consul servers.
		}

		checks, meta, err := a.client.Health().State(api.HealthAny, opts)
		if err != nil {
			a.logger.Printf("[WARN] Error querying for health check info: %v", err)
			continue
		}

		opts.WaitIndex = meta.LastIndex
		updateCh <- checks
	}
}
