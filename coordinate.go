package main

import (
	"time"

	"net"

	"fmt"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/serf/coordinate"
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

		if opts.WaitIndex != meta.LastIndex {
			opts.WaitIndex = meta.LastIndex
			nodeCh <- nodes
		}

		time.Sleep(retryTime)
	}
}

func (a *Agent) updateCoords(nodeCh <-chan []*api.Node, shutdownCh <-chan struct{}) {
	// Wait for the first node ordering
	nodes := <-nodeCh
	coordConf := coordinate.DefaultConfig()

	for {
		select {
		case <-shutdownCh:
			return
		case nodes = <-nodeCh:
		default:
		}

		for _, node := range nodes {
			coords, _, err := a.client.Coordinate().Nodes(nil)
			if err != nil {
				a.logger.Printf("[ERR] error getting coordinate for node %q, skipping update", node.Node)
				time.Sleep(retryTime)
				continue
			}

			// Get coordinate info for the node.
			// todo: add the /v1/coordinate/node endpoint and use that instead
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
					Coord:   coordinate.NewCoordinate(coordConf),
				}
			}

			// Get the local agent's coordinate info.
			self, err := a.client.Agent().Self()
			if err != nil {
				a.logger.Printf("[ERR] could not retrieve local agent's coordinate info: %v", err)
				time.Sleep(retryTime)
				continue
			}
			coordInfo := self["Coord"]
			vecRaw := coordInfo["Vec"].([]interface{})
			vec := make([]float64, len(vecRaw))
			for i, v := range vecRaw {
				vec[i] = v.(float64)
			}
			localCoord := &coordinate.Coordinate{
				Vec:        vec,
				Error:      coordInfo["Error"].(float64),
				Adjustment: coordInfo["Adjustment"].(float64),
				Height:     coordInfo["Height"].(float64),
			}

			// Run an ICMP ping to the node.
			rtt, err := pingNode(node.Address)
			if err != nil {
				a.logger.Printf("[WARN] could not ping node %q: %v", node.Node, err)
				time.Sleep(retryTime)
				continue
			}

			// Perform the coordinate update calculations.
			client, _ := coordinate.NewClient(coordConf)
			if err := client.SetCoordinate(coord.Coord); err != nil {
				a.logger.Printf("[ERR] invalid coordinate for node %q: %v", node.Node, err)
				time.Sleep(retryTime)
				continue
			}
			newCoord, err := client.Update("local", localCoord, rtt)
			if err != nil {
				a.logger.Printf("[ERR] error updating coordinate for node %q: %v", node.Node, err)
				time.Sleep(retryTime)
				continue
			}

			// Update the coordinate in the catalog.
			_, err = a.client.Coordinate().Update(&api.CoordinateEntry{
				Node:    coord.Node,
				Segment: coord.Segment,
				Coord:   newCoord,
			}, nil)

			if err != nil {
				a.logger.Printf("[ERR] error applying coordinate update for node %q: %v", node.Node, err)
				time.Sleep(retryTime)
				continue
			}

			time.Sleep(a.config.CoordinateUpdateInterval)
		}
	}
}

// pingNode runs an ICMP ping against an address and returns the round-trip time.
func pingNode(addr string) (time.Duration, error) {
	var rtt time.Duration
	var err error

	p := fastping.NewPinger()
	ipAddr, err := net.ResolveIPAddr("ip4:icmp", addr)
	if err != nil {
		return 0, err
	}
	p.AddIPAddr(ipAddr)
	p.OnRecv = func(addr *net.IPAddr, responseTime time.Duration) {
		rtt = responseTime
	}
	p.OnIdle = func() {
		err = fmt.Errorf("ping to %q timed out", addr)
	}
	err = p.Run()
	if err != nil {
		return 0, err
	}

	return rtt, err
}
