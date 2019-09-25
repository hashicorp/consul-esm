package main

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/hashicorp/serf/coordinate"
	"github.com/mitchellh/mapstructure"
	"github.com/sparrc/go-ping"
)

const (
	NodeAliveStatus    = "Node alive or reachable"
	NodeCriticalStatus = "Node not live or unreachable"
)

var (
	// The maximum time to wait for a ping to complete.
	MaxRTT = 5 * time.Second
)

// updateCoords is a long running goroutine that attempts to ping all external nodes
// once per CoordinateUpdateInterval and update their statuses in Consul.
func (a *Agent) updateCoords(nodeCh <-chan []*api.Node) {
	// Wait for the first node ordering
	nodes := <-nodeCh
	shuffleNodes(nodes)

	// Start a ticker to help time the pings based on the watched node count.
	ticker := a.nodeTicker(len(nodes))
	defer ticker.Stop()

	index := 0
	for {
		// Shuffle the new slice of nodes and update the ticker if there's a node update.
		select {
		case newNodes := <-nodeCh:
			if len(newNodes) != len(nodes) {
				ticker.Stop()
				ticker = a.nodeTicker(len(newNodes))
				a.logger.Printf("[INFO] Now running probes for %d external nodes", len(newNodes))
			}
			nodes = newNodes
			shuffleNodes(nodes)
			index = 0
		default:
		}

		// Wait for the next tick before performing another ping. Using the ticker this way
		// ensures that we evenly space out the pings over the CoordinateUpdateInterval.
		select {
		case <-ticker.C:
			// Cycle through the nodes in shuffled order.
			index += 1
			if index >= len(nodes) {
				index = 0
			}
		case <-a.shutdownCh:
			return
		}

		if len(nodes) == 0 {
			a.logger.Printf("[DEBUG] No nodes to probe, will retry in %s", retryTime.String())
			time.Sleep(retryTime)
			continue
		}

		// Start a new ping for the node if there isn't one already in-flight.
		node := nodes[index]
		a.inflightLock.Lock()
		if _, ok := a.inflightPings[node.Node]; ok {
			a.logger.Printf("[WARN] Error pinging node %q (ID: %s): last request still outstanding", node.Node, node.ID)
		} else {
			a.inflightPings[node.Node] = struct{}{}
			go a.runNodePing(node)
		}
		a.inflightLock.Unlock()
	}
}

// runNodePing pings a node and updates its status in Consul accordingly.
func (a *Agent) runNodePing(node *api.Node) {
	// Get the critical status of the node.
	kvClient := a.client.KV()
	key := fmt.Sprintf("%sprobes/%s", a.config.KVPath, node.Node)
	kvPair, _, err := kvClient.Get(key, nil)
	if err != nil {
		a.logger.Printf("[ERR] could not get critical status for node %q: %v", node.Node, err)
	}

	// Run an ICMP ping to the node.
	rtt, err := pingNode(node.Address, a.config.PingType)

	// Update the node's health based on the results of the ping.
	if err == nil {
		if err := a.updateHealthyNode(node, kvClient, key, kvPair); err != nil {
			a.logger.Printf("[WARN] error updating node: %v", err)
		}
		if err := a.updateNodeCoordinate(node, rtt); err != nil {
			a.logger.Printf("[WARN] could not update coordinate for node %q: %v", node.Node, err)
		}
	} else {
		a.logger.Printf("[WARN] could not ping node %q: %v", node.Node, err)
		if err := a.updateFailedNode(node, kvClient, key, kvPair); err != nil {
			a.logger.Printf("[WARN] error updating node: %v", err)
		}
	}

	a.inflightLock.Lock()
	delete(a.inflightPings, node.Node)
	a.inflightLock.Unlock()
}

// shuffleNodes randomizes the ordering of a slice of nodes.
func shuffleNodes(nodes []*api.Node) {
	for i := len(nodes) - 1; i >= 0; i-- {
		j := rand.Intn(i + 1)
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}
}

// nodeTicker returns a time.Ticker to cycle through all nodes once
// every CoordinateUpdateInterval.
func (a *Agent) nodeTicker(numNodes int) *time.Ticker {
	waitTime := a.config.CoordinateUpdateInterval
	if numNodes > 0 {
		waitTime = a.config.CoordinateUpdateInterval / time.Duration(numNodes)
	}
	a.logger.Printf("[DEBUG] Now waiting %s between node pings", waitTime.String())
	return time.NewTicker(waitTime)
}

// updateHealthyNode updates the node's health check and clears any kv
// critical tracking associated with it.
func (a *Agent) updateHealthyNode(node *api.Node, kvClient *api.KV, key string, kvPair *api.KVPair) error {
	status := api.HealthPassing

	// If a critical node went back to passing, delete the KV entry for it.
	var ops api.TxnOps
	if kvPair != nil {
		ops = append(ops, &api.TxnOp{
			KV: &api.KVTxnOp{
				Verb:  api.KVDeleteCAS,
				Key:   key,
				Index: kvPair.ModifyIndex,
			},
		})
		a.logger.Printf("[TRACE] Deleting KV entry for key: %s", key)
	}

	// Batch the possible KV deletion operation with the external health check update.
	return a.updateNodeCheck(node, ops, status, NodeAliveStatus)
}

// updateFailedNode sets the node's health check to critical and checks whether
// the node has exceeded its timeout an needs to be reaped.
func (a *Agent) updateFailedNode(node *api.Node, kvClient *api.KV, key string, kvPair *api.KVPair) error {
	status := api.HealthCritical

	// If there's no existing key tracking how long the node has been critical, create one.
	var ops api.TxnOps
	if kvPair == nil {
		bytes, _ := time.Now().UTC().GobEncode()
		ops = append(ops, &api.TxnOp{
			KV: &api.KVTxnOp{
				Verb:  api.KVSet,
				Key:   key,
				Value: bytes,
			},
		})
		a.logger.Printf("[TRACE] Writing KV entry for key: %s", key)
	} else {
		var criticalStart time.Time
		err := criticalStart.GobDecode(kvPair.Value)
		if err != nil {
			return fmt.Errorf("could not decode critical time for node %q: %v", node.Node, err)
		}

		// Check if the node has been critical for too long and needs to be reaped.
		if time.Since(criticalStart) > a.config.NodeReconnectTimeout {
			a.logger.Printf("[INFO] reaping node %q that has been failed for more then %s",
				node.Node, a.config.NodeReconnectTimeout.String())

			// Clear the KV entry.
			ops = append(ops, &api.TxnOp{
				KV: &api.KVTxnOp{
					Verb:  api.KVDeleteCAS,
					Key:   key,
					Index: kvPair.ModifyIndex,
				},
			})

			// If the node still exists in the catalog, add an atomic delete on the node to
			// the list of operations to run.
			existing, _, err := a.client.Catalog().Node(node.Node, nil)
			if err != nil {
				return fmt.Errorf("could not fetch existing node %q: %v", node.Node, err)
			}
			if existing != nil && existing.Node != nil {
				ops = append(ops, &api.TxnOp{
					Node: &api.NodeTxnOp{
						Verb: api.NodeDeleteCAS,
						Node: api.Node{
							Node:        node.Node,
							ModifyIndex: existing.Node.ModifyIndex,
						},
					},
				})
				a.logger.Printf("[DEBUG] Deregistering node %q", node.Node)
			}

			// Run the transaction as-is to deregister the node and delete the KV entry.
			return a.runClientTxn(ops)
		}
	}

	// Batch our KV update tracking the critical time with the external health check update.
	return a.updateNodeCheck(node, ops, status, NodeCriticalStatus)
}

// updateNodeCheck updates the node's externalNodeHealth check with the given status/output.
func (a *Agent) updateNodeCheck(node *api.Node, ops api.TxnOps, status, output string) error {
	// Update the external health check status.
	ops = append(ops, &api.TxnOp{
		Check: &api.CheckTxnOp{
			Verb: api.CheckSet,
			Check: api.HealthCheck{
				Node:    node.Node,
				CheckID: externalCheckName,
				Name:    "External Node Status",
				Status:  status,
				Output:  output,
			},
		},
	})

	a.logger.Printf("[TRACE] Updating external health check for node %q", node.Node)

	return a.runClientTxn(ops)
}

// runClientTxn runs the given transaction using the configured Consul client and
// returns any errors encountered.
func (a *Agent) runClientTxn(ops api.TxnOps) error {
	ok, resp, _, err := a.client.Txn().Txn(ops, nil)
	if err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		var errs error
		for _, e := range resp.Errors {
			errs = multierror.Append(errs, errors.New(e.What))
		}
		return errs
	}
	if !ok {
		return fmt.Errorf("Failed to atomically write updates Consul")
	}

	return nil
}

// updateNodeCoordinate updates the node's coordinate entry based on the
// given RTT from a ping
func (a *Agent) updateNodeCoordinate(node *api.Node, rtt time.Duration) error {
	// Get coordinate info for the node.
	coords, _, err := a.client.Coordinate().Node(node.Node, nil)
	if err != nil && !strings.Contains(err.Error(), "Unexpected response code: 404") {
		return fmt.Errorf("error getting coordinate for node %q: %v, skipping update", node.Node, err)
	}

	// Take the first coordinate in the list if there are pre-existing
	// coordinates, we don't have to worry about picking the right one
	// because segments don't apply to external nodes.
	var coord *api.CoordinateEntry
	if len(coords) != 0 {
		coord = coords[0]
	} else {
		coord = &api.CoordinateEntry{
			Node:    node.Node,
			Segment: node.Meta[structs.MetaSegmentKey],
			Coord:   coordinate.NewCoordinate(coordinate.DefaultConfig()),
		}
	}

	// Get the local agent's coordinate info.
	self, err := a.client.Agent().Self()
	if err != nil {
		return fmt.Errorf("could not retrieve local agent's coordinate info: %v", err)
	}

	coordInfo, ok := self["Coord"]
	if !ok {
		return fmt.Errorf("could not decode local agent's coordinate info: %v", err)
	}

	var localCoord coordinate.Coordinate
	if err := mapstructure.Decode(coordInfo, &localCoord); err != nil {
		return fmt.Errorf("could not decode local agent's coordinate info: %v", err)
	}

	// Perform the coordinate update calculations.
	client, _ := coordinate.NewClient(coordinate.DefaultConfig())
	if err := client.SetCoordinate(coord.Coord); err != nil {
		return fmt.Errorf("invalid coordinate for node %q: %v", node.Node, err)
	}
	newCoord, err := client.Update("local", &localCoord, rtt)
	if err != nil {
		return fmt.Errorf("error updating coordinate for node %q: %v", node.Node, err)
	}

	// Update the coordinate in the catalog.
	_, err = a.client.Coordinate().Update(&api.CoordinateEntry{
		Node:    coord.Node,
		Segment: coord.Segment,
		Coord:   newCoord,
	}, nil)

	if err != nil {
		return fmt.Errorf("error applying coordinate update for node %q: %v", node.Node, err)
	}
	a.logger.Printf("[TRACE] Updated coordinates for node %q", node.Node)

	return nil
}

// pingNode runs an ICMP or UDP ping against an address.
// It will returns the round-trip time with ICMP but not with UDP.
// For `socket: permission denied` see the Contributing section in README.md.
func pingNode(addr string, method string) (time.Duration, error) {
	var rtt time.Duration
	var pingErr error

	p, err := ping.NewPinger(addr)
	if err != nil {
		return 0, err
	}

	switch method {
	case PingTypeUDP: // p's default
	case PingTypeSocket:
		p.SetPrivileged(true)
	default:
		return 0, fmt.Errorf("invalid ping type %q", method)
	}

	p.Timeout = MaxRTT
	p.OnRecv = func(pkt *ping.Packet) {
		rtt = pkt.Rtt
	}
	p.OnFinish = func(*ping.Statistics) {
		pingErr = fmt.Errorf("ping to %q timed out", addr)
	}
	p.Run()

	if rtt != 0 {
		return rtt, nil
	} else {
		return 0, pingErr
	}
}
