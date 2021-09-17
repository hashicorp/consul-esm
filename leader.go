package main

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
)

type NodeWatchList struct {
	Nodes  []string
	Probes []string
}

func (a *Agent) runLeaderLoop() {
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
	a.logger.Info("Trying to obtain leadership...")
	if lock == nil {
		var err error
		lock, err = a.client.LockKey(a.config.KVPath + LeaderKey)
		if err != nil {
			a.logger.Error("Error trying to create leader lock (will retry)", "error", err)
			time.Sleep(retryTime)
			goto LEADER_WAIT
		}
	}

	leaderCh, err := lock.Lock(a.shutdownCh)
	if err != nil {
		if err == api.ErrLockHeld {
			a.logger.Error("Unable to use leader lock that was held previously and presumed lost, giving up the lock (will retry)", "error", err)
			lock.Unlock()
			time.Sleep(retryTime)
			goto LEADER_WAIT
		} else {
			a.logger.Error("Error trying to get leader lock (will retry)", "error", err)
			time.Sleep(retryTime)
			goto LEADER_WAIT
		}
	}
	if leaderCh == nil {
		// This is how the Lock() call lets us know that it quit because
		// we closed the shutdown channel.
		return
	}
	a.logger.Info("Obtained leadership")

	// Start a goroutine for computing the node watches.
	go a.computeWatchedNodes(leaderCh)

	for {
		select {
		case <-leaderCh:
			a.logger.Warn("Lost leadership")
			goto LEADER_WAIT
		case <-a.shutdownCh:
			return
		}
	}
}

// nodesLists builds lists of nodes each agent is responsible for.
func nodeLists(nodes []*api.Node, insts []*api.ServiceEntry,
) (map[string][]string, map[string][]string) {
	healthNodes := make(map[string][]string)
	pingNodes := make(map[string][]string)
	if len(insts) == 0 {
		return healthNodes, pingNodes
	}
	for i, node := range nodes {
		idx := i % len(insts)
		agentID := insts[idx].Service.ID

		// If it's a node to probe, add it to the ping list. Otherwise just add
		// it to the list of nodes to be health checked.
		if node.Meta != nil {
			if v, ok := node.Meta["external-probe"]; ok && v == "true" {
				pingNodes[agentID] = append(pingNodes[agentID], node.Node)
				continue
			}
		}
		healthNodes[agentID] = append(healthNodes[agentID], node.Node)
	}
	return healthNodes, pingNodes
}

func (a *Agent) commitOps(ops api.KVTxnOps) bool {
	success, results, _, err := a.client.KV().Txn(ops, nil)
	if err != nil || !success {
		a.logger.Error("Error writing state to KV store", "results", results, "error", err)
		// Try again after the wait because we got an error.
		return false
	}
	return true
}

// computeWatchedNodes watches both the list of registered ESM instances and
// the list of external nodes registered in Consul and decides which nodes each
// ESM instance should be in charge of, writing the output to the KV store.
func (a *Agent) computeWatchedNodes(stopCh <-chan struct{}) {
	nodeCh := make(chan []*api.Node)
	instanceCh := make(chan []*api.ServiceEntry)

	go a.watchExternalNodes(nodeCh, stopCh)
	go a.watchServiceInstances(instanceCh, stopCh)

	externalNodes := <-nodeCh
	healthyInstances := <-instanceCh

	var prevHealthNodes map[string][]string
	var prevPingNodes map[string][]string

	// Avoid blocking on first pass
	retryTimer := time.After(0)

WATCH_NODES_WAIT:
	for {
		select {
		case <-stopCh:
			return
		case externalNodes = <-nodeCh:
		case healthyInstances = <-instanceCh:
		case <-retryTimer:
		}

		// Next time through block until either nodes or instances are updated
		retryTimer = nil

		// Wait for some instances to become available, if there are none, then there isn't anything we can do
		if len(healthyInstances) == 0 {
			retryTimer = time.After(retryTime)
			continue
		}

		healthNodes, pingNodes := nodeLists(externalNodes, healthyInstances)

		// Write the KV update as a transaction.
		ops := api.KVTxnOps{
			&api.KVTxnOp{
				Verb: api.KVDeleteTree,
				Key:  a.kvNodeListPath(),
			},
		}
		for _, agent := range healthyInstances {
			bytes, _ := json.Marshal(NodeWatchList{
				Nodes:  healthNodes[agent.Service.ID],
				Probes: pingNodes[agent.Service.ID],
			})
			op := &api.KVTxnOp{
				Verb:  api.KVSet,
				Key:   a.kvNodeListPath() + agent.Service.ID,
				Value: bytes,
			}
			ops = append(ops, op)

			// Flush any ops if we're nearing a transaction limit
			if len(ops) >= maximumTransactionSize {
				if !a.commitOps(ops) {
					retryTimer = time.After(retryTime)
					goto WATCH_NODES_WAIT
				}
				ops = api.KVTxnOps{}
			}
		}

		// Final flush for ops
		if !a.commitOps(ops) {
			retryTimer = time.After(retryTime)
			continue
		}

		// Log a message when the balancing changes.
		if !reflect.DeepEqual(healthNodes, prevHealthNodes) || !reflect.DeepEqual(pingNodes, prevPingNodes) {
			a.logger.Info("Rebalanced external nodes across ESM instances", "nodes", len(externalNodes), "instances", len(healthyInstances))
			prevHealthNodes = healthNodes
			prevPingNodes = pingNodes
		}
	}
}

// watchExternalNodes does a watch for external nodes and returns any updates
// back through nodeCh as a sorted list.
func (a *Agent) watchExternalNodes(nodeCh chan []*api.Node, stopCh <-chan struct{}) {
	opts := &api.QueryOptions{
		NodeMeta: a.config.NodeMeta,
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-stopCh
		cancelFunc()
	}()

	firstRun := true
	for {
		if !firstRun {
			select {
			case <-stopCh:
				return
			case <-time.After(retryTime):
				// Sleep here to limit how much load we put on the Consul servers.
			}
		}
		firstRun = false

		// Do a blocking query for any external node changes
		externalNodes, meta, err := a.client.Catalog().Nodes(opts)
		if err != nil {
			a.logger.Warn("Error getting external node list", "error", err)
			continue
		}
		sort.Slice(externalNodes, func(a, b int) bool {
			return externalNodes[a].Node < externalNodes[b].Node
		})

		opts.WaitIndex = meta.LastIndex

		a.logger.Info("Updating external node list", "items", len(externalNodes))

		nodeCh <- externalNodes
	}
}

// watchServiceInstances does a watch for any ESM instances with the same service tag as
// this agent and sends any updates back through instanceCh as a sorted list.
func (a *Agent) watchServiceInstances(instanceCh chan []*api.ServiceEntry, stopCh <-chan struct{}) {
	var opts *api.QueryOptions
	ctx, cancelFunc := context.WithCancel(context.Background())
	opts = opts.WithContext(ctx)
	go func() {
		<-stopCh
		cancelFunc()
	}()

	for {
		select {
		case <-stopCh:
			return
		case <-time.After(retryTime / 10):
			// Sleep here to limit how much load we put on the Consul servers.
			// We can wait a lot less than the normal retry time here because
			// the ESM service instance list is relatively small and cheap to
			// query.
		}

		switch healthyInstances, err := a.getServiceInstances(opts); err {
		case nil:
			instanceCh <- healthyInstances
		default:
			a.logger.Warn("[WARN] Error querying for health check info",
				"error", err)
			continue // not needed, but nice to be explicit
		}
	}
}

// getServiceInstances retuns a list of services with a 'passing' (healthy) state.
// It loops over all available namespaces to get instances from each.
func (a *Agent) getServiceInstances(opts *api.QueryOptions) ([]*api.ServiceEntry, error) {
	var healthyInstances []*api.ServiceEntry
	var meta *api.QueryMeta

	namespaces, err := namespacesList(a.client)
	if err != nil {
		return nil, err
	}

	for _, ns := range namespaces {
		if ns.Name != "" {
			a.logger.Info("checking namespaces for services", "name", ns.Name)
		}
		opts.Namespace = ns.Name
		healthy, m, err := a.client.Health().Service(a.config.Service,
			a.config.Tag, true, opts)
		if err != nil {
			return nil, err
		}
		meta = m // keep last good meta
		for _, h := range healthy {
			healthyInstances = append(healthyInstances, h)
		}
	}
	opts.WaitIndex = meta.LastIndex

	sort.Slice(healthyInstances, func(a, b int) bool {
		return healthyInstances[a].Service.ID < healthyInstances[b].Service.ID
	})

	return healthyInstances, nil
}

// namespacesList returns a list of all accessable namespaces.
// Returns namespace "" (none) if none found for consul OSS compatibility.
func namespacesList(client *api.Client) ([]*api.Namespace, error) {
	ossErr := "Unexpected response code: 404" // error snippet OSS consul returns
	namespaces, _, err := client.Namespaces().List(nil)
	switch {
	case err == nil:
	case strings.Contains(err.Error(), ossErr):
		namespaces = []*api.Namespace{{Name: ""}}
	case err != nil: // default, but more explicit
		return nil, err
	}
	return namespaces, nil
}
