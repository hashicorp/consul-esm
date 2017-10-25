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

			coords, _, err := a.client.Coordinate().Nodes(nil)
			if err != nil {
				a.logger.Printf("[ERR] error getting coordinate for node %q, skipping update", node.Node)
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
				continue
			}

			coordInfo, ok := self["Coord"]
			if !ok {
				a.logger.Printf("[ERR] could not decode local agent's coordinate info: %v", err)
				continue
			}

			var localCoord coordinate.Coordinate
			if err := mapstructure.Decode(coordInfo, &localCoord); err != nil {
				a.logger.Printf("[ERR] could not decode local agent's coordinate info: %v", err)
				continue
			}

			// Run an ICMP ping to the node.
			rtt, err := pingNode(node.Address)
			if err != nil {
				a.logger.Printf("[WARN] could not ping node %q: %v", node.Node, err)
				continue
			}

			// Perform the coordinate update calculations.
			client, _ := coordinate.NewClient(coordConf)
			if err := client.SetCoordinate(coord.Coord); err != nil {
				a.logger.Printf("[ERR] invalid coordinate for node %q: %v", node.Node, err)
				continue
			}
			newCoord, err := client.Update("local", &localCoord, rtt)
			if err != nil {
				a.logger.Printf("[ERR] error updating coordinate for node %q: %v", node.Node, err)
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
				continue
			}
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
