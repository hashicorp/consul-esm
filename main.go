package main

import (
	"os"
	"os/signal"
)

func main() {
	config := DefaultConfig()
	a, err := NewAgent(config)
	if err != nil {
		panic(err)
	}

	shutdownCh := make(chan struct{})

	signalCh := make(chan os.Signal, 10)
	signal.Notify(signalCh)
	go handleSignals(signalCh, shutdownCh)

	if err := a.Run(shutdownCh); err != nil {
		panic(err)
	}
}

func handleSignals(signalCh chan os.Signal, shutdownCh chan struct{}) {
	for sig := range signalCh {
		switch sig {
		case os.Interrupt:
			close(shutdownCh)
		}
	}
}
