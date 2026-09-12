package cli

import (
	"context"
	"fmt"
	"strconv"

	ingothome "github.com/ingot-agent/ingot/internal/home"
)

func (cli CLI) runProject(ctx context.Context, home *ingothome.Home, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("project requires a subcommand")
	}
	command, rest := arguments[0], arguments[1:]
	if command != "init" {
		return cli.usageError("unknown project subcommand " + strconv.Quote(command))
	}
	remaining, profile, hasProfile, err := extractStringOption(rest, "profile")
	if err != nil {
		return cli.usageError(err.Error())
	}
	if !hasProfile {
		profile = "default"
	}
	remaining, force, err := extractBoolOption(remaining, "force")
	if err != nil {
		return cli.usageError(err.Error())
	}
	if len(remaining) != 1 {
		return cli.usageError("project init requires exactly one directory")
	}
	if remaining[0] == "-h" || remaining[0] == "--help" {
		_, _ = fmt.Fprintln(cli.Stdout, "usage: ingot [--home PATH] project init <directory> [--profile default|minimal] [--force]")
		return 0
	}
	if len(remaining[0]) > 0 && remaining[0][0] == '-' {
		return cli.usageError("unknown project init option " + strconv.Quote(remaining[0]))
	}
	result, err := home.InitProject(ctx, ingothome.ProjectInitOptions{Directory: remaining[0], Profile: profile, Force: force})
	if err == nil {
		err = writeJSON(cli.Stdout, result)
	}
	return cli.result(err)
}
