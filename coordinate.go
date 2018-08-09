package main

import (
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/serf/coordinate"
	"github.com/mitchellh/mapstructure"
	"github.com/tatsushid/go-fastping"
)

const (
	NodeAliveStatus    = "Node alive or reachable"
	NodeCriticalStatus = "Node not live or unreachable"
)

var (
	// The maximum time to wait for a ping to complete.
	MaxRTT = 5 * time.Second
)

// updateCoords is a long running goroutine that pings an external node
// once per interval and updates its coordinates and virtual health check
// in the catalog.
func (a *Agent) updateCoords(nodeCh <-chan []*api.Node) {
	// Wait for the first node ordering
	nodes := <-nodeCh
	shuffleNodes(nodes)

	index := 0
	for {
		// Cycle through all nodes every CoordinateUpdateInterval.
		waitTime := a.config.CoordinateUpdateInterval
		if len(nodes) > 0 {
			waitTime = a.config.CoordinateUpdateInterval / time.Duration(len(nodes))
		}

		select {
		// Shuffle the new slice of nodes when we get an update.
		case nodes = <-nodeCh:
			shuffleNodes(nodes)
			index = 0
		case <-a.shutdownCh:
			return
		// Cycle through the nodes in shuffled order.
		case <-time.After(waitTime):
			index += 1
			if index >= len(nodes) {
				index = 0
			}
		}

		if len(nodes) == 0 {
			a.logger.Printf("[DEBUG] No nodes to probe, will retry in %s", retryTime.String())
			time.Sleep(retryTime)
			continue
		}

		node := nodes[index]

		// Get the critical status of the node.
		kvClient := a.client.KV()
		key := fmt.Sprintf("%s/%s", a.config.KVPath, node.Node)
		kvPair, _, err := kvClient.Get(key, nil)
		if err != nil {
			a.logger.Printf("[ERR] could not get critical status for node %q: %v", node.Node, err)
		}
    a.logger.Printf("[TRACE] Getting KV entry for key: %s", key)

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
	}
}

// shuffleNodes randomizes the ordering of a slice of nodes.
func shuffleNodes(nodes []*api.Node) {
	for i := len(nodes) - 1; i >= 0; i-- {
		j := rand.Intn(i + 1)
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}
}

// updateHealthyNode updates the node's health check and clears any kv
// critical tracking associated with it.
func (a *Agent) updateHealthyNode(node *api.Node, kvClient *api.KV, key string, kvPair *api.KVPair) error {
	status := api.HealthPassing

	// If a critical node went back to passing, delete the KV entry for it.
	if kvPair != nil {
		if _, err := kvClient.Delete(key, nil); err != nil {
			return fmt.Errorf("could not delete critical timer key %q: %v", key, err)
		}
		a.logger.Printf("[TRACE] Deleting KV entry for key: %s", key)
	}

	return a.updateNodeCheck(node, status, NodeAliveStatus)
}

// updateFailedNode sets the node's health check to critical and checks whether
// the node has exceeded its timeout an needs to be reaped.
func (a *Agent) updateFailedNode(node *api.Node, kvClient *api.KV, key string, kvPair *api.KVPair) error {
	status := api.HealthCritical

	// If there's no existing key tracking how long the node has been critical, create one.
	if kvPair == nil {
		bytes, _ := time.Now().UTC().GobEncode()
		kvPair = &api.KVPair{
			Key:   key,
			Value: bytes,
		}
		if _, err := kvClient.Put(kvPair, nil); err != nil {
			return fmt.Errorf("could not update critical time for node %q: %v", node.Node, err)
		}
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
			_, err := a.client.Catalog().Deregister(&api.CatalogDeregistration{
				Node:       node.Node,
				Datacenter: node.Datacenter,
			}, nil)
			if err != nil {
				return fmt.Errorf("could not reap node %q: %v", node.Node, err)
			}
			a.logger.Printf("[DEBUG] Deregistered node %q", node.Node)

			if _, err := kvClient.Delete(key, nil); err != nil {
				return fmt.Errorf("could not delete critical timer key %q for reaped node: %v", key, err)
			}

			// Return early to avoid re-registering the check
			return nil
		}
	}

	return a.updateNodeCheck(node, status, NodeCriticalStatus)
}

// updateNodeCheck updates the node's externalNodeHealth check with the given status/output.
func (a *Agent) updateNodeCheck(node *api.Node, status, output string) error {
	// Exit early if the node's been deregistered since we started the probe.
	existing, _, err := a.client.Catalog().Node(node.Node, nil)
	if err != nil {
		return fmt.Errorf("error retrieving existing node entry: %v", err)
	}
	if existing == nil {
		return nil
	}

	_, err = a.client.Catalog().Register(&api.CatalogRegistration{
		Node: node.Node,
		Check: &api.AgentCheck{
			CheckID: externalCheckName,
			Name:    "External Node Status",
			Status:  status,
			Output:  output,
		},
		SkipNodeUpdate: true,
	}, nil)
	if err != nil {
		return fmt.Errorf("could not update external node check for node %q: %v", node.Node, err)
	}
	a.logger.Printf("[TRACE] Updated external health check for node %q", node.Node)

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

// pingNode runs an ICMP ping against an address and returns the round-trip time.
func pingNode(addr string, method string) (time.Duration, error) {
	var rtt time.Duration
	var pingErr error

	p := fastping.NewPinger()
	switch method {
	case PingTypeUDP:
		if _, err := p.Network("udp"); err != nil {
			return 0, err
		}
		p.AddIP(addr)
	case PingTypeSocket:
		ipAddr, err := net.ResolveIPAddr("ip4:icmp", addr)
		if err != nil {
			return 0, err
		}
		p.AddIPAddr(ipAddr)
	default:
		return 0, fmt.Errorf("invalid ping type %q, should be impossible", method)
	}

	p.MaxRTT = MaxRTT
	p.OnRecv = func(addr *net.IPAddr, responseTime time.Duration) {
		rtt = responseTime
	}
	p.OnIdle = func() {
		pingErr = fmt.Errorf("ping to %q timed out", addr)
	}
	err := p.Run()
	if err != nil {
		return 0, err
	}

	if rtt != 0 {
		return rtt, nil
	} else {
		return 0, pingErr
	}
}
