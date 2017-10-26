package main

import (
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/serf/coordinate"
	"github.com/mitchellh/mapstructure"
	"github.com/tatsushid/go-fastping"
)

func (a *Agent) getExternalNodes(shutdownCh <-chan struct{}) {
	opts := &api.QueryOptions{
		NodeMeta: a.config.NodeMeta,
	}

	nodeCh := make(chan []*api.Node, 1)
	go a.updateCoords(nodeCh, shutdownCh)

	for {
		select {
		case <-shutdownCh:
			return
		default:
		}

		nodes, meta, err := a.client.Catalog().Nodes(opts)
		if err != nil {
			a.logger.Printf("[ERR] error getting external nodes: %v", err)
			time.Sleep(retryTime)
			continue
		}

		// Don't block on sending the update
		if opts.WaitIndex != meta.LastIndex {
			select {
			case nodeCh <- nodes:
				opts.WaitIndex = meta.LastIndex
			default:
			}
		}

		time.Sleep(retryTime)
	}
}

func (a *Agent) updateCoords(nodeCh <-chan []*api.Node, shutdownCh <-chan struct{}) {
	// Wait for the first node ordering
	nodes := <-nodeCh

	for {
		select {
		case nodes = <-nodeCh:
		default:
		}

		for _, node := range nodes {
			select {
			case <-shutdownCh:
				return
			case <-time.After(a.config.CoordinateUpdateInterval):
			default:
			}

			// Get the critical status of the node.
			kvClient := a.client.KV()
			key := fmt.Sprintf("%s/%s", a.config.Service, node.Node)
			kvPair, _, err := kvClient.Get(key, nil)
			if err != nil {
				a.logger.Printf("[ERR] could not get critical status for node %q: %v", node.Node, err)
			}

			// Run an ICMP ping to the node.
			rtt, err := pingNode(node.Address)

			// Update the node's health based on the results of the ping.
			if err == nil {
				a.updateHealthyNode(node, kvClient, key, kvPair)
				if err := a.updateNodeCoordinate(node, rtt); err != nil {
					a.logger.Printf("[WARN] could not update coordinate for node %q: %v", node.Node, err)
				}
			} else {
				a.logger.Printf("[WARN] could not ping node %q: %v", node.Node, err)
				a.updateFailedNode(node, kvClient, key, kvPair)
			}
		}
	}
}

func (a *Agent) updateHealthyNode(node *api.Node, kvClient *api.KV, key string, kvPair *api.KVPair) {
	status := api.HealthPassing
	output := "Node alive or reachable"

	// If a critical node went back to passing, delete the KV entry for it.
	if kvPair != nil {
		if _, err := kvClient.Delete(key, nil); err != nil {
			a.logger.Printf("[ERR] could not delete critical timer key %q: %v", key, err)
		}
	}

	a.updateNodeCheck(node, status, output)
}

func (a *Agent) updateFailedNode(node *api.Node, kvClient *api.KV, key string, kvPair *api.KVPair) {
	status := api.HealthCritical
	output := "Node not live or unreachable"

	// If there's no existing key tracking how long the node has been critical, create one.
	if kvPair == nil {
		bytes, _ := time.Now().UTC().GobEncode()
		kvPair = &api.KVPair{
			Key:   key,
			Value: bytes,
		}
		if _, err := kvClient.Put(kvPair, nil); err != nil {
			a.logger.Printf("[ERR] could not update critical time for node %q: %v", node.Node, err)
		}
	} else {
		var criticalStart time.Time
		err := criticalStart.GobDecode(kvPair.Value)
		if err != nil {
			a.logger.Printf("[ERR] could not decode critical time for node %q: %v", node.Node, err)
		}
		if time.Since(criticalStart) > a.config.ReconnectTimeout {
			a.logger.Printf("[INFO] reaping node %q that has been failed for more then %s",
				node.Node, a.config.ReconnectTimeout.String())
			_, err := a.client.Catalog().Deregister(&api.CatalogDeregistration{Node: node.Node}, nil)
			if err != nil {
				a.logger.Printf("[ERR] could not reap node %q: %v", node.Node, err)
			}

			// Return early to avoid re-registering the check
			return
		}
	}

	a.updateNodeCheck(node, status, output)
}

// updateNodeCheck updates the node's externalNodeHealth check with the given status/output.
func (a *Agent) updateNodeCheck(node *api.Node, status, output string) {
	_, err := a.client.Catalog().Register(&api.CatalogRegistration{
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
		a.logger.Printf("[ERR] error updating external node check for node %q: %v", node.Node, err)
	}
}

// updateNodeCoordinate updates the node's coordinate entry based on the
// given RTT from a ping
func (a *Agent) updateNodeCoordinate(node *api.Node, rtt time.Duration) error {
	// Get coordinate info for the node.
	// todo: add the /v1/coordinate/node endpoint and use that instead
	coords, _, err := a.client.Coordinate().Nodes(nil)
	if err != nil {
		return fmt.Errorf("error getting coordinate for node %q, skipping update", node.Node)
	}

	var coord *api.CoordinateEntry
	for _, c := range coords {
		if c.Node == node.Node {
			coord = c
			break
		}
	}
	if coord == nil {
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

	return nil
}

// pingNode runs an ICMP ping against an address and returns the round-trip time.
func pingNode(addr string) (time.Duration, error) {
	var rtt time.Duration
	var pingErr error

	p := fastping.NewPinger()
	ipAddr, err := net.ResolveIPAddr("ip4:icmp", addr)
	if err != nil {
		return 0, err
	}
	fmt.Printf("[INFO] pinging address %q\n", ipAddr.String())
	p.AddIPAddr(ipAddr)
	p.MaxRTT = 10 * time.Second
	p.OnRecv = func(addr *net.IPAddr, responseTime time.Duration) {
		rtt = responseTime
	}
	p.OnIdle = func() {
		pingErr = fmt.Errorf("ping to %q timed out", addr)
	}
	err = p.Run()
	if err != nil {
		return 0, err
	}

	return rtt, pingErr
}
