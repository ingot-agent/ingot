package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/collection"
	ingothome "github.com/ingot-agent/ingot/internal/home"
	"github.com/ingot-agent/ingot/internal/image"
	processmgr "github.com/ingot-agent/ingot/internal/process"
	"github.com/spf13/cobra"
)

type initCommandResult struct {
	Setup   ingothome.InitResult        `json:"setup"`
	Project ingothome.ProjectInitResult `json:"project"`
}

type buildCommandResult struct {
	Recipe         string                `json:"recipe"`
	Lock           string                `json:"lock"`
	ImageID        string                `json:"image_id"`
	ArtifactDigest string                `json:"artifact_digest"`
	Target         image.Target          `json:"target"`
	Runtime        ingothome.RuntimeView `json:"runtime"`
	RuntimeCreated bool                  `json:"runtime_created"`
	BindingChanged bool                  `json:"binding_changed"`
}

type generateCommandResult struct {
	Recipe          string       `json:"recipe"`
	Lock            string       `json:"lock"`
	SourceDirectory string       `json:"source_directory"`
	BuildManifest   string       `json:"build_manifest"`
	ExpectedImageID string       `json:"expected_image_id"`
	Target          image.Target `json:"target"`
}

type upCommandResult struct {
	Build   buildCommandResult    `json:"build"`
	Runtime ingothome.RuntimeView `json:"runtime"`
	Process *processmgr.Record    `json:"process"`
}

func (app *application) newSetupCommand() *cobra.Command {
	var profile string
	var force bool
	command := &cobra.Command{
		Use:     "setup",
		Short:   "Initialize or refresh the managed Home",
		GroupID: "common",
		Args:    exactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			home, err := ingothome.OpenForInit(app.homePath)
			if err != nil {
				return err
			}
			result, err := home.Init(ingothome.InitOptions{Profile: profile, Force: force})
			if err != nil {
				return err
			}
			return app.output(result, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Home ready: %s\nProfile: %s\nRecipe: %s\n", result.Home, result.Profile, result.ProfileRecipePath)
				return err
			})
		},
	}
	command.Flags().StringVar(&profile, "profile", "default", "official plugin profile")
	command.Flags().BoolVar(&force, "force", false, "rewrite managed configuration")
	_ = command.RegisterFlagCompletionFunc("profile", fixedCompletion("default", "minimal"))
	return command
}

func (app *application) newInitCommand() *cobra.Command {
	var profile string
	var force bool
	command := &cobra.Command{
		Use:     "init [directory]",
		Short:   "Initialize an ingot project",
		GroupID: "common",
		Args:    rangeArgs(0, 1),
		RunE: func(command *cobra.Command, args []string) error {
			directory := "."
			if len(args) == 1 {
				directory = args[0]
			}
			home, err := ingothome.OpenForInit(app.homePath)
			if err != nil {
				return err
			}
			setup, err := home.Init(ingothome.InitOptions{Profile: profile})
			if err != nil {
				return err
			}
			project, err := home.InitProject(command.Context(), ingothome.ProjectInitOptions{Directory: directory, Profile: profile, Force: force})
			if err != nil {
				return err
			}
			result := initCommandResult{Setup: setup, Project: project}
			return app.output(result, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Project initialized: %s\nRecipe: %s\nProfile: %s\n", project.Project, project.PluginsPath, project.Profile)
				return err
			})
		},
	}
	command.Flags().StringVar(&profile, "profile", "default", "official plugin profile")
	command.Flags().BoolVar(&force, "force", false, "overwrite an existing project recipe")
	_ = command.RegisterFlagCompletionFunc("profile", fixedCompletion("default", "minimal"))
	command.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return command
}

func (app *application) newBuildCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{
		Use:     "build [runtime]",
		Short:   "Build the project and bind a Runtime",
		GroupID: "common",
		Args:    rangeArgs(0, 1),
		RunE: func(command *cobra.Command, args []string) error {
			name := optionalRuntimeName(args)
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			options, err := selector.options(home)
			if err != nil {
				return err
			}
			result, err := home.BuildProjectForRuntime(command.Context(), options, builder.ResolveOptions{}, name)
			if err != nil {
				return err
			}
			output := projectBuildOutput(result)
			return app.output(output, func(writer io.Writer) error { return writeBuildSummary(writer, output) })
		},
	}
	selector.addFlags(command, true)
	command.ValidArgsFunction = app.completeRuntimeNames(true)
	return command
}

func (app *application) newUpCommand() *cobra.Command {
	selector := projectSelector{}
	var detach bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "up [runtime] [-- argv...]",
		Short:   "Build, bind, and restart a Runtime",
		GroupID: "common",
		RunE: func(command *cobra.Command, args []string) error {
			before, argv, hasArgv, err := splitRuntimeArgv(command, args)
			if err != nil {
				return err
			}
			name := defaultRuntimeName
			if len(before) == 1 {
				name = before[0]
			}
			if !detach {
				if err := app.rejectJSON("up"); err != nil {
					return err
				}
			}
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			options, err := selector.options(home)
			if err != nil {
				return err
			}
			built, err := home.BuildProjectForRuntime(command.Context(), options, builder.ResolveOptions{}, name)
			if err != nil {
				return err
			}
			if hasArgv {
				_, err = home.RuntimeCommand(command.Context(), name, argv)
				if err != nil {
					return err
				}
			}
			if err := home.RuntimeStop(command.Context(), name, "", timeout); err != nil {
				return err
			}
			buildOutput := projectBuildOutput(built)
			if detach {
				record, err := home.RuntimeStart(command.Context(), name, nil, timeout)
				if err != nil {
					return err
				}
				view, err := home.RuntimeInspect(command.Context(), name)
				if err != nil {
					return err
				}
				output := upCommandResult{Build: buildOutput, Runtime: view, Process: record}
				return app.output(output, func(writer io.Writer) error {
					if err := writeBuildSummary(writer, buildOutput); err != nil {
						return err
					}
					_, err := fmt.Fprintf(writer, "Started %s in the background (process %s)\n", name, record.ProcessID)
					return err
				})
			}
			if err := writeBuildSummary(app.stderr, buildOutput); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(app.stderr, "Starting %s in the foreground\n", name); err != nil {
				return err
			}
			code, err := home.RuntimeRun(command.Context(), name, nil, app.stdin, app.stdout, app.stderr)
			if err != nil {
				return err
			}
			if code != 0 {
				return runtimeExit(code)
			}
			return nil
		},
	}
	selector.addFlags(command, true)
	command.Flags().BoolVarP(&detach, "detach", "d", false, "run in the background")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "stop and startup timeout")
	command.ValidArgsFunction = app.completeRuntimeNames(true)
	return command
}

func projectBuildOutput(result ingothome.ProjectBuildResult) buildCommandResult {
	return buildCommandResult{
		Recipe: result.Paths.Recipe, Lock: result.Paths.Lock,
		ImageID: result.Build.ImageID, ArtifactDigest: result.Build.ArtifactDigest, Target: result.Build.Target,
		Runtime: *result.Runtime, RuntimeCreated: result.RuntimeCreated, BindingChanged: result.BindingChanged,
	}
}

func writeBuildSummary(writer io.Writer, result buildCommandResult) error {
	changed := "unchanged"
	if result.RuntimeCreated {
		changed = "created"
	} else if result.BindingChanged {
		changed = "updated"
	}
	_, err := fmt.Fprintf(writer, "Built %s (%s)\nRuntime %s: %s", shortDigest(result.ImageID), result.Target.Platform(), result.Runtime.Name, changed)
	if result.Runtime.RestartRequired {
		_, _ = fmt.Fprint(writer, " (restart required)")
	}
	_, _ = fmt.Fprintln(writer)
	return err
}

func (app *application) newProjectCommand() *cobra.Command {
	command := &cobra.Command{Use: "project", Short: "Inspect, resolve, and generate a project", GroupID: "project"}
	command.AddCommand(app.newProjectStatusCommand(), app.newProjectShowCommand(), app.newProjectResolveCommand(), app.newProjectGenerateCommand())
	return command
}

func (app *application) newProjectGenerateCommand() *cobra.Command {
	selector := projectSelector{}
	var outputDirectory string
	command := &cobra.Command{
		Use:   "generate",
		Short: "Generate runtime source without compiling it",
		Args:  exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			if outputDirectory == "" {
				return usageErrorf("project generate requires --output")
			}
			home, options, err := app.projectHome(selector)
			if err != nil {
				return err
			}
			result, err := home.GenerateProject(command.Context(), options, builder.ResolveOptions{}, outputDirectory)
			if err != nil {
				return err
			}
			output := generateCommandResult{
				Recipe: result.Paths.Recipe, Lock: result.Paths.Lock,
				SourceDirectory: result.Export.SourceDirectory, BuildManifest: result.Export.BuildManifest,
				ExpectedImageID: result.Export.ExpectedImageID, Target: result.Export.Target,
			}
			return app.output(output, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Generated runtime source: %s\nBuild manifest: %s\nExpected image: %s (%s)\n", output.SourceDirectory, output.BuildManifest, shortDigest(output.ExpectedImageID), output.Target.Platform())
				return err
			})
		},
	}
	selector.addFlags(command, false)
	selector.addLockedFlag(command)
	command.Flags().StringVarP(&outputDirectory, "output", "o", "", "generated runtime source directory")
	command.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return command
}

func (app *application) newProjectStatusCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{
		Use:   "status",
		Short: "Show desired, locked, and built state",
		Args:  exactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			home, options, err := app.projectHome(selector)
			if err != nil {
				return err
			}
			status, err := home.ProjectStatus(options)
			if err != nil {
				return err
			}
			return app.output(status, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Recipe: %s\nLock: %s\nDesired locked: %t\nSources locked: %t\nBuilt: %t\nImage: %s\n", status.Recipe, status.Lock, status.DesiredLocked, status.LockedSources, status.Built, emptyDash(shortDigest(status.LockedImageID)))
				return err
			})
		},
	}
	selector.addFlags(command, false)
	return command
}

func (app *application) newProjectShowCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{
		Use:   "show",
		Short: "Show the resolved component graph",
		Args:  exactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			home, options, err := app.projectHome(selector)
			if err != nil {
				return err
			}
			inspection, err := home.ProjectInspect(options, "")
			if err != nil {
				return err
			}
			return app.output(inspection, func(writer io.Writer) error { return writeInspection(writer, inspection) })
		},
	}
	selector.addFlags(command, false)
	return command
}

func (app *application) newProjectResolveCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve plugins and refresh the lock",
		Args:  exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			home, options, err := app.projectHome(selector)
			if err != nil {
				return err
			}
			lock, paths, err := home.ResolveProject(command.Context(), options, builder.ResolveOptions{})
			if err != nil {
				return err
			}
			imageID, _ := lock.ImageID()
			output := map[string]any{"recipe": paths.Recipe, "lock": paths.Lock, "image_id": imageID}
			return app.output(output, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Resolved %s\nLock: %s\nImage: %s\n", paths.Recipe, paths.Lock, shortDigest(imageID))
				return err
			})
		},
	}
	selector.addFlags(command, false)
	return command
}

func (app *application) projectHome(selector projectSelector) (*ingothome.Home, ingothome.RecipeOptions, error) {
	home, err := ingothome.Open(app.homePath)
	if err != nil {
		return nil, ingothome.RecipeOptions{}, err
	}
	options, err := selector.options(home)
	return home, options, err
}

func (app *application) newPluginCommand() *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Manage project plugins", GroupID: "project"}
	command.AddCommand(
		app.newPluginListCommand(), app.newPluginShowCommand(), app.newPluginAddCommand(),
		app.newPluginRemoveCommand(), app.newPluginUpdateCommand(), app.newPluginMoveCommand(),
	)
	return command
}

func (app *application) newPluginListCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List direct plugins", Args: exactArgs(0)}
	command.RunE = func(_ *cobra.Command, _ []string) error {
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		inspection, err := home.ProjectInspect(options, "")
		if err != nil {
			return err
		}
		return app.output(inspection.DirectPlugins, func(writer io.Writer) error { return writePluginTable(writer, inspection.DirectPlugins) })
	}
	selector.addFlags(command, false)
	return command
}

func (app *application) newPluginShowCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{Use: "show <plugin>", Aliases: []string{"inspect"}, Short: "Show one plugin", Args: exactArgs(1)}
	command.RunE = func(_ *cobra.Command, args []string) error {
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		inspection, err := home.ProjectInspect(options, args[0])
		if err != nil {
			return err
		}
		return app.output(inspection, func(writer io.Writer) error { return writeInspection(writer, inspection) })
	}
	selector.addFlags(command, false)
	command.ValidArgsFunction = app.completePluginReferences(selector)
	return command
}

func (app *application) newPluginAddCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{Use: "add <module[@query]|path>", Short: "Add a plugin", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		token := args[0]
		var plugin builder.DesiredPlugin
		if isLocalPluginPath(token) {
			absolute, err := filepath.Abs(token)
			if err != nil {
				return err
			}
			moduleID, err := builder.ModuleIdentity(filepath.Join(absolute, "go.mod"))
			if err != nil {
				return err
			}
			paths, err := ingothome.DiscoverProject(options)
			if err != nil {
				return err
			}
			locator, err := filepath.Rel(filepath.Dir(paths.Recipe), absolute)
			if err != nil {
				locator = absolute
			}
			plugin = builder.DesiredPlugin{Module: moduleID, Path: filepath.ToSlash(locator)}
		} else {
			moduleID, version, err := home.ResolveModuleQuery(command.Context(), token)
			if err != nil {
				return err
			}
			plugin = builder.DesiredPlugin{Module: moduleID, Version: version}
		}
		paths, err := home.AddProject(command.Context(), options, plugin, builder.ResolveOptions{})
		if err != nil {
			return err
		}
		return app.output(paths, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Added %s\nRecipe: %s\n", plugin.Module, paths.Recipe)
			return err
		})
	}
	selector.addFlags(command, false)
	return command
}

func (app *application) newPluginRemoveCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{Use: "rm <plugin>", Aliases: []string{"remove"}, Short: "Remove a plugin", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		paths, err := home.RemoveProject(command.Context(), options, args[0], builder.ResolveOptions{})
		if err != nil {
			return err
		}
		return app.output(paths, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Removed %s\n", args[0])
			return err
		})
	}
	selector.addFlags(command, false)
	command.ValidArgsFunction = app.completePluginReferences(selector)
	return command
}

func (app *application) newPluginUpdateCommand() *cobra.Command {
	selector := projectSelector{}
	command := &cobra.Command{Use: "update <plugin[@query]>", Short: "Update a plugin", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		reference, query := splitReferenceQuery(args[0])
		lookup, err := home.LookupProjectPlugin(options, reference)
		if err != nil {
			return err
		}
		if query == "" {
			query = "latest"
		}
		moduleID, version, err := home.ResolveModuleQuery(command.Context(), lookup.Plugin.Module+"@"+query)
		if err != nil {
			return err
		}
		paths, err := home.UpdateProject(command.Context(), options, reference, builder.DesiredPlugin{Module: moduleID, Version: version}, builder.ResolveOptions{})
		if err != nil {
			return err
		}
		return app.output(paths, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Updated %s to %s\n", reference, version)
			return err
		})
	}
	selector.addFlags(command, false)
	command.ValidArgsFunction = app.completePluginReferences(selector)
	return command
}

func (app *application) newPluginMoveCommand() *cobra.Command {
	selector := projectSelector{}
	var before, after string
	command := &cobra.Command{Use: "move <plugin>", Aliases: []string{"reorder"}, Short: "Move a plugin in composition order", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		if (before == "") == (after == "") {
			return usageErrorf("plugin move requires exactly one of --before or --after")
		}
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		anchor, isBefore := before, true
		if after != "" {
			anchor, isBefore = after, false
		}
		paths, err := home.ReorderProject(command.Context(), options, args[0], anchor, isBefore, builder.ResolveOptions{})
		if err != nil {
			return err
		}
		return app.output(paths, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Moved %s\n", args[0])
			return err
		})
	}
	selector.addFlags(command, false)
	command.Flags().StringVar(&before, "before", "", "move before this plugin")
	command.Flags().StringVar(&after, "after", "", "move after this plugin")
	command.ValidArgsFunction = app.completePluginReferences(selector)
	return command
}

func isLocalPluginPath(value string) bool {
	return filepath.IsAbs(value) || strings.HasPrefix(value, "."+string(filepath.Separator)) || strings.HasPrefix(value, ".."+string(filepath.Separator)) || value == "." || value == ".."
}

func splitReferenceQuery(value string) (string, string) {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return value, ""
	}
	return value[:index], value[index+1:]
}

func writeInspection(writer io.Writer, inspection ingothome.Inspection) error {
	if err := writePluginTable(writer, inspection.DirectPlugins); err != nil {
		return err
	}
	if len(inspection.ComponentCreationOrder) > 0 {
		_, _ = fmt.Fprintf(writer, "\nComponent creation order:\n  %s\n", strings.Join(inspection.ComponentCreationOrder, " -> "))
	}
	return nil
}

func writePluginTable(writer io.Writer, plugins []ingothome.PluginInspection) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "NAME\tMODULE\tVERSION\tSOURCE\tCOMPONENTS")
	for _, plugin := range plugins {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\n", plugin.Name, plugin.ID, emptyDash(plugin.Version), plugin.SourceKind, len(plugin.Components))
	}
	return table.Flush()
}

func (app *application) newCollectionCommand() *cobra.Command {
	command := &cobra.Command{Use: "collection", Short: "Inspect and apply plugin collections", GroupID: "project"}
	command.AddCommand(app.newCollectionInspectCommand(), app.newCollectionPlanCommand(), app.newCollectionApplyCommand())
	return command
}

func (app *application) newCollectionInspectCommand() *cobra.Command {
	var digest string
	command := &cobra.Command{Use: "inspect <path-or-https-url>", Short: "Inspect a collection", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		loaded, err := (collection.Loader{}).Load(command.Context(), args[0], digest)
		if err != nil {
			return err
		}
		return app.output(loaded, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "%s %s\nID: %s\nPlugins: %d\nDigest: %s\n", loaded.Collection.Metadata.Name, loaded.Collection.Version, loaded.Collection.ID, len(loaded.Collection.Plugins), loaded.Digest)
			return err
		})
	}
	command.Flags().StringVar(&digest, "expect-digest", "", "required collection digest")
	return command
}

func (app *application) newCollectionPlanCommand() *cobra.Command {
	selector := projectSelector{}
	var digest string
	var acceptOrder bool
	command := &cobra.Command{Use: "plan <source>", Short: "Plan collection changes", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		loaded, err := (collection.Loader{}).Load(command.Context(), args[0], digest)
		if err != nil {
			return err
		}
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		plan, paths, err := home.PlanCollection(command.Context(), options, loaded, collection.PlanOptions{AcceptOrder: acceptOrder})
		if err != nil {
			return err
		}
		output := struct {
			Recipe string           `json:"recipe"`
			Lock   string           `json:"lock"`
			Plan   *collection.Plan `json:"plan"`
		}{Recipe: paths.Recipe, Lock: paths.Lock, Plan: plan}
		return app.output(output, func(writer io.Writer) error { return writeCollectionPlan(writer, plan) })
	}
	selector.addFlags(command, false)
	command.Flags().StringVar(&digest, "expect-digest", "", "required collection digest")
	command.Flags().BoolVar(&acceptOrder, "accept-order", false, "accept the planned minimal reorder")
	return command
}

func (app *application) newCollectionApplyCommand() *cobra.Command {
	selector := projectSelector{}
	var digest string
	var acceptOrder bool
	command := &cobra.Command{Use: "apply <source>", Short: "Apply a collection", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		loaded, err := (collection.Loader{}).Load(command.Context(), args[0], digest)
		if err != nil {
			return err
		}
		home, options, err := app.projectHome(selector)
		if err != nil {
			return err
		}
		result, err := home.ApplyCollection(command.Context(), options, loaded, collection.PlanOptions{AcceptOrder: acceptOrder}, builder.ResolveOptions{})
		if err != nil {
			return err
		}
		return app.output(result, func(writer io.Writer) error {
			if err := writeCollectionPlan(writer, result.Plan); err != nil {
				return err
			}
			_, err := fmt.Fprintf(writer, "Applied to %s\n", result.Recipe)
			return err
		})
	}
	selector.addFlags(command, false)
	command.Flags().StringVar(&digest, "expect-digest", "", "required collection digest")
	command.Flags().BoolVar(&acceptOrder, "accept-order", false, "accept the planned minimal reorder")
	return command
}

func writeCollectionPlan(writer io.Writer, plan *collection.Plan) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "PLUGIN\tVERSION\tSTATUS")
	for _, entry := range plan.Entries {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", entry.Module, entry.RequestedVersion, entry.Status)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "Applicable: %t\nChanged: %t\nOrder: %s\n", plan.Applicable, plan.Changed, plan.Order.Status)
	return err
}

func shortDigest(value string) string {
	if len(value) > len("sha256:")+12 {
		return value[:len("sha256:")+12]
	}
	return value
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
