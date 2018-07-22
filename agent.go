package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-uuid"
)

var (
	// agentTTL controls the TTL of the "agent alive" check, and also
	// determines how often we poll the agent to check on service
	// registration.
	agentTTL = 30 * time.Second

	// retryTime is used as a dead time before retrying a failed operation,
	// and is also used when trying to give up leadership to another
	// instance.
	retryTime = 10 * time.Second

	// deregisterTime is the time the TTL check must be in a failed state for
	// the ESM service in consul to be deregistered.
	deregisterTime = 30 * time.Minute
)

type Agent struct {
	config *Config
	client *api.Client
	logger *log.Logger
	id     string

	shutdownCh chan struct{}
	shutdown   bool
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
		config:     config,
		client:     client,
		id:         id,
		logger:     logger,
		shutdownCh: make(chan struct{}),
	}

	logger.Printf("[INFO] Connecting to Consul on %s...", clientConf.Address)
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

func (a *Agent) Run() error {
	// Do the initial service registration.
	serviceID := fmt.Sprintf("%s:%s", a.config.Service, a.id)
	if err := a.register(serviceID); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		a.runRegister(serviceID)
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runTTL(serviceID)
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runHealthChecks(serviceID)
		wg.Done()
	}()

	// Wait for shutdown.
	<-a.shutdownCh
	wg.Wait()

	// Clean up.
	if err := a.client.Agent().ServiceDeregister(serviceID); err != nil {
		return err
	}

	return nil
}

func (a *Agent) Shutdown() {
	if !a.shutdown {
		a.shutdown = true
		close(a.shutdownCh)
	}
}

// register is used to register this agent with Consul service discovery.
func (a *Agent) register(serviceID string) error {
	service := &api.AgentServiceRegistration{
		ID:   serviceID,
		Name: a.config.Service,
	}
	if a.config.Tag != "" {
		service.Tags = []string{a.config.Tag}
	}
	if err := a.client.Agent().ServiceRegister(service); err != nil {
		return err
	}

	return nil
}

// runRegister is a long-running goroutine that ensures this agent is registered
// with Consul's service discovery. It will run until the shutdownCh is closed.
func (a *Agent) runRegister(serviceID string) {
	for {
	REGISTER_CHECK:
		select {
		case <-a.shutdownCh:
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
func (a *Agent) runTTL(serviceID string) {
	ttlID := fmt.Sprintf("%s:agent-ttl", serviceID)

REGISTER:
	select {
	case <-a.shutdownCh:
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
			DeregisterCriticalServiceAfter: deregisterTime.String(),
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
		case <-a.shutdownCh:
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

// runHealthChecks is the top-level goroutine responsible for running the
// node and health check watches and using those updates to inform the health check runner
// and node pinger.
func (a *Agent) runHealthChecks(serviceID string) {
	// Set up the update channels for the various goroutines.
	healthNodeCh := make(chan map[string]bool, 1)
	coordNodeCh := make(chan []*api.Node, 1)
	updateCh := make(chan api.HealthChecks, 0)

	// Start a goroutine to compute the nodes that this agent should be responsible for,
	// based on the currently healthy ESM instances with the same service tag as this agent.
	go a.computeWatchedNodes(serviceID, healthNodeCh, coordNodeCh)

	// Start a goroutine to get health check updates from the catalog, filtering them using
	// the results from computeWatchedNodes.
	go a.watchHealthChecks(updateCh, healthNodeCh)

	// Start a goroutine to run the pings used for coordinate and externalNodeHealth updates
	// on the nodes returned from computeWatchedNodes.
	go a.updateCoords(coordNodeCh)

	// Start a check runner to track and run the health checks we're responsible for and call
	// UpdateChecks when we get an update from watchHealthChecks.
	checkRunner := NewCheckRunner(a.logger, a.client, a.config.CheckUpdateInterval)
	go checkRunner.reapServices(a.shutdownCh)
	defer checkRunner.Stop()

	for {
		// Wait for the next event.
		select {
		case checks := <-updateCh:
			checkRunner.UpdateChecks(checks)
		case <-a.shutdownCh:
			return
		}
	}
}

// watchHealthChecks does a blocking query to the Consul api to get
// all health checks on nodes marked with the external node metadata
// identifier and sends any updates through the given updateCh.
func (a *Agent) watchHealthChecks(updateCh chan api.HealthChecks, nodeListCh chan map[string]bool) {
	opts := &api.QueryOptions{
		NodeMeta: a.config.NodeMeta,
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-a.shutdownCh
		cancelFunc()
	}()

	ourNodes := <-nodeListCh
	firstRun := make(chan struct{}, 1)
	firstRun <- struct{}{}
	for {
		select {
		case <-a.shutdownCh:
			return
		case <-firstRun:
			// Skip the wait on the first run.
		case ourNodes = <-nodeListCh:
			// Re-run if there's a change to the watched node list.
		case <-time.After(retryTime):
			// Sleep here to limit how much load we put on the Consul servers.
		}

		checks, meta, err := a.client.Health().State(api.HealthAny, opts)
		if err != nil {
			a.logger.Printf("[WARN] Error querying for health check info: %v", err)
			continue
		}

		ourChecks := make(api.HealthChecks, 0)
		for _, c := range checks {
			if ourNodes[c.Node] {
				ourChecks = append(ourChecks, c)
			}
		}

		opts.WaitIndex = meta.LastIndex
		updateCh <- checks
	}
}

// computeWatchedNodes watches both the list of registered ESM instances and the list of
// external nodes registered in Consul and decides which nodes the local agent should be
// in charge of in a deterministic way.
func (a *Agent) computeWatchedNodes(serviceID string, healthNodeCh chan map[string]bool, pingNodeCh chan []*api.Node) {
	nodeCh := make(chan []*api.Node)
	instanceCh := make(chan []*api.ServiceEntry)

	go a.watchExternalNodes(nodeCh)
	go a.watchServiceInstances(instanceCh)

	externalNodes := <-nodeCh
	healthyInstances := <-instanceCh

	firstRun := make(chan struct{}, 1)
	firstRun <- struct{}{}
	for {
		select {
		case <-a.shutdownCh:
			return
		case externalNodes = <-nodeCh:
		case healthyInstances = <-instanceCh:
		case <-firstRun:
			// Skip the wait on the first run.
		}

		// Get our "index" in the list of sorted service instances
		ourIndex := -1
		for i, instance := range healthyInstances {
			if instance.Service.ID == serviceID {
				ourIndex = i
				break
			}
		}
		if ourIndex == -1 {
			a.logger.Printf("[WARN] Didn't register service with Consul yet, retrying...")
			continue
		}

		healthCheckNodes := make(map[string]bool)
		var pingNodes []*api.Node
		for i := ourIndex; i < len(externalNodes); i += len(healthyInstances) {
			node := externalNodes[i]
			healthCheckNodes[node.Node] = true

			if node.Meta == nil {
				continue
			}
			if _, ok := node.Meta["external-probe"]; ok {
				pingNodes = append(pingNodes, node)
			}
		}

		a.logger.Printf("[DEBUG] Now watching %d external nodes", len(healthCheckNodes))

		healthNodeCh <- healthCheckNodes
		pingNodeCh <- pingNodes
	}
}

// watchExternalNodes does a watch for external nodes and returns any updates
// back through nodeCh as a sorted list.
func (a *Agent) watchExternalNodes(nodeCh chan []*api.Node) {
	opts := &api.QueryOptions{
		NodeMeta: a.config.NodeMeta,
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-a.shutdownCh
		cancelFunc()
	}()

	firstRun := make(chan struct{}, 1)
	firstRun <- struct{}{}
	for {
		select {
		case <-a.shutdownCh:
			return
		case <-firstRun:
			// Skip the wait on the first run.
		case <-time.After(retryTime):
			// Sleep here to limit how much load we put on the Consul servers.
		}

		// Do a blocking query for any external node changes
		externalNodes, meta, err := a.client.Catalog().Nodes(opts)
		if err != nil {
			a.logger.Printf("[WARN] Error getting external node list: %v", err)
			continue
		}
		sort.Slice(externalNodes, func(a, b int) bool {
			return externalNodes[a].Node < externalNodes[b].Node
		})

		opts.WaitIndex = meta.LastIndex

		nodeCh <- externalNodes
	}
}

// watchExternalNodes does a watch for any ESM instances with the same service tag as
// this agent and sends any updates back through instanceCh as a sorted list.
func (a *Agent) watchServiceInstances(instanceCh chan []*api.ServiceEntry) {
	var opts *api.QueryOptions
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-a.shutdownCh
		cancelFunc()
	}()

	for {
		select {
		case <-a.shutdownCh:
			return
		case <-time.After(retryTime / 10):
			// Sleep here to limit how much load we put on the Consul servers. We can
			// wait a lot less than the normal retry time here because the ESM service instance
			// list is small and cheap to query.
		}

		healthyInstances, meta, err := a.client.Health().Service(a.config.Service, a.config.Tag, true, opts)
		if err != nil {
			a.logger.Printf("[WARN] Error querying for health check info: %v", err)
			continue
		}
		sort.Slice(healthyInstances, func(a, b int) bool {
			return healthyInstances[a].Service.ID < healthyInstances[b].Service.ID
		})

		opts.WaitIndex = meta.LastIndex

		instanceCh <- healthyInstances
	}
}
