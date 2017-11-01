package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/hashicorp/consul/command/flags"
)

func main() {
	// Set up the flags
	var configFiles flags.AppendSliceValue

	f := flag.NewFlagSet("", flag.ContinueOnError)
	f.Var(&configFiles, "config-file", "A config file to use. Can be either .hcl or .json "+
		"format. Can be specified multiple times.")
	f.Var(&configFiles, "config-dir", "A directory to look for .hcl or .json config files in. "+
		"Can be specified multiple times.")

	f.Usage = func() {
		fmt.Print(flags.Usage(usage, f))
	}

	err := f.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	config := DefaultConfig()
	err = MergeConfigPaths(config, []string(configFiles))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	fmt.Printf("%q %v\n", config.HTTPAddr, config.CoordinateUpdateInterval.String())

	a, err := NewAgent(config)
	if err != nil {
		panic(err)
	}

	shutdownCh := make(chan struct{})

	signalCh := make(chan os.Signal, 10)
	signal.Notify(signalCh)
	go handleSignals(a.logger, signalCh, shutdownCh)

	if err := a.Run(shutdownCh); err != nil {
		panic(err)
	}
}

func handleSignals(logger *log.Logger, signalCh chan os.Signal, shutdownCh chan struct{}) {
	for sig := range signalCh {
		switch sig {
		case os.Interrupt:
			logger.Printf("[INFO] got signal, shutting down...")
			close(shutdownCh)
		}
	}
}

const usage = `
Usage: consul-esm [options]

  A config file is optional, and can be either HCL or JSON format.
`
