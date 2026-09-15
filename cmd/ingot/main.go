package main

import (
	"context"
	"os"

	"github.com/ingot-agent/ingot/internal/cli"
	"github.com/ingot-agent/ingot/internal/coreupdate"
)

func main() {
	_ = coreupdate.CleanupPrevious()
	os.Exit((cli.CLI{}).Run(context.Background(), os.Args[1:]))
}
