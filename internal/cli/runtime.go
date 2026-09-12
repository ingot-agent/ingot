package cli

import (
	"context"
	"os"
	"strconv"
	"time"

	ingothome "github.com/ingot-agent/ingot/internal/home"
)

func (cli CLI) runRuntime(ctx context.Context, home *ingothome.Home, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("runtime requires a subcommand")
	}
	command, rest := arguments[0], arguments[1:]
	switch command {
	case "create":
		before, argv := splitDashDash(rest)
		remaining, imageRef, _, err := extractStringOption(before, "image")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(remaining) != 1 || imageRef == "" {
			return cli.usageError("runtime create requires name and --image")
		}
		view, err := home.RuntimeCreate(ctx, remaining[0], imageRef, argv)
		if err == nil {
			err = writeJSON(cli.Stdout, view)
		}
		return cli.result(err)
	case "list":
		if len(rest) != 0 {
			return cli.usageError("runtime list takes no arguments")
		}
		views, err := home.RuntimeList(ctx)
		if err == nil {
			err = writeJSON(cli.Stdout, views)
		}
		return cli.result(err)
	case "inspect":
		if len(rest) != 1 {
			return cli.usageError("runtime inspect requires a name")
		}
		view, err := home.RuntimeInspect(ctx, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, view)
		}
		return cli.result(err)
	case "switch":
		if len(rest) != 2 {
			return cli.usageError("runtime switch requires name and image reference")
		}
		view, err := home.RuntimeSwitch(ctx, rest[0], rest[1])
		if err == nil {
			err = writeJSON(cli.Stdout, view)
		}
		return cli.result(err)
	case "rollback":
		if len(rest) != 1 {
			return cli.usageError("runtime rollback requires a name")
		}
		view, err := home.RuntimeRollback(ctx, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, view)
		}
		return cli.result(err)
	case "command":
		if len(rest) < 2 {
			return cli.usageError("runtime command requires set|clear and a name")
		}
		action := rest[0]
		if action == "clear" {
			if len(rest) != 2 {
				return cli.usageError("runtime command clear requires a name")
			}
			view, err := home.RuntimeCommand(ctx, rest[1], []string{})
			if err == nil {
				err = writeJSON(cli.Stdout, view)
			}
			return cli.result(err)
		}
		if action == "set" {
			before, argv := splitDashDash(rest[1:])
			if len(before) != 1 {
				return cli.usageError("runtime command set requires name -- argv")
			}
			view, err := home.RuntimeCommand(ctx, before[0], argv)
			if err == nil {
				err = writeJSON(cli.Stdout, view)
			}
			return cli.result(err)
		}
		return cli.usageError("runtime command requires set or clear")
	case "run":
		before, argv := splitDashDash(rest)
		if len(before) != 1 {
			return cli.usageError("runtime run requires a name")
		}
		temporary := argv
		if argv == nil {
			temporary = nil
		}
		code, err := home.RuntimeRun(ctx, before[0], temporary, os.Stdin, cli.Stdout, cli.Stderr)
		if err != nil {
			return cli.result(err)
		}
		return code
	case "start":
		before, argv := splitDashDash(rest)
		remaining, timeoutValue, hasTimeout, err := extractStringOption(before, "timeout")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(remaining) != 1 {
			return cli.usageError("runtime start requires a name")
		}
		timeout := 30 * time.Second
		if hasTimeout {
			timeout, err = time.ParseDuration(timeoutValue)
			if err != nil {
				return cli.usageError(err.Error())
			}
		}
		temporary := argv
		if argv == nil {
			temporary = nil
		}
		record, err := home.RuntimeStart(ctx, remaining[0], temporary, timeout)
		if err == nil {
			err = writeJSON(cli.Stdout, record)
		}
		return cli.result(err)
	case "restart":
		remaining, timeoutValue, hasTimeout, err := extractStringOption(rest, "timeout")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(remaining) != 1 {
			return cli.usageError("runtime restart requires a name")
		}
		timeout := 30 * time.Second
		if hasTimeout {
			timeout, err = time.ParseDuration(timeoutValue)
			if err != nil {
				return cli.usageError(err.Error())
			}
		}
		record, err := home.RuntimeRestart(ctx, remaining[0], timeout)
		if err == nil {
			err = writeJSON(cli.Stdout, record)
		}
		return cli.result(err)
	case "logs":
		remaining, processID, _, err := extractStringOption(rest, "process")
		if err != nil {
			return cli.usageError(err.Error())
		}
		remaining, follow, err := extractBoolOption(remaining, "follow")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(remaining) != 1 {
			return cli.usageError("runtime logs requires a name")
		}
		return cli.result(home.RuntimeLogs(ctx, remaining[0], processID, follow, cli.Stdout))
	case "delete":
		remaining, purge, err := extractBoolOption(rest, "purge")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(remaining) != 1 {
			return cli.usageError("runtime delete requires a name")
		}
		err = home.RuntimeDelete(ctx, remaining[0], purge)
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"deleted": remaining[0], "purged": purge})
		}
		return cli.result(err)
	default:
		return cli.usageError("unknown runtime subcommand " + strconv.Quote(command))
	}
}

func (cli CLI) runNamed(ctx context.Context, home *ingothome.Home, arguments []string) int {
	before, argv := splitDashDash(arguments)
	remaining, name, _, err := extractStringOption(before, "name")
	if err != nil {
		return cli.usageError(err.Error())
	}
	remaining, detach, err := extractBoolOption(remaining, "detach")
	if err != nil {
		return cli.usageError(err.Error())
	}
	if name == "" || len(remaining) != 1 {
		return cli.usageError("run requires --name and one image reference")
	}
	if _, err := home.RuntimeCreate(ctx, name, remaining[0], argv); err != nil {
		return cli.result(err)
	}
	if detach {
		record, err := home.RuntimeStart(ctx, name, nil, 30*time.Second)
		if err == nil {
			err = writeJSON(cli.Stdout, record)
		}
		return cli.result(err)
	}
	code, err := home.RuntimeRun(ctx, name, nil, os.Stdin, cli.Stdout, cli.Stderr)
	if err != nil {
		return cli.result(err)
	}
	return code
}
