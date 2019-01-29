package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hashicorp/consul-esm/version"
	"github.com/hashicorp/consul/command/flags"
	"github.com/hashicorp/consul/logger"
	"github.com/hashicorp/consul/service_os"
	"github.com/mitchellh/cli"
)

const (
	ExitCodeOK int = 0

	ExitCodeError = 10 + iota
)

func main() {
	// Handle parsing the CLI flags.
	var configFiles flags.AppendSliceValue
	var isVersion bool

	f := flag.NewFlagSet("", flag.ContinueOnError)
	f.Var(&configFiles, "config-file", "A config file to use. Can be either .hcl or .json "+
		"format. Can be specified multiple times.")
	f.Var(&configFiles, "config-dir", "A directory to look for .hcl or .json config files in. "+
		"Can be specified multiple times.")
	f.BoolVar(&isVersion, "version", false, "Print the version of this daemon.")

	f.Usage = func() {
		fmt.Print(flags.Usage(usage, f))
	}

	err := f.Parse(os.Args[1:])
	if err != nil {
		if err != flag.ErrHelp {
			fmt.Printf("error parsing flags: %v", err)
		}
		os.Exit(ExitCodeError)
	}

	if isVersion {
		fmt.Printf("%s\n", version.GetHumanVersion())
		os.Exit(ExitCodeOK)
	}

	// Build the config.
	config, err := BuildConfig([]string(configFiles))
	if err != nil {
		fmt.Println(err)
		os.Exit(ExitCodeError)
	}

	// Set up logging.
	logConfig := &logger.Config{
		LogLevel:       config.LogLevel,
		EnableSyslog:   config.EnableSyslog,
		SyslogFacility: config.SyslogFacility,
	}
	ui := &cli.BasicUi{Writer: os.Stdout, ErrorWriter: os.Stderr}
	_, gatedWriter, _, logOutput, ok := logger.Setup(logConfig, ui)
	if !ok {
		os.Exit(ExitCodeError)
	}
	logger := log.New(logOutput, "", log.LstdFlags)
	gatedWriter.Flush()

	agent, err := NewAgent(config, logger)
	if err != nil {
		panic(err)
	}

	// Set up shutdown and signal handling.
	signalCh := make(chan os.Signal, 10)
	signal.Notify(signalCh)
	go handleSignals(agent.logger, signalCh, agent)

	ui.Output("Consul ESM running!")
	if config.Datacenter == "" {
		ui.Info(fmt.Sprintf("            Datacenter: (default)"))
	} else {
		ui.Info(fmt.Sprintf("            Datacenter: %q", config.Datacenter))
	}
	ui.Info(fmt.Sprintf("               Service: %q", config.Service))
	ui.Info(fmt.Sprintf("           Service Tag: %q", config.Tag))
	ui.Info(fmt.Sprintf("            Service ID: %q", agent.serviceID()))
	ui.Info(fmt.Sprintf("Node Reconnect Timeout: %q", config.NodeReconnectTimeout.String()))

	ui.Info("")
	ui.Output("Log data will now stream in as it occurs:\n")

	// Run the agent!
	if err := agent.Run(); err != nil {
		ui.Error(err.Error())
		os.Exit(ExitCodeError)
	}

	os.Exit(ExitCodeOK)
}

func handleSignals(logger *log.Logger, signalCh chan os.Signal, agent *Agent) {
	for {
		var sig os.Signal
		select {
		case s := <-signalCh:
			sig = s
		case <-service_os.Shutdown_Channel():
			sig = os.Interrupt
		}
		logger.Printf("[INFO] Caught signal: %s", sig.String())
		switch sig {
		case syscall.SIGINT, syscall.SIGTERM:
			logger.Printf("[INFO] Shutting down...")
			agent.Shutdown()
		default:
		}
	}
}

const usage = `
Usage: consul-esm [options]

  A config file is optional, and can be either HCL or JSON format.
`
