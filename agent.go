// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul-esm/version"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/go-hclog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const LeaderKey = "leader"

// Do not edit this, it is used by the other application in integration.
const agentTypeHttpHeader = "X-Consul-Agent-Type"

// Do not edit this, it is used by the other application in integration.
const agentTypeESM = "consul-esm"

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

	// Specifies a minimum interval that check's can run on
	minimumInterval = 1 * time.Second

	// Specifies the maximum transaction size for kv store ops
	maximumTransactionSize = 64
)

type lastKnownStatus struct {
	status string
	time   time.Time
}

func (s lastKnownStatus) isExpired(ttl time.Duration, now time.Time) bool {
	statusAge := now.Sub(s.time)
	return statusAge >= ttl
}

type Agent struct {
	config      *Config
	client      *api.Client
	logger      hclog.Logger
	checkRunner *CheckRunner
	id          string

	shutdownCh chan struct{}
	shutdown   bool
	ready      chan struct{}

	inflightPings map[string]struct{}
	inflightLock  sync.Mutex

	// Custom func to hook into for testing.
	watchedNodeFunc       func(map[string]bool, []*api.Node)
	knownNodeStatuses     map[string]lastKnownStatus
	knownNodeStatusesLock sync.Mutex

	metrics *lib.MetricsConfig
}

func NewAgent(config *Config, logger hclog.Logger) (*Agent, error) {
	clientConf := config.ClientConfig()
	client, err := api.NewClient(clientConf)
	if err != nil {
		return nil, err
	}

	// A unique marker to identify the agent type as ESM.
	client.AddHeader(agentTypeHttpHeader, agentTypeESM)

	// Never used locally. I think we keep the reference to avoid GC.
	metricsConf, err := lib.InitTelemetry(config.Telemetry, logger)
	if err != nil {
		return nil, err
	}

	agent := Agent{
		config:            config,
		client:            client,
		id:                config.InstanceID,
		logger:            logger,
		shutdownCh:        make(chan struct{}),
		ready:             make(chan struct{}, 1),
		inflightPings:     make(map[string]struct{}),
		knownNodeStatuses: make(map[string]lastKnownStatus),
		metrics:           metricsConf,
	}

	logger.Info("Connecting to Consul", "address", clientConf.Address)
	for {
		leader, err := client.Status().Leader()
		if err != nil {
			logger.Error("error getting leader status, will retry", "time", retryTime.String(), "error", err.Error())
		} else if leader == "" {
			logger.Info("waiting for cluster to elect a leader before starting, will retry", "time", retryTime.String())
		} else {
			break
		}

		time.Sleep(retryTime)
	}

	return &agent, nil
}

func (a *Agent) isAgentLess() bool {
	if a.config == nil {
		return false
	}
	return a.config.AgentLess
}

func (a *Agent) Run() error {

	if a.isAgentLess() {
		// register ESM as catalog service instead of agent
		if err := a.registerCatalog(); err != nil {
			return err
		}
	} else {
		// Do the initial service registration.
		if err := a.register(); err != nil {
			return err
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		a.runHTTP()
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		a.runRegister()
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		if a.isAgentLess() {
			a.runTTLAgentless()
		} else {
			a.runTTL()
		}
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

	a.ready <- struct{}{} // used for testing
	defer func() {        // be sure to drain it between calls
		select {
		case <-a.ready:
		default:
		}
	}()
	// Wait for shutdown.
	<-a.shutdownCh
	wg.Wait()

	// Clean up.
	if err := a.client.Agent().ServiceDeregisterOpts(a.serviceID(), a.ConsulQueryOption()); err != nil {
		a.logger.Warn("Failed to deregister service", "error", err)
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

func (a *Agent) serviceMeta() map[string]string {
	return map[string]string{
		"external-source": "consul-esm",
	}
}

type alreadyExistsError struct {
	serviceID string
}

func (e *alreadyExistsError) Error() string {
	return fmt.Sprintf("ESM instance with service id '%s' is already registered with Consul", e.serviceID)
}

func (a *Agent) createCatalogHealthCheck(status string) *api.CatalogRegistration {
	// create a catalog registration without service
	reg := a.createCatalogRegistration(false, true)
	reg.Checks[0].Status = status

	// this is super important to avoid the node update
	reg.SkipNodeUpdate = true
	return reg
}

func (a *Agent) createCatalogRegistration(addService bool, addChecks bool) *api.CatalogRegistration {
	// Define service details
	serviceID := a.serviceID()
	serviceName := a.config.Service
	nodeName := a.nodeID()
	address := fmt.Sprintf("esm-%s", serviceID)

	// Node does not exist, register it
	reg := &api.CatalogRegistration{
		Node:    nodeName,
		Address: address,
		NodeMeta: map[string]string{
			"external-source": "consul-esm",
			//"external-probe":   "true",
			//"external-node":    "true",
			"enforce-session":  "true",
			"session-check-id": a.ttlCheckID(),
			"session-key":      fmt.Sprintf("%s:session", serviceID),
		},
	}

	if addChecks {
		reg.Checks = api.HealthChecks{
			{
				Node:    nodeName,
				CheckID: a.ttlCheckID(),
				Name:    a.ttlCheckID(),
				Status:  api.HealthPassing,
				//ServiceID:   serviceID,
				//ServiceName: serviceName,
				Type: "ttl",
			},
		}
	}

	if addService {
		reg.Service = &api.AgentService{
			ID:      serviceID,
			Service: serviceName,
			//Port:    8080,
			//Address: "127.0.0.2",
		}
	}
	return reg
}

// register ESM as service in Catalog
func (a *Agent) registerCatalog() error {
	// agent ids need to be unique to disambiguate different instances on same host
	// check if service already exists in agent mode
	if existing, _, _ := a.client.Agent().Service(a.serviceID(), a.ConsulQueryOption()); existing != nil {
		return &alreadyExistsError{a.serviceID()}
	}

	// check if the instance is not already registered as service in catalog
	if services, _, _ := a.client.Catalog().Service(a.serviceID(), "", a.ConsulQueryOption()); len(services) > 0 {
		return &alreadyExistsError{a.serviceID()}
	}

	nodeName := a.nodeID()
	// Check if the node exists
	node, _, err := a.client.Catalog().Node(nodeName, nil)
	if err != nil {
		return err
	}

	if node == nil {
		//// Node does not exist, register it
		//reg := &api.CatalogRegistration{
		//	Node:    nodeName,
		//	Address: fmt.Sprintf("esm-%s", a.serviceID()),
		//	NodeMeta: map[string]string{
		//		"external-source": "consul-esm",
		//		"external-probe":  "true",
		//		"external-node":   "true",
		//	},
		//	Checks: api.HealthChecks{
		//		{
		//			Node:        nodeName,
		//			CheckID:     a.ttlCheckID(),
		//			Name:        a.ttlCheckID(),
		//			Status:      api.HealthPassing,
		//			ServiceID:   a.serviceID(),
		//			ServiceName: a.config.Service,
		//			Type:        "ttl",
		//			Definition: api.HealthCheckDefinition{
		//				IntervalDuration:               time.Duration(30 * time.Second),
		//				TimeoutDuration:                time.Duration(2 * time.Second),
		//				DeregisterCriticalServiceAfter: api.ReadableDuration(10 * time.Minute),
		//			},
		//		},
		//	},
		//}

		_, err := a.client.Catalog().Register(a.createCatalogRegistration(false, false), &api.WriteOptions{
			Datacenter: a.config.Datacenter,
			Partition:  a.PartitionOrEmpty(),
		})
		if err != nil {
			return err
		}

		fmt.Println("Node registered successfully")
	} else {
		fmt.Println("Node already exists")
	}

	// Register the ESM service
	// Check if the service already exists
	service, _, err := a.client.Catalog().Service(a.serviceID(), "", a.ConsulQueryOption())
	if err != nil {
		return err
	}

	if len(service) <= 0 {
		// Service does not exist, register it

		// Define service details
		//serviceID := a.serviceID()
		//serviceName := a.config.Service

		// Register the service in the Consul catalog
		//reg := &api.CatalogRegistration{
		//	Node:    nodeName, // The node where this service is registered
		//	Address: "127.0.0.2",
		//	Service: &api.AgentService{
		//		ID:      serviceID,
		//		Service: serviceName,
		//		Port:    8080,
		//		Address: "127.0.0.2",
		//	},
		//	NodeMeta: map[string]string{
		//		"enforce-session":  "true",
		//		"session-check-id": a.ttlCheckID(),
		//		"session-key":      fmt.Sprintf("%s:session", a.serviceID()),
		//	},
		//	Checks: api.HealthChecks{
		//		{
		//			Node:        nodeName,
		//			CheckID:     a.ttlCheckID(),
		//			Name:        a.ttlCheckID(),
		//			Status:      api.HealthPassing,
		//			ServiceID:   serviceID,
		//			ServiceName: a.config.Service,
		//			Type:        "ttl",
		//			Definition: api.HealthCheckDefinition{
		//				IntervalDuration:               time.Duration(30 * time.Second),
		//				TimeoutDuration:                time.Duration(2 * time.Second),
		//				DeregisterCriticalServiceAfter: api.ReadableDuration(10 * time.Minute),
		//			},
		//		},
		//	},
		//}

		_, err = a.client.Catalog().Register(a.createCatalogRegistration(true, true), &api.WriteOptions{
			Datacenter: a.config.Datacenter,
			Partition:  a.PartitionOrEmpty(),
		})

		if err != nil {
			return err
		}

		//// 2️⃣ Register TTL-based health check using Agent API
		//check := &api.AgentCheckRegistration{
		//	ID:        "ttl-check-" + serviceID,
		//	Name:      "TTL Health Check",
		//	ServiceID: serviceID, // Linking the check to our service
		//	AgentServiceCheck: api.AgentServiceCheck{
		//		Status:                         api.HealthPassing, // Initially healthy
		//		Interval:                       "5s",              // How often to run the check
		//		Timeout:                        "1s",              // How long before the check times out
		//		DeregisterCriticalServiceAfter: "1m",              // Consul will remove the service if this check is critical for more than 1m
		//		TTL:                            "10s",             // Consul expects TTL updates within 10s
		//	},
		//}
		//
		//err = client.Agent().CheckRegister(check)
		//if err != nil {
		//	log.Fatalf("Failed to register TTL health check: %v", err)
		//}
		//log.Println("✅ TTL health check registered in Consul")

	}

	return nil
}

// register is used to register this agent with Consul service discovery.
func (a *Agent) register() error {
	// agent ids need to be unique to disambiguate different instances on same host
	if existing, _, _ := a.client.Agent().Service(a.serviceID(), a.ConsulQueryOption()); existing != nil {
		return &alreadyExistsError{a.serviceID()}
	}

	service := &api.AgentServiceRegistration{
		ID:   a.serviceID(),
		Name: a.config.Service,
		Meta: a.serviceMeta(),
	}
	a.HasPartition(func(partition string) {
		service.Partition = partition

		// we already have `agent-ttl` check for agent health, we just need this check to enable the registration of the agent
		// Note: This check will be removed by regular ttl check
		service.Check = &api.AgentServiceCheck{
			CheckID:                        "SerfHealth",
			Name:                           "SerfHealth",
			Notes:                          "",
			TTL:                            "31556926s",
			Status:                         api.HealthPassing,
			DeregisterCriticalServiceAfter: deregisterTime.String(),
		}

		// Marker to identify ESM Service and Check
		service.Tags = []string{agentTypeESM}
	})

	if a.config.Tag != "" {
		service.Tags = []string{a.config.Tag}
	}
	if err := a.client.Agent().ServiceRegister(service); err != nil {
		return err
	}
	a.logger.Debug("Registered ESM service with Consul")

	return nil
}

func (a *Agent) ttlCheckID() string {
	return fmt.Sprintf("%s:agent-ttl", a.serviceID())
}

func (a *Agent) nodeID() string {
	return fmt.Sprintf("%s:node", a.serviceID())
}

// runHTTP is a long-running goroutine that exposes an http interface for
// metrics and/or pprof.
func (a *Agent) runHTTP() {
	if a.config.ClientAddress == "" {
		return
	}

	enableMetrics := a.config.Telemetry.PrometheusOpts.Expiration >= 1
	if !enableMetrics && !a.config.EnableDebug {
		return
	}

	mux := http.NewServeMux()
	srv := &http.Server{Addr: a.config.ClientAddress, Handler: mux}

	if enableMetrics {
		handlerOptions := promhttp.HandlerOpts{
			ErrorLog: a.logger.StandardLogger(&hclog.StandardLoggerOptions{
				InferLevels: true,
			}),
			ErrorHandling: promhttp.ContinueOnError,
		}
		mux.Handle("/metrics", promhttp.HandlerFor(prometheus.DefaultGatherer, handlerOptions))
	}

	if a.config.EnableDebug {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	go func() {
		<-a.shutdownCh
		deadline := time.Now().Add(5 * time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			a.logger.Error("Failed to shutdown http interface", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		a.logger.Error("Failed to open http interface", "error", err)
	}
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
			services, err := a.client.Agent().ServicesWithFilterOpts("", a.ConsulQueryOption())
			if err != nil {
				a.logger.Error("Failed to check services (will retry)", "error", err)
				time.Sleep(retryTime)
				goto REGISTER_CHECK
			}

			if _, ok := services[serviceID]; !ok {
				a.logger.Warn("Service registration was lost, reregistering")
				if err := a.register(); err != nil {
					a.logger.Error("Failed to reregister service (will retry)", "error", err)
					time.Sleep(retryTime)
					goto REGISTER_CHECK
				}
			}
		}
	}
}

func (a *Agent) runTTLAgentless() {
	// registering a session to monitor the health of the agent
	sessionKey := fmt.Sprintf("%s:session", a.serviceID())
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
	case <-a.shutdownCh:
		return
	default:
	}

	// Wait to get the leader lock before running snapshots.
	a.logger.Info("Agent: Trying to obtain health check session...")
	if lock == nil {
		var err error
		opts := &api.LockOptions{
			Key:         a.config.KVPath + sessionKey,
			SessionName: sessionKey,
		}
		lock, err = a.client.LockOpts(opts)
		opts.SessionOpts = &api.SessionEntry{
			Node:       a.nodeID(),
			Name:       opts.SessionName,
			TTL:        opts.SessionTTL,
			LockDelay:  opts.LockDelay,
			NodeChecks: []string{a.ttlCheckID()},
			Checks:     []string{a.ttlCheckID()},
		}

		if err != nil {
			a.logger.Error("Agent: Error trying to create leader lock (will retry)", "error", err)
			time.Sleep(retryTime)
			goto LEADER_WAIT
		}
	}

	leaderCh, err := lock.Lock(a.shutdownCh)
	if err != nil {
		if err == api.ErrLockHeld {
			a.logger.Error("Agent: Unable to use leader lock that was held previously and presumed lost, giving up the lock (will retry)", "error", err)
			lock.Unlock()
			time.Sleep(retryTime)
			goto LEADER_WAIT
		} else {
			a.logger.Error("Agent: Error trying to get leader lock (will retry)", "error", err)
			time.Sleep(retryTime)

			//// making the service healthy briefly to register the service
			//reg := &api.CatalogRegistration{
			//	SkipNodeUpdate: true,
			//	Node:           a.nodeID(), // The node where this service is registered
			//	Address:        "127.0.0.2",
			//	NodeMeta: map[string]string{
			//		"enforce-session":  "true",
			//		"session-check-id": a.ttlCheckID(),
			//		"session-key":      fmt.Sprintf("%s:session", a.serviceID()),
			//	},
			//	Checks: api.HealthChecks{
			//		{
			//			Node:        a.nodeID(),
			//			CheckID:     a.ttlCheckID(),
			//			Name:        a.ttlCheckID(),
			//			Status:      api.HealthPassing,
			//			ServiceID:   a.serviceID(),
			//			ServiceName: a.config.Service,
			//			Type:        "ttl",
			//			Definition: api.HealthCheckDefinition{
			//				IntervalDuration:               time.Duration(30 * time.Second),
			//				TimeoutDuration:                time.Duration(2 * time.Second),
			//				DeregisterCriticalServiceAfter: api.ReadableDuration(10 * time.Minute),
			//			},
			//		},
			//	},
			//}

			_, err = a.client.Catalog().Register(a.createCatalogHealthCheck(api.HealthPassing), &api.WriteOptions{
				Datacenter: a.config.Datacenter,
				Partition:  a.PartitionOrEmpty(),
			})

			if err != nil {
				a.logger.Error("Agent: nested error trying to get leader lock (will retry)", "error", err)
			}

			goto LEADER_WAIT
		}
	}
	if leaderCh == nil {
		// This is how the Lock() call lets us know that it quit because
		// we closed the shutdown channel.
		return
	}
	a.logger.Info("Agent: Obtained leadership")

	for {
		select {
		case <-leaderCh:
			a.logger.Warn("Agent: Lost leadership")
			goto LEADER_WAIT
		case <-a.shutdownCh:
			return
		}
	}
}

// runTTL is a long-running goroutine that registers an "agent alive" TTL health
// check and services it periodically. It will run until the shutdownCh is closed.
func (a *Agent) runTTL() {
	serviceID := a.serviceID()
	ttlID := a.ttlCheckID()

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
	a.HasPartition(func(partition string) {
		check.Partition = partition
	})
	if err := a.client.Agent().CheckRegisterOpts(check, a.ConsulQueryOption()); err != nil {
		a.logger.Error("Failed to register TTL check (will retry)", "error", err)
		time.Sleep(retryTime)
		goto REGISTER
	}

	// Wait for the shutdown signal and poke the TTL check.
	for {
		select {
		case <-a.shutdownCh:
			return

		case <-time.After(agentTTL / 2):
			if err := a.client.Agent().UpdateTTLOpts(ttlID, "", api.HealthPassing, a.ConsulQueryOption()); err != nil {
				a.logger.Error("Failed to refresh agent TTL check (will reregister)", "error", err)
				time.Sleep(retryTime)
				goto REGISTER
			}

			//if err := a.client.Agent().UpdateTTLOpts("SerfHealth", "", api.HealthPassing, a.ConsulQueryOption()); err != nil {
			//	a.logger.Error("Failed to refresh agent TTL check (will reregister)", "error", err)
			//}
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
			a.logger.Warn("Error querying for node watch list", "error", err)
			time.Sleep(retryTime)
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
			a.logger.Warn("Error deserializing node list", "error", err)
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
			a.logger.Warn("Error querying for node list", "error", err)
			continue
		}

		a.logger.Info("Fetched nodes from catalog", "count", len(nodes))

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
	// Initialize a tlsConfig struct
	tlsConfig := api.TLSConfig{
		CAFile:   a.config.HTTPSCAFile,
		CAPath:   a.config.HTTPSCAPath,
		CertFile: a.config.HTTPSCertFile,
		KeyFile:  a.config.HTTPSKeyFile,
	}

	tlsClientConfig, err := api.SetupTLSConfig(&tlsConfig)
	if err != nil {
		a.logger.Error("Could not create TLS config", "error", err)
		return
	}

	// Start a check runner to track and run the health checks we're responsible for and call
	// UpdateChecks when we get an update from watchHealthChecks.
	a.checkRunner = NewCheckRunner(a.logger, a.client,
		a.config.CheckUpdateInterval, minimumInterval,
		tlsClientConfig, a.config.PassingThreshold, a.config.CriticalThreshold)
	go a.checkRunner.reapServices(a.shutdownCh)
	defer a.checkRunner.Stop()

	var ourNodes map[string]bool
	var waitIndex uint64
	checkCount := 0
	for {
		select {
		case <-a.shutdownCh:
			return
		case ourNodes = <-nodeListCh:
			// Re-run if there's a change to the watched node list.
			waitIndex = 0
		case <-time.After(retryTime):
			// Sleep here to limit how much load we put on the Consul servers.
		}
		if len(ourNodes) == 0 {
			continue
		}

		ourChecks, lastIndex := a.getHealthChecks(waitIndex, ourNodes)
		if len(ourChecks) == 0 {
			continue
		}

		waitIndex = lastIndex
		a.checkRunner.UpdateChecks(ourChecks)

		if checkCount != len(ourChecks) {
			checkCount = len(ourChecks)
			a.logger.Info("Health check counts changed",
				"healthChecks", checkCount, "nodes", len(ourNodes))
		}
	}
}

func (a *Agent) getHealthChecks(waitIndex uint64, nodes map[string]bool) (api.HealthChecks, uint64) {
	namespaces, err := namespacesList(a.client, a.config)
	if err != nil {
		a.logger.Warn("Error getting namespaces, falling back to default namespace", "error", err)
		namespaces = []*api.Namespace{{Name: ""}}
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	opts := &api.QueryOptions{
		NodeMeta:  a.config.NodeMeta,
		WaitIndex: waitIndex,
	}
	opts = opts.WithContext(ctx)
	a.HasPartition(func(partition string) {
		opts.Partition = partition
	})
	defer cancelFunc()
	go func() {
		select {
		case <-a.shutdownCh:
			cancelFunc()
		case <-ctx.Done():
		}
	}()

	ourChecks := make(api.HealthChecks, 0)
	var lastIndex uint64
	for _, ns := range namespaces {
		opts.Namespace = ns.Name
		if ns.Name != "" { // ns.Name only set on enterprise version
			a.logger.Info("checking namespaces for services", "name", ns.Name)
		}
		checks, meta, err := a.client.Health().State(api.HealthAny, opts)
		if err != nil {
			a.logger.Warn("Error querying for health check info", "error", err)
			continue
		}
		lastIndex = meta.LastIndex

		for _, c := range checks {
			if nodes[c.Node] && c.CheckID != externalCheckName {
				ourChecks = append(ourChecks, c)
				a.logger.Info("found check", "name", c.Name)
			}
		}
	}

	return ourChecks, lastIndex
}

// Check last visible node status.
// Returns true, if status is changed or expired since last update and false otherwise.
func (a *Agent) shouldUpdateNodeStatus(node string, newStatus string) bool {
	a.knownNodeStatusesLock.Lock()
	defer a.knownNodeStatusesLock.Unlock()
	ttl := a.config.NodeHealthRefreshInterval
	lastStatus, exists := a.knownNodeStatuses[node]
	if !exists || lastStatus.isExpired(ttl, time.Now()) {
		return true
	}
	return newStatus != lastStatus.status
}

// Update last visible node status.
func (a *Agent) updateLastKnownNodeStatus(node string, newStatus string) {
	a.knownNodeStatusesLock.Lock()
	defer a.knownNodeStatusesLock.Unlock()
	a.knownNodeStatuses[node] = lastKnownStatus{newStatus, time.Now()}
}

// VerifyConsulCompatibility queries Consul for local agent and all server versions to verify
// compatibility with ESM.
func (a *Agent) VerifyConsulCompatibility() error {
	if a.client == nil {
		return fmt.Errorf("unable to check version compatibility without Consul client initialized")
	}

	// Fetch local agent version
	agentInfo, err := a.client.Agent().Self()
	if err != nil {
		// ESM blocks in NewAgent() until agent is available. At this point
		// /agent/self endpoint should be available and an error would not be useful
		// to retry the request.
		return fmt.Errorf("unable to check version compatibility with Consul agent: %s", err)
	}
	agentVersionRaw, ok := agentInfo["Config"]["Version"]
	if !ok {
		return fmt.Errorf("unable to check version compatibility with Consul agent")
	}
	agentVersion := agentVersionRaw.(string)

VERIFYCONSULSERVER:
	select {
	case <-a.shutdownCh:
		return nil
	default:
	}

	// Fetch server versions
	// consul is always registered in default partition. There is possibility the default token may not have access to the partition
	svs, _, err := a.client.Catalog().Service("consul", "", &api.QueryOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			// 429 is a warning that something is unhealthy. This may occur when ESM
			// races with Consul servers first starting up, so this is safe to retry.
			a.logger.Error("Failed to query for Consul server versions (will retry)", "error", err)
			time.Sleep(retryTime)
			goto VERIFYCONSULSERVER
		}

		return fmt.Errorf("unable to check version compatibility with Consul servers: %s", err)
	}

	versions := []string{agentVersion}
	uniqueVersions := map[string]bool{agentVersion: true}
	var foundServer bool
	for _, s := range svs {
		if v, ok := s.ServiceMeta["version"]; ok {
			foundServer = true
			if !uniqueVersions[v] {
				uniqueVersions[v] = true
				versions = append(versions, v)
			}
		}
	}

	if !foundServer {
		a.logger.Warn("unable to determine Consul server version, check for " +
			"compatibility; requires " + version.GetConsulVersionConstraint())
	}

	err = version.CheckConsulVersions(versions)
	if err != nil {
		a.logger.Error("Incompatible Consul versions")
		return err
	}

	a.logger.Debug("Consul agent and all servers are running compatible versions with ESM")
	return nil
}

// PartitionOrEmpty returns the partition if it exists, otherwise returns an empty string.
func (a *Agent) PartitionOrEmpty() string {
	if a.config == nil || a.config.Partition == "" {
		return ""
	}
	return a.config.Partition
}

// HasPartition checks if the partition is valid and calls the callback with the partition if it has any.
func (a *Agent) HasPartition(callback func(partition string)) {
	partition := a.PartitionOrEmpty()

	if partition == "" || strings.ToLower(partition) == "default" {
		// Ignore empty or default partitions
		return
	}

	callback(a.config.Partition)
}

// ConsulQueryOption constructs and returns a new api.QueryOptions object.
// If the Agent has a valid partition, it sets the partition in the QueryOptions.
//
// Returns:
//   *api.QueryOptions: A new QueryOptions object with the partition set if applicable.

func (a *Agent) ConsulQueryOption() *api.QueryOptions {
	opts := &api.QueryOptions{}
	a.HasPartition(func(partition string) {
		opts.Partition = partition
	})
	return opts
}
