package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-uuid"
)

const LeaderKey = "leader"

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
	config      *Config
	client      *api.Client
	logger      *log.Logger
	checkRunner *CheckRunner
	id          string

	shutdownCh chan struct{}
	shutdown   bool

	inflightPings map[string]struct{}
	inflightLock  sync.Mutex

	// Custom func to hook into for testing.
	watchedNodeFunc func(map[string]bool, []*api.Node)
}

func NewAgent(config *Config, logger *log.Logger) (*Agent, error) {
	clientConf := config.ClientConfig()
	client, err := api.NewClient(clientConf)
	if err != nil {
		return nil, err
	}

	// Generate a unique ID for this agent so we can disambiguate different
	// instances on the same host.
	id := config.id
	if id == "" {
		id, err = uuid.GenerateUUID()
		if err != nil {
			return nil, err
		}
	}

	agent := Agent{
		config:        config,
		client:        client,
		id:            id,
		logger:        logger,
		shutdownCh:    make(chan struct{}),
		inflightPings: make(map[string]struct{}),
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
	if err := a.register(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		a.runRegister()
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runTTL()
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.watchNodeList()
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runLeaderLoop()
		wg.Done()
	}()

	// Wait for shutdown.
	<-a.shutdownCh
	wg.Wait()

	// Clean up.
	if err := a.client.Agent().ServiceDeregister(a.serviceID()); err != nil {
		a.logger.Printf("[WARN] Failed to deregister service: %v", err)
	}

	return nil
}

func (a *Agent) Shutdown() {
	if !a.shutdown {
		a.shutdown = true
		close(a.shutdownCh)
	}
}

func (a *Agent) serviceID() string {
	return fmt.Sprintf("%s:%s", a.config.Service, a.id)
}

// register is used to register this agent with Consul service discovery.
func (a *Agent) register() error {
	service := &api.AgentServiceRegistration{
		ID:   a.serviceID(),
		Name: a.config.Service,
	}
	if a.config.Tag != "" {
		service.Tags = []string{a.config.Tag}
	}
	if err := a.client.Agent().ServiceRegister(service); err != nil {
		return err
	}
	a.logger.Printf("[DEBUG] Registered ESM service with Consul")

	return nil
}

// runRegister is a long-running goroutine that ensures this agent is registered
// with Consul's service discovery. It will run until the shutdownCh is closed.
func (a *Agent) runRegister() {
	serviceID := a.serviceID()
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
				if err := a.register(); err != nil {
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
func (a *Agent) runTTL() {
	serviceID := a.serviceID()
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
			TTL:                            agentTTL.String(),
			Status:                         api.HealthPassing,
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

// watchNodeList is the top-level goroutine responsible for watching this agent's
// node list in the KV store and using those updates to inform the health check runner
// and node pinger.
func (a *Agent) watchNodeList() {
	// Set up the update channels for the various goroutines.
	healthNodeCh := make(chan map[string]bool, 1)
	coordNodeCh := make(chan []*api.Node, 1)

	// Start a goroutine to get health check updates from the catalog, filtering them using
	// the results from computeWatchedNodes.
	go a.watchHealthChecks(healthNodeCh)

	// Start a goroutine to run the pings used for coordinate and externalNodeHealth updates
	// on the nodes returned from computeWatchedNodes.
	go a.updateCoords(coordNodeCh)

	// Watch the node list at the KV path for our service ID. This will return the list
	// of external nodes that this agent is responsible for as decided by the leader.
	var opts *api.QueryOptions
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-a.shutdownCh
		cancelFunc()
	}()

	firstRun := true
	for {
		if !firstRun {
			select {
			case <-a.shutdownCh:
				return
			case <-time.After(retryTime):
			}
		}

		// Get the KV entry for this agent's node list.
		kv, meta, err := a.client.KV().Get(a.kvNodeListPath()+a.serviceID(), opts)
		if err != nil {
			a.logger.Printf("[WARN] Error querying for node watch list: %v", err)
			continue
		}

		// If the KV entry wasn't created yet, retry immediately (without setting
		// firstRun = false) so that we can get an update as soon as it's created.
		if kv == nil {
			opts.WaitIndex = meta.LastIndex
			continue
		}
		firstRun = false

		var nodeList NodeWatchList
		if err := json.Unmarshal(kv.Value, &nodeList); err != nil {
			a.logger.Printf("[WARN] Error deserializing node list: %v", err)
		}

		// Format the node lists for the health check/ping runners.
		healthNodes := make(map[string]bool)
		pingNodes := make(map[string]bool)
		for _, node := range nodeList.Nodes {
			healthNodes[node] = true
		}
		for _, node := range nodeList.Probes {
			healthNodes[node] = true
			pingNodes[node] = true
		}

		nodes, _, err := a.client.Catalog().Nodes(&api.QueryOptions{NodeMeta: a.config.NodeMeta})
		if err != nil {
			a.logger.Printf("[WARN] Error querying for node list: %v", err)
			continue
		}

		var pingList []*api.Node
		for _, node := range nodes {
			if pingNodes[node.Node] {
				pingList = append(pingList, node)
			}
		}

		healthNodeCh <- healthNodes
		coordNodeCh <- pingList

		opts.WaitIndex = meta.LastIndex
	}
}

// kvNodeListPath returns the path to the KV directory where the list of nodes
// for each agent to watch are written.
func (a *Agent) kvNodeListPath() string {
	return a.config.KVPath + "agents/"
}

// watchHealthChecks does a blocking query to the Consul api to get
// all health checks on nodes marked with the external node metadata
// identifier and sends any updates through the given updateCh.
func (a *Agent) watchHealthChecks(nodeListCh chan map[string]bool) {
	opts := &api.QueryOptions{
		NodeMeta: a.config.NodeMeta,
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-a.shutdownCh
		cancelFunc()
	}()

	// Start a check runner to track and run the health checks we're responsible for and call
	// UpdateChecks when we get an update from watchHealthChecks.
	a.checkRunner = NewCheckRunner(a.logger, a.client, a.config.CheckUpdateInterval)
	go a.checkRunner.reapServices(a.shutdownCh)
	defer a.checkRunner.Stop()

	ourNodes := <-nodeListCh
	checkCount := 0
	firstRun := true
	for {
		if !firstRun {
			select {
			case <-a.shutdownCh:
				return
			case ourNodes = <-nodeListCh:
				// Re-run if there's a change to the watched node list.
				opts.WaitIndex = 0
			case <-time.After(retryTime):
				// Sleep here to limit how much load we put on the Consul servers.
			}
		}
		firstRun = false

		checks, meta, err := a.client.Health().State(api.HealthAny, opts)
		if err != nil {
			a.logger.Printf("[WARN] Error querying for health check info: %v", err)
			continue
		}

		ourChecks := make(api.HealthChecks, 0)
		for _, c := range checks {
			if ourNodes[c.Node] && c.CheckID != externalCheckName {
				ourChecks = append(ourChecks, c)
			}
		}

		opts.WaitIndex = meta.LastIndex
		a.checkRunner.UpdateChecks(ourChecks)

		if checkCount != len(ourChecks) {
			checkCount = len(ourChecks)
			a.logger.Printf("[INFO] Now managing %d health checks across %d nodes", checkCount, len(ourNodes))
		}
	}
}
