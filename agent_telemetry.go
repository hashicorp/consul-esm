package main

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl-opentelemetry"
	"go.opentelemetry.io/otel/api/metric"
)

type agentInstruments struct {
	coordTxnCounter metric.Int64Counter
}

func newAgentInstruments() (*agentInstruments, error) {
	meter := hclotel.GlobalMeter()
	prefix := hclotel.GlobalMeterName()

	coordTxn, err := meter.NewInt64Counter(
		fmt.Sprintf("%s.check.txn", prefix),
		metric.WithDescription("A counter of node check updates using Consul txn API"))
	if err != nil {
		return nil, err
	}

	return &agentInstruments{
		coordTxnCounter: coordTxn,
	}, nil
}

func (i *agentInstruments) coordTxn() {
	if i == nil {
		return
	}

	i.coordTxnCounter.Add(context.Background(), 1)
}
