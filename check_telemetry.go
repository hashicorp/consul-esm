package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/consul-esm/telemetry"
	"go.opentelemetry.io/otel/api/metric"
)

type checkRunnerInstruments struct {
	checkTxnCounter      metric.Int64Counter
	checksUpdateDuration metric.Float64ValueRecorder
}

func newCheckRunnerInstruments() (*checkRunnerInstruments, error) {
	meter := telemetry.GlobalMeter()
	prefix := telemetry.GlobalMeterName()

	checkTxn, err := meter.NewInt64Counter(
		fmt.Sprintf("%s.check.txn", prefix),
		metric.WithDescription("A counter of check updates using Consul txn API"))
	if err != nil {
		return nil, err
	}

	checksUpdate, err := meter.NewFloat64ValueRecorder(
		fmt.Sprintf("%s.checks.update", prefix),
		metric.WithDescription("The duration (seconds) to update checks"))
	if err != nil {
		return nil, err
	}

	return &checkRunnerInstruments{
		checkTxnCounter:      checkTxn,
		checksUpdateDuration: checksUpdate,
	}, nil
}

// checkTxn is a wrapper to increment counter for check updates. This safely
// skips reporting metrics if the instrument isn't instantiated.
func (i *checkRunnerInstruments) checkTxn() {
	if i == nil {
		return
	}

	i.checkTxnCounter.Add(context.Background(), 1)
}

// checksUpdate is a wrapper to record the duration to update checks. This
// safely skips reporting metrics if the instrument isn't instantiated.
func (i *checkRunnerInstruments) checksUpdate(dur time.Duration) {
	if i == nil {
		return
	}

	i.checksUpdateDuration.Record(context.Background(), dur.Seconds())
}
