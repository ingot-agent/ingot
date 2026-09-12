package cli

import (
	"context"
	"encoding/json"
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
		return cli.usageError(err.Error())
	}
	if len(arguments) == 0 {
		cli.usage()
		return 2
	}
	command, rest := arguments[0], arguments[1:]
	if command == "help" || command == "--help" || command == "-h" {
		cli.usage()
		return 0
	}
	var home *ingothome.Home
	if command == "supervise" {
		home, err = ingothome.OpenForSupervisor(homePath)
	} else if command == "init" {
		home, err = ingothome.OpenForInit(homePath)
	} else {
		home, err = ingothome.Open(homePath)
	}
	if err != nil {
		return cli.result(err)
	}
	switch command {
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		flags.SetOutput(cli.Stderr)
		profile := flags.String("profile", "default", "bundle profile")
		bundlePath := flags.String("bundle", "", "official plugins directory")
		force := flags.Bool("force", false, "rewrite managed Home configuration")
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			return cli.usageError("init takes no positional arguments")
		}
		result, err := home.Init(ingothome.InitOptions{Profile: *profile, BundlePath: *bundlePath, Force: *force})
		if err == nil {
			err = writeJSON(cli.Stdout, result)
		}
		return cli.result(err)
	case "resolve":
		options, resolveOptions, code := cli.parseRecipeFlags("resolve", rest, false)
		if code != 0 {
			return code
		}
		lock, paths, err := home.ResolveProject(ctx, options, resolveOptions)
		if err == nil {
			imageID, _ := lock.ImageID()
			err = writeJSON(cli.Stdout, map[string]any{"recipe": paths.Recipe, "lock": paths.Lock, "image_id": imageID})
		}
		return cli.result(err)
	case "project":
		return cli.runProject(ctx, home, rest)
	case "build":
		options, resolveOptions, code := cli.parseRecipeFlags("build", rest, true)
		if code != 0 {
			return code
		}
		result, paths, err := home.BuildProject(ctx, options, resolveOptions)
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"recipe": paths.Recipe, "lock": paths.Lock, "image_id": result.ImageID, "artifact_digest": result.ArtifactDigest, "target": result.Target})
		}
		return cli.result(err)
	case "status":
		options, _, code := cli.parseRecipeFlags("status", rest, false)
		if code != 0 {
			return code
		}
		status, err := home.ProjectStatus(options)
		if err == nil {
			err = writeJSON(cli.Stdout, status)
		}
		return cli.result(err)
	case "inspect":
		before, _ := splitDashDash(rest)
		options, remaining, err := extractRecipeOptions(before)
		if err != nil {
			return cli.usageError(err.Error())
		}
		if len(remaining) > 1 {
			return cli.usageError("inspect accepts at most one plugin id or name")
		}
		reference := ""
		if len(remaining) == 1 {
			reference = remaining[0]
		}
		inspection, err := home.ProjectInspect(options, reference)
		if err == nil {
			err = writeJSON(cli.Stdout, inspection)
		}
		return cli.result(err)
	case "image":
		return cli.runImage(ctx, home, rest)
	case "runtime":
		return cli.runRuntime(ctx, home, rest)
	case "run":
		return cli.runNamed(ctx, home, rest)
	case "ps":
		if len(rest) != 0 {
			return cli.usageError("ps takes no arguments")
		}
		processes, err := home.Processes(ctx)
		if err == nil {
			err = writeJSON(cli.Stdout, processes)
		}
		return cli.result(err)
	case "stop":
		return cli.runStop(ctx, home, rest)
	case "gc":
		flags := flag.NewFlagSet("gc", flag.ContinueOnError)
		flags.SetOutput(cli.Stderr)
		keep := flags.Int("keep-recent", 3, "recent unreferenced images to keep")
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			return cli.usageError("gc takes no positional arguments")
		}
		removed, err := home.GC(ctx, *keep)
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"removed": removed})
		}
		return cli.result(err)
	case "plugin":
		return cli.runPlugin(ctx, home, rest)
	case "bundle":
		return cli.runBundle(ctx, home, rest)
	case "supervise":
		return cli.runSupervise(ctx, home, rest)
	case "apply", "rollback":
		return cli.usageError(command + " was removed in M2; use build/runtime switch/runtime rollback")
	default:
		return cli.usageError("unknown command " + strconv.Quote(command))
	}
}

func (cli CLI) parseRecipeFlags(name string, arguments []string, build bool) (ingothome.RecipeOptions, builder.ResolveOptions, int) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(cli.Stderr)
	use := flags.String("use", "", "recipe path")
	lock := flags.String("lock", "", "lock path")
	locked := flags.Bool("locked", false, "require an existing up-to-date lock")
	tag := flags.String("tag", "", "image name:tag")
	if err := flags.Parse(arguments); err != nil {
		return ingothome.RecipeOptions{}, builder.ResolveOptions{}, 2
	}
	if flags.NArg() != 0 {
		return ingothome.RecipeOptions{}, builder.ResolveOptions{}, cli.usageError(name + " takes no positional arguments")
	}
	if !build && (*locked || *tag != "") {
		return ingothome.RecipeOptions{}, builder.ResolveOptions{}, cli.usageError(name + " does not accept --locked or --tag")
	}
	return ingothome.RecipeOptions{Use: *use, Lock: *lock, Locked: *locked, Tag: *tag}, builder.ResolveOptions{}, 0
}

func (cli CLI) runBundle(ctx context.Context, home *ingothome.Home, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("bundle requires a subcommand: check or update")
	}
	command, rest := arguments[0], arguments[1:]
	flags := flag.NewFlagSet("bundle "+command, flag.ContinueOnError)
	flags.SetOutput(cli.Stderr)
	bundlePath := flags.String("bundle", "", "official plugins distribution directory (default: locate relative to the executable)")
	apply := flags.Bool("apply", false, "legacy flag removed in M2")
	if err := flags.Parse(rest); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return cli.usageError("bundle " + command + " takes no positional arguments")
	}
	switch command {
	case "check":
		if *apply {
			return cli.usageError("bundle check does not accept --apply")
		}
		status, err := home.CheckBundle(ctx, *bundlePath)
		if err == nil {
			err = writeJSON(cli.Stdout, status)
		}
		return cli.result(err)
	case "update":
		if *apply {
			return cli.usageError("bundle update --apply was removed in M2")
		}
		result, err := home.UpdateBundle(ctx, ingothome.BundleUpdateOptions{BundlePath: *bundlePath, Apply: *apply})
		if err == nil {
			err = writeJSON(cli.Stdout, result)
		}
		return cli.result(err)
	default:
		return cli.usageError("unknown bundle subcommand " + strconv.Quote(command))
	}
}

func (cli CLI) runPlugin(ctx context.Context, home *ingothome.Home, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("plugin requires a subcommand")
	}
	command, rest := arguments[0], arguments[1:]
	if remaining, apply, err := extractBoolOption(rest, "apply"); err != nil {
		return cli.usageError(err.Error())
	} else if apply {
		return cli.usageError("plugin --apply was removed in M2")
	} else {
		rest = remaining
	}
	options, rest, err := extractRecipeOptions(rest)
	if err != nil {
		return cli.usageError(err.Error())
	}
	switch command {
	case "list":
		if len(rest) != 0 {
			return cli.usageError("plugin list takes no arguments")
		}
		inspection, err := home.ProjectInspect(options, "")
		if err == nil {
			err = writeJSON(cli.Stdout, inspection.DirectPlugins)
		}
		return cli.result(err)
	case "inspect":
		if len(rest) != 1 {
			return cli.usageError("plugin inspect requires an id or name")
		}
		inspection, err := home.ProjectInspect(options, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, inspection)
		}
		return cli.result(err)
	case "add":
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
			paths, discoverErr := ingothome.DiscoverProject(options)
			if discoverErr != nil {
				return cli.result(discoverErr)
			}
			locator, relativeErr := filepath.Rel(filepath.Dir(paths.Recipe), absolute)
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
			_, err = home.AddProject(ctx, options, plugin, builder.ResolveOptions{})
		}
		return cli.result(err)
	case "remove":
		if len(rest) != 1 {
			return cli.usageError("plugin remove requires an id or name")
		}
		_, err = home.RemoveProject(ctx, options, rest[0], builder.ResolveOptions{})
		return cli.result(err)
	case "update":
		if len(rest) != 1 {
			return cli.usageError("plugin update requires name[@query] or id[@query]")
		}
		token := rest[0]
		reference, query := splitReferenceQuery(token)
		lookup, err := home.LookupProjectPlugin(options, reference)
		if err != nil {
			return cli.result(err)
		}
		if query == "" {
			query = "latest"
		}
		moduleID, version, err := home.ResolveModuleQuery(ctx, lookup.Plugin.Module+"@"+query)
		if err == nil {
			_, err = home.UpdateProject(ctx, options, reference, builder.DesiredPlugin{Module: moduleID, Version: version}, builder.ResolveOptions{})
		}
		return cli.result(err)
	case "reorder":
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
		_, err = home.ReorderProject(ctx, options, rest[0], anchor, isBefore, builder.ResolveOptions{})
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
	_, _ = fmt.Fprintln(cli.Stdout, "usage: ingot [--home PATH] <init|project init|resolve|build|status|inspect|image ...|runtime ...|run|ps|stop|gc|bundle ...|plugin ...>")
}
