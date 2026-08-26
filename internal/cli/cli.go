package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"

	ingothome "github.com/ingot-agent/ingot/internal/home"
)

type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
}

func (cli CLI) Run(ctx context.Context, arguments []string) int {
	if cli.Stdout == nil {
		cli.Stdout = os.Stdout
	}
	if cli.Stderr == nil {
		cli.Stderr = os.Stderr
	}
	homePath, arguments, err := parseGlobalHome(arguments)
	if err != nil {
		_, _ = fmt.Fprintln(cli.Stderr, err)
		return 2
	}
	home, err := ingothome.Open(homePath)
	if err != nil {
		_, _ = fmt.Fprintln(cli.Stderr, err)
		return 1
	}
	if len(arguments) == 0 {
		cli.usage()
		return 2
	}
	command, rest := arguments[0], arguments[1:]
	switch command {
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		flags.SetOutput(cli.Stderr)
		profile := flags.String("profile", "default", "bundle profile: default or minimal")
		bundlePath := flags.String("bundle", "", "official plugins distribution directory (default: locate relative to the executable)")
		force := flags.Bool("force", false, "overwrite an already initialized home")
		directApply := flags.Bool("apply", false, "resolve, build and switch current immediately")
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			return cli.usageError("init takes no positional arguments")
		}
		result, err := home.Init(ingothome.InitOptions{Profile: *profile, BundlePath: *bundlePath, Force: *force})
		if err != nil {
			return cli.result(err)
		}
		if result.WrotePlugins {
			_, _ = fmt.Fprintf(cli.Stdout, "Initialized ingot home: %s\n", result.Home)
			_, _ = fmt.Fprintf(cli.Stdout, "  profile: %s (%d plugins)\n", result.Profile, len(result.Plugins))
			_, _ = fmt.Fprintf(cli.Stdout, "  sources: %s\n", result.BundledPath)
			_, _ = fmt.Fprintf(cli.Stdout, "  wrote: %s\n", result.PluginsPath)
		}
		if result.WroteConfig {
			_, _ = fmt.Fprintf(cli.Stdout, "  wrote: %s\n", result.ConfigPath)
		}
		if *directApply {
			applied, err := home.Apply(ctx, builder.ResolveOptions{})
			if err != nil {
				_, _ = fmt.Fprintln(cli.Stderr, "init files written; apply failed:", err)
				return 1
			}
			_, _ = fmt.Fprintf(cli.Stdout, "  applied image: %s\n", applied.ImageID)
			_, _ = fmt.Fprintln(cli.Stdout, "\nYou can now run: ingot <command>  (e.g. ingot chat)")
			return 0
		}
		_, _ = fmt.Fprintln(cli.Stdout, "\nNext steps:")
		_, _ = fmt.Fprintf(cli.Stdout, "  1. Edit %s — set your model provider base_url and api_key.\n", result.ConfigPath)
		_, _ = fmt.Fprintln(cli.Stdout, "  2. Run: ingot apply")
		_, _ = fmt.Fprintln(cli.Stdout, "  3. Run: ingot chat")
		return 0
	case "resolve":
		if len(rest) != 0 {
			return cli.usageError("resolve takes no arguments")
		}
		lock, err := home.Resolve(ctx, builder.ResolveOptions{})
		if err == nil {
			id, _ := lock.ImageID()
			_, _ = fmt.Fprintln(cli.Stdout, id)
		}
		return cli.result(err)
	case "build":
		if len(rest) != 0 {
			return cli.usageError("build takes no arguments")
		}
		result, err := home.Build(ctx)
		if err == nil {
			_, _ = fmt.Fprintln(cli.Stdout, result.ImageID)
		}
		return cli.result(err)
	case "apply":
		if len(rest) != 0 {
			return cli.usageError("apply takes no arguments")
		}
		result, err := home.Apply(ctx, builder.ResolveOptions{})
		if err == nil {
			_, _ = fmt.Fprintln(cli.Stdout, result.ImageID)
		}
		return cli.result(err)
	case "status":
		if len(rest) != 0 {
			return cli.usageError("status takes no arguments")
		}
		status, err := home.Status()
		if err == nil {
			err = writeJSON(cli.Stdout, status)
		}
		return cli.result(err)
	case "inspect":
		if len(rest) > 1 {
			return cli.usageError("inspect accepts at most one plugin id or name")
		}
		reference := ""
		if len(rest) == 1 {
			reference = rest[0]
		}
		inspection, err := home.Inspect(reference)
		if err == nil {
			err = writeJSON(cli.Stdout, inspection)
		}
		return cli.result(err)
	case "rollback":
		if len(rest) > 1 {
			return cli.usageError("rollback accepts at most one image id")
		}
		imageID := ""
		if len(rest) == 1 {
			imageID = rest[0]
		}
		err := home.Rollback(ctx, imageID)
		if err == nil {
			current, _ := home.Current()
			_, _ = fmt.Fprintln(cli.Stdout, current)
		}
		return cli.result(err)
	case "gc":
		flags := flag.NewFlagSet("gc", flag.ContinueOnError)
		flags.SetOutput(cli.Stderr)
		keep := flags.Int("keep", 3, "number of recent images to retain")
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			return cli.usageError("gc takes no positional arguments")
		}
		removed, err := home.GC(ctx, *keep)
		if err == nil {
			err = writeJSON(cli.Stdout, removed)
		}
		return cli.result(err)
	case "plugin":
		return cli.runPlugin(ctx, home, rest)
	case "help", "--help", "-h":
		cli.usage()
		return 0
	default:
		err := home.RunCurrent(ctx, arguments)
		var exitErr *ingothome.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.Code
		}
		return cli.result(err)
	}
}

func (cli CLI) runPlugin(ctx context.Context, home *ingothome.Home, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("plugin requires a subcommand")
	}
	command, rest := arguments[0], arguments[1:]
	switch command {
	case "list":
		if len(rest) != 0 {
			return cli.usageError("plugin list takes no arguments")
		}
		inspection, err := home.Inspect("")
		if err == nil {
			err = writeJSON(cli.Stdout, inspection.DirectPlugins)
		}
		return cli.result(err)
	case "inspect":
		if len(rest) != 1 {
			return cli.usageError("plugin inspect requires an id or name")
		}
		inspection, err := home.Inspect(rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, inspection)
		}
		return cli.result(err)
	case "add":
		rest, apply, err := extractBoolOption(rest, "apply")
		if err != nil {
			return cli.usageError(err.Error())
		}
		rest, localPath, _, err := extractStringOption(rest, "path")
		if err != nil {
			return cli.usageError(err.Error())
		}
		var plugin builder.DesiredPlugin
		if localPath != "" {
			if len(rest) != 0 {
				return cli.usageError("plugin add --path takes no module argument")
			}
			absolute, absoluteErr := filepath.Abs(localPath)
			if absoluteErr != nil {
				return cli.result(absoluteErr)
			}
			moduleID, identityErr := builder.ModuleIdentity(filepath.Join(absolute, "go.mod"))
			if identityErr != nil {
				return cli.result(identityErr)
			}
			locator, relativeErr := filepath.Rel(home.Root, absolute)
			if relativeErr != nil {
				locator = absolute
			}
			plugin = builder.DesiredPlugin{Module: moduleID, Path: filepath.ToSlash(locator)}
		} else {
			if len(rest) != 1 {
				return cli.usageError("plugin add requires module[@query] or --path")
			}
			moduleID, version, queryErr := home.ResolveModuleQuery(ctx, rest[0])
			err = queryErr
			plugin = builder.DesiredPlugin{Module: moduleID, Version: version}
		}
		if err == nil {
			_, err = home.Add(ctx, plugin, builder.ResolveOptions{}, apply)
		}
		return cli.result(err)
	case "remove":
		rest, apply, err := extractBoolOption(rest, "apply")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(rest) != 1 {
			return cli.usageError("plugin remove requires an id or name")
		}
		_, err = home.Remove(ctx, rest[0], builder.ResolveOptions{}, apply)
		return cli.result(err)
	case "update":
		rest, apply, err := extractBoolOption(rest, "apply")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(rest) != 1 {
			return cli.usageError("plugin update requires name[@query] or id[@query]")
		}
		token := rest[0]
		reference, query := splitReferenceQuery(token)
		lookup, err := home.LookupPlugin(reference)
		if err != nil {
			return cli.result(err)
		}
		if query == "" {
			query = "latest"
		}
		moduleID, version, err := home.ResolveModuleQuery(ctx, lookup.Plugin.Module+"@"+query)
		if err == nil {
			_, err = home.Update(ctx, reference, builder.DesiredPlugin{Module: moduleID, Version: version}, builder.ResolveOptions{}, apply)
		}
		return cli.result(err)
	case "reorder":
		rest, apply, err := extractBoolOption(rest, "apply")
		if err != nil {
			return cli.usageError(err.Error())
		}
		rest, before, hasBefore, err := extractStringOption(rest, "before")
		if err != nil {
			return cli.usageError(err.Error())
		}
		rest, after, hasAfter, err := extractStringOption(rest, "after")
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(rest) != 1 || hasBefore == hasAfter {
			return cli.usageError("plugin reorder requires one plugin and exactly one of --before/--after")
		}
		anchor, isBefore := before, true
		if hasAfter {
			anchor, isBefore = after, false
		}
		_, err = home.Reorder(ctx, rest[0], anchor, isBefore, builder.ResolveOptions{}, apply)
		return cli.result(err)
	default:
		return cli.usageError("unknown plugin subcommand " + strconv.Quote(command))
	}
}

func parseGlobalHome(arguments []string) (string, []string, error) {
	if len(arguments) == 0 {
		return "", arguments, nil
	}
	if arguments[0] == "--home" {
		if len(arguments) < 2 {
			return "", nil, fmt.Errorf("--home requires a path")
		}
		return arguments[1], arguments[2:], nil
	}
	if strings.HasPrefix(arguments[0], "--home=") {
		return strings.TrimPrefix(arguments[0], "--home="), arguments[1:], nil
	}
	return "", arguments, nil
}

func splitReferenceQuery(value string) (string, string) {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return value, ""
	}
	return value[:index], value[index+1:]
}

func extractBoolOption(arguments []string, name string) ([]string, bool, error) {
	option := "--" + name
	found := false
	result := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if argument == option {
			if found {
				return nil, false, fmt.Errorf("%s may be specified only once", option)
			}
			found = true
			continue
		}
		if strings.HasPrefix(argument, option+"=") {
			return nil, false, fmt.Errorf("%s does not take a value", option)
		}
		result = append(result, argument)
	}
	return result, found, nil
}

func extractStringOption(arguments []string, name string) ([]string, string, bool, error) {
	option := "--" + name
	value := ""
	found := false
	result := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument != option && !strings.HasPrefix(argument, option+"=") {
			result = append(result, argument)
			continue
		}
		if found {
			return nil, "", false, fmt.Errorf("%s may be specified only once", option)
		}
		found = true
		if strings.HasPrefix(argument, option+"=") {
			value = strings.TrimPrefix(argument, option+"=")
		} else {
			if index+1 >= len(arguments) {
				return nil, "", false, fmt.Errorf("%s requires a value", option)
			}
			index++
			value = arguments[index]
		}
		if value == "" {
			return nil, "", false, fmt.Errorf("%s requires a non-empty value", option)
		}
	}
	return result, value, found, nil
}
func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
func (cli CLI) result(err error) int {
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(cli.Stderr, err)
	return 1
}
func (cli CLI) usageError(message string) int { _, _ = fmt.Fprintln(cli.Stderr, message); return 2 }
func (cli CLI) usage() {
	_, _ = fmt.Fprintln(cli.Stdout, "usage: ingot [--home PATH] <init|resolve|build|apply|status|inspect|rollback|gc|plugin ...|runtime command>")
}
