package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/ingot-agent/ingot/internal/cli"
	"github.com/ingot-agent/ingot/internal/coreupdate"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	_ = coreupdate.CleanupPrevious()
	ctx, stop := signal.NotifyContext(context.Background(), processSignals()...)
	defer stop()
	restoreProcessSignalsOnCancel(ctx, stop)
	return (cli.CLI{}).Run(ctx, arguments)
}

func restoreProcessSignalsOnCancel(ctx context.Context, stop context.CancelFunc) {
	go func() {
		<-ctx.Done()
		stop()
	}()
}
