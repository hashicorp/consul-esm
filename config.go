package main

import "time"

type Config struct {
	Service                  string
	LockKey                  string
	NodeMeta                 map[string]string
	MaxFailures              uint
	Interval                 time.Duration
	DeregisterAfter          time.Duration
	CheckUpdateInterval      time.Duration
	CoordinateUpdateInterval time.Duration
}

func DefaultConfig() *Config {
	return &Config{
		Service: "consul-esm",
		LockKey: "consul-esm/lock",
		NodeMeta: map[string]string{
			"external-node": "true",
		},
		MaxFailures:              3,
		Interval:                 10 * time.Second,
		DeregisterAfter:          72 * time.Hour,
		CheckUpdateInterval:      5 * time.Minute,
		CoordinateUpdateInterval: 1 * time.Second,
	}
}
