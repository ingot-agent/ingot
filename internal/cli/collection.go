package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/collection"
	ingothome "github.com/ingot-agent/ingot/internal/home"
)

func (cli CLI) runCollection(ctx context.Context, homePath string, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("collection requires a subcommand: inspect, plan, or apply")
	}
	command, rest := arguments[0], arguments[1:]
	if command == "-h" || command == "--help" || command == "help" {
		cli.collectionUsage()
		return 0
	}
	remaining, expectedDigest, _, err := extractStringOption(rest, "expect-digest")
	if err != nil {
		return cli.usageError(err.Error())
	}
	remaining, acceptOrder, err := extractBoolOption(remaining, "accept-order")
	if err != nil {
		return cli.usageError(err.Error())
	}
	options := ingothome.RecipeOptions{}
	if command == "plan" || command == "apply" {
		options, remaining, err = extractRecipeOptions(remaining)
		if err != nil {
			return cli.usageError(err.Error())
		}
	} else if command != "inspect" {
		return cli.usageError("unknown collection subcommand " + strconv.Quote(command))
	}
	if command == "inspect" && acceptOrder {
		return cli.usageError("collection inspect does not accept --accept-order")
	}
	if len(remaining) != 1 {
		return cli.usageError("collection " + command + " requires exactly one local path or HTTPS URL")
	}
	if remaining[0] == "-h" || remaining[0] == "--help" {
		cli.collectionUsage()
		return 0
	}
	if remaining[0][0] == '-' {
		return cli.usageError("unknown collection option " + strconv.Quote(remaining[0]))
	}
	loaded, err := (collection.Loader{}).Load(ctx, remaining[0], expectedDigest)
	if err != nil {
		return cli.result(err)
	}
	if command == "inspect" {
		return cli.result(writeJSON(cli.Stdout, loaded))
	}
	home, err := ingothome.Open(homePath)
	if err != nil {
		return cli.result(err)
	}
	planOptions := collection.PlanOptions{AcceptOrder: acceptOrder}
	if command == "plan" {
		plan, paths, err := home.PlanCollection(ctx, options, loaded, planOptions)
		if err == nil {
			output := struct {
				Recipe string           `json:"recipe"`
				Lock   string           `json:"lock"`
				Plan   *collection.Plan `json:"plan"`
			}{Recipe: paths.Recipe, Lock: paths.Lock, Plan: plan}
			err = writeJSON(cli.Stdout, output)
		}
		return cli.result(err)
	}
	result, err := home.ApplyCollection(ctx, options, loaded, planOptions, builder.ResolveOptions{})
	if err == nil {
		err = writeJSON(cli.Stdout, result)
	}
	return cli.result(err)
}

func (cli CLI) collectionUsage() {
	_, _ = fmt.Fprintln(cli.Stdout, "usage: ingot collection inspect [--expect-digest DIGEST] <path-or-https-url>")
	_, _ = fmt.Fprintln(cli.Stdout, "       ingot collection plan [--use FILE] [--lock FILE] [--expect-digest DIGEST] [--accept-order] <source>")
	_, _ = fmt.Fprintln(cli.Stdout, "       ingot collection apply [--use FILE] [--lock FILE] [--expect-digest DIGEST] [--accept-order] <source>")
}
