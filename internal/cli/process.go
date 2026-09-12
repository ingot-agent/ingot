package cli

import (
	"context"
	"fmt"
	"time"

	ingothome "github.com/ingot-agent/ingot/internal/home"
)

func (cli CLI) runStop(ctx context.Context, home *ingothome.Home, arguments []string) int {
	remaining, processID, _, err := extractStringOption(arguments, "process")
	if err != nil {
		return cli.usageError(err.Error())
	}
	remaining, timeoutValue, hasTimeout, err := extractStringOption(remaining, "timeout")
	if err != nil {
		return cli.usageError(err.Error())
	}
	timeout := 10 * time.Second
	if hasTimeout {
		timeout, err = time.ParseDuration(timeoutValue)
		if err != nil {
			return cli.usageError(err.Error())
		}
	}
	if processID != "" {
		if len(remaining) != 0 {
			return cli.usageError("stop --process takes no runtime name")
		}
		err = home.StopProcess(ctx, processID, timeout)
	} else {
		if len(remaining) != 1 {
			return cli.usageError("stop requires a runtime name or --process")
		}
		err = home.RuntimeStop(ctx, remaining[0], "", timeout)
	}
	if err == nil {
		err = writeJSON(cli.Stdout, map[string]any{"stopped": true})
	}
	return cli.result(err)
}

func (cli CLI) runSupervise(ctx context.Context, home *ingothome.Home, arguments []string) int {
	remaining, name, _, err := extractStringOption(arguments, "runtime")
	if err != nil {
		return 2
	}
	remaining, processID, _, err := extractStringOption(remaining, "process")
	if err != nil {
		return 2
	}
	remaining, argv, _, err := extractStringOption(remaining, "argv")
	if err != nil {
		return 2
	}
	if len(remaining) != 0 || name == "" || processID == "" || argv == "" {
		return 2
	}
	code, err := home.SuperviseDetached(ctx, name, processID, argv)
	if err != nil {
		_, _ = fmt.Fprintln(cli.Stderr, err)
		return 1
	}
	return code
}
