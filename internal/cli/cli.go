package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/buildinfo"
	"github.com/ingot-agent/ingot/internal/coreupdate"
	"github.com/spf13/cobra"
)

const defaultRuntimeName = "default"

type CLI struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	updateCore func(context.Context, coreupdate.Options) (coreupdate.Result, error)
}

type application struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	homePath   string
	jsonOutput bool
	updateCore func(context.Context, coreupdate.Options) (coreupdate.Result, error)
}

type commandError struct {
	code int
	err  error
}

func (err commandError) Error() string {
	if err.err == nil {
		return ""
	}
	return err.err.Error()
}

func (err commandError) Unwrap() error { return err.err }

func usageErrorf(format string, arguments ...any) error {
	return commandError{code: 2, err: fmt.Errorf(format, arguments...)}
}

func runtimeExit(code int) error { return commandError{code: code} }

func (cli CLI) Run(ctx context.Context, arguments []string) int {
	app := &application{stdin: cli.Stdin, stdout: cli.Stdout, stderr: cli.Stderr, updateCore: cli.updateCore}
	if app.stdin == nil {
		app.stdin = os.Stdin
	}
	if app.stdout == nil {
		app.stdout = os.Stdout
	}
	if app.stderr == nil {
		app.stderr = os.Stderr
	}
	root := app.rootCommand()
	root.SetArgs(arguments)
	_, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	var commandErr commandError
	if errors.As(err, &commandErr) {
		if commandErr.err != nil {
			_, _ = fmt.Fprintln(app.stderr, commandErr.err)
		}
		return commandErr.code
	}
	if strings.HasPrefix(err.Error(), "unknown command") {
		_, _ = fmt.Fprintln(app.stderr, err)
		return 2
	}
	_, _ = fmt.Fprintln(app.stderr, err)
	return 1
}

func (app *application) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "ingot",
		Short:         "Build immutable agent images and run them",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       buildinfo.Current().CoreVersion,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	root.SetIn(app.stdin)
	root.SetOut(app.stdout)
	root.SetErr(app.stderr)
	root.SetVersionTemplate("ingot {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return commandError{code: 2, err: err}
	})
	root.PersistentFlags().StringVar(&app.homePath, "home", "", "managed Home path")
	root.PersistentFlags().BoolVar(&app.jsonOutput, "json", false, "write stable JSON output")
	root.AddGroup(
		&cobra.Group{ID: "common", Title: "Common Commands:"},
		&cobra.Group{ID: "project", Title: "Project Commands:"},
		&cobra.Group{ID: "resources", Title: "Resource Commands:"},
		&cobra.Group{ID: "maintenance", Title: "Maintenance Commands:"},
	)
	root.SetHelpCommandGroupID("maintenance")
	root.SetCompletionCommandGroupID("maintenance")
	root.AddCommand(
		app.newSetupCommand(),
		app.newInitCommand(),
		app.newBuildCommand(),
		app.newUpCommand(),
		app.newRunCommand(),
		app.newStartCommand(),
		app.newStopCommand(),
		app.newRestartCommand(),
		app.newLogsCommand(),
		app.newPSCommand(),
		app.newProjectCommand(),
		app.newPluginCommand(),
		app.newCollectionCommand(),
		app.newImageCommand(),
		app.newRuntimeCommand(),
		app.newGCCommand(),
		app.newUpdateCommand(),
		app.newVersionCommand(),
		app.newSuperviseCommand(),
	)
	return root
}

func (app *application) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Show core and builder versions",
		GroupID: "maintenance",
		Args:    exactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			result := versionResult()
			executable, _ := os.Executable()
			if versionJSONOutput(app.jsonOutput, executable) {
				return app.writeJSON(result)
			}
			return app.output(result, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Core: %s\nIngot ABI: %s\nBuilder: %s\nTarget: %s\n", result.CoreVersion, result.IngotVersion, result.BuilderVersion, result.Target)
				return err
			})
		},
	}
}

func versionJSONOutput(explicit bool, executable string) bool {
	return explicit || coreupdate.IsStagedCandidatePath(executable)
}

type coreVersionResult struct {
	buildinfo.Info
	IngotVersion   string `json:"ingot_version"`
	BuilderVersion string `json:"builder_version"`
}

func versionResult() coreVersionResult {
	return coreVersionResult{
		Info:           buildinfo.Current(),
		IngotVersion:   builder.DefaultIngotVersion,
		BuilderVersion: builder.DefaultBuilderVersion,
	}
}

func (app *application) newUpdateCommand() *cobra.Command {
	var check, force bool
	var version string
	command := &cobra.Command{
		Use:     "update",
		Short:   "Check for or install a core update",
		GroupID: "maintenance",
		Args:    exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			if check && force {
				return usageErrorf("update --check does not accept --force")
			}
			update := app.updateCore
			if update == nil {
				update = coreupdate.New().Run
			}
			result, err := update(command.Context(), coreupdate.Options{Check: check, Version: version, Force: force})
			if err != nil {
				return err
			}
			return app.output(result, func(writer io.Writer) error {
				if result.UpdateAvailable {
					_, err := fmt.Fprintf(writer, "Core %s is available (current %s)\n", result.TargetVersion, result.CurrentVersion)
					return err
				}
				_, err := fmt.Fprintf(writer, "Core is up to date (%s)\n", result.CurrentVersion)
				return err
			})
		},
	}
	command.Flags().BoolVar(&check, "check", false, "check without installing")
	command.Flags().StringVar(&version, "version", "", "exact core version")
	command.Flags().BoolVar(&force, "force", false, "allow reinstalling or downgrading")
	return command
}

func (app *application) output(value any, human func(io.Writer) error) error {
	if app.jsonOutput {
		return app.writeJSON(value)
	}
	return human(app.stdout)
}

func (app *application) writeJSON(value any) error {
	encoder := json.NewEncoder(app.stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (app *application) rejectJSON(command string) error {
	if app.jsonOutput {
		return usageErrorf("%s streams raw output and does not accept --json", command)
	}
	return nil
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != count {
			return usageErrorf("requires exactly %d argument(s)", count)
		}
		return nil
	}
}

func rangeArgs(minimum, maximum int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < minimum || len(args) > maximum {
			return usageErrorf("requires between %d and %d argument(s)", minimum, maximum)
		}
		return nil
	}
}
