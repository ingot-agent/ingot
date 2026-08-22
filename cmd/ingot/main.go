package main

import (
	"context"
	"os"

	"github.com/ingot-agent/ingot/internal/cli"
)

func main() { os.Exit((cli.CLI{}).Run(context.Background(), os.Args[1:])) }
