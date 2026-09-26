package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	ingothome "github.com/ingot-agent/ingot/internal/home"
	processmgr "github.com/ingot-agent/ingot/internal/process"
	"github.com/spf13/cobra"
)

func (app *application) newRunCommand() *cobra.Command {
	var detach bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "run <runtime> <image> [-- argv...]",
		Short:   "Create and run a named Runtime from an existing Image",
		GroupID: "common",
		RunE: func(command *cobra.Command, args []string) error {
			before, argv, _, err := splitArgsAtDash(command, args, 2, 2)
			if err != nil {
				return err
			}
			if !detach {
				if err := app.rejectJSON("run"); err != nil {
					return err
				}
			}
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			view, err := home.RuntimeCreate(command.Context(), before[0], before[1], argv)
			if err != nil {
				return err
			}
			if detach {
				record, err := home.RuntimeStart(command.Context(), view.Name, nil, timeout)
				if err != nil {
					return err
				}
				output := struct {
					Runtime ingothome.RuntimeView `json:"runtime"`
					Process *processmgr.Record    `json:"process"`
				}{Runtime: view, Process: record}
				return app.output(output, func(writer io.Writer) error {
					_, err := fmt.Fprintf(writer, "Created and started %s (process %s)\n", view.Name, record.ProcessID)
					return err
				})
			}
			_, _ = fmt.Fprintf(app.stderr, "Created %s; starting in the foreground\n", view.Name)
			code, err := home.RuntimeRun(command.Context(), view.Name, nil, app.stdin, app.stdout, app.stderr)
			if err != nil {
				return err
			}
			if code != 0 {
				return runtimeExit(code)
			}
			return nil
		},
	}
	command.Flags().BoolVarP(&detach, "detach", "d", false, "run in the background")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "startup timeout")
	command.ValidArgsFunction = app.completeRunArguments
	return command
}

func (app *application) newStartCommand() *cobra.Command {
	var detach, foreground bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "start [runtime] [-- argv...]",
		Short:   "Start an existing Runtime",
		GroupID: "common",
		RunE: func(command *cobra.Command, args []string) error {
			before, argv, hasArgv, err := splitRuntimeArgv(command, args)
			if err != nil {
				return err
			}
			if detach && foreground {
				return usageErrorf("start --detach does not accept --foreground")
			}
			if !detach {
				if err := app.rejectJSON("start"); err != nil {
					return err
				}
			}
			name := defaultRuntimeName
			if len(before) == 1 {
				name = before[0]
			}
			temporary := []string(nil)
			if hasArgv {
				temporary = argv
			}
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			if !detach {
				code, err := home.RuntimeRun(command.Context(), name, temporary, app.stdin, app.stdout, app.stderr)
				if err != nil {
					return err
				}
				if code != 0 {
					return runtimeExit(code)
				}
				return nil
			}
			record, err := home.RuntimeStart(command.Context(), name, temporary, timeout)
			if err != nil {
				return err
			}
			return app.output(record, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Started %s (process %s)\n", name, record.ProcessID)
				return err
			})
		},
	}
	command.Flags().BoolVarP(&detach, "detach", "d", false, "run in the background and write output to logs")
	command.Flags().BoolVar(&foreground, "foreground", false, "run attached to this terminal")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "startup timeout")
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func (app *application) newStopCommand() *cobra.Command {
	var processID string
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "stop [runtime]",
		Short:   "Gracefully stop a Runtime process",
		GroupID: "common",
		Args:    rangeArgs(0, 1),
		RunE: func(command *cobra.Command, args []string) error {
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			name := defaultRuntimeName
			if len(args) == 1 {
				name = args[0]
			}
			if processID != "" {
				if len(args) != 0 {
					return usageErrorf("stop --process does not accept a Runtime name")
				}
				err = home.StopProcess(command.Context(), processID, timeout)
			} else {
				err = home.RuntimeStop(command.Context(), name, "", timeout)
			}
			if err != nil {
				return err
			}
			output := map[string]any{"stopped": true, "runtime": name, "process_id": processID}
			return app.output(output, func(writer io.Writer) error {
				target := name
				if processID != "" {
					target = processID
				}
				_, err := fmt.Fprintf(writer, "Stopped %s\n", target)
				return err
			})
		},
	}
	command.Flags().StringVar(&processID, "process", "", "stop a specific process ID")
	command.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "shutdown timeout")
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func (app *application) newRestartCommand() *cobra.Command {
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "restart [runtime]",
		Short:   "Restart an existing Runtime in the background",
		GroupID: "common",
		Args:    rangeArgs(0, 1),
		RunE: func(command *cobra.Command, args []string) error {
			name := optionalRuntimeName(args)
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			record, err := home.RuntimeRestart(command.Context(), name, timeout)
			if err != nil {
				return err
			}
			return app.output(record, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Restarted %s (process %s)\n", name, record.ProcessID)
				return err
			})
		},
	}
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "shutdown and startup timeout")
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func (app *application) newLogsCommand() *cobra.Command {
	var processID string
	var follow bool
	command := &cobra.Command{
		Use:     "logs [runtime]",
		Short:   "Print detached Runtime logs",
		GroupID: "common",
		Args:    rangeArgs(0, 1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := app.rejectJSON("logs"); err != nil {
				return err
			}
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			return home.RuntimeLogs(command.Context(), optionalRuntimeName(args), processID, follow, app.stdout)
		},
	}
	command.Flags().StringVar(&processID, "process", "", "select a process log")
	command.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func (app *application) newPSCommand() *cobra.Command {
	var all bool
	command := &cobra.Command{
		Use:     "ps",
		Short:   "List Runtime processes",
		GroupID: "common",
		Args:    exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			var views []ingothome.RuntimeView
			if all {
				views, err = home.RuntimeList(command.Context())
			} else {
				views, err = home.Processes(command.Context())
			}
			if err != nil {
				return err
			}
			return app.output(views, func(writer io.Writer) error { return writeRuntimeTable(writer, views) })
		},
	}
	command.Flags().BoolVarP(&all, "all", "a", false, "include stopped Runtimes")
	return command
}

func (app *application) newRuntimeCommand() *cobra.Command {
	command := &cobra.Command{Use: "runtime", Short: "Manage Runtime bindings and state", GroupID: "resources"}
	command.AddCommand(
		app.newRuntimeListCommand(), app.newRuntimeShowCommand(), app.newRuntimeCreateCommand(),
		app.newRuntimeSwitchCommand(), app.newRuntimeRollbackCommand(), app.newRuntimeCommandCommand(), app.newRuntimeRemoveCommand(),
	)
	return command
}

func (app *application) newRuntimeListCommand() *cobra.Command {
	return &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List all Runtimes", Args: exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			views, err := home.RuntimeList(command.Context())
			if err != nil {
				return err
			}
			return app.output(views, func(writer io.Writer) error { return writeRuntimeTable(writer, views) })
		},
	}
}

func (app *application) newRuntimeShowCommand() *cobra.Command {
	command := &cobra.Command{Use: "show [runtime]", Aliases: []string{"inspect"}, Short: "Show a Runtime", Args: rangeArgs(0, 1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		view, err := home.RuntimeInspect(command.Context(), optionalRuntimeName(args))
		if err != nil {
			return err
		}
		return app.output(view, func(writer io.Writer) error { return writeRuntimeDetail(writer, view) })
	}
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func (app *application) newRuntimeCreateCommand() *cobra.Command {
	command := &cobra.Command{Use: "create <runtime> <image> [-- argv...]", Short: "Create a stopped Runtime"}
	command.RunE = func(command *cobra.Command, args []string) error {
		before, argv, _, err := splitArgsAtDash(command, args, 2, 2)
		if err != nil {
			return err
		}
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		view, err := home.RuntimeCreate(command.Context(), before[0], before[1], argv)
		if err != nil {
			return err
		}
		return app.output(view, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Created %s -> %s\n", view.Name, shortDigest(view.DesiredImage.ImageID))
			return err
		})
	}
	command.ValidArgsFunction = app.completeRunArguments
	return command
}

func (app *application) newRuntimeSwitchCommand() *cobra.Command {
	command := &cobra.Command{Use: "switch <runtime> <image>", Short: "Change a Runtime Image", Args: exactArgs(2)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		view, err := home.RuntimeSwitch(command.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		return app.output(view, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Switched %s -> %s\n", view.Name, shortDigest(view.DesiredImage.ImageID))
			return err
		})
	}
	command.ValidArgsFunction = app.completeRuntimeThenImage
	return command
}

func (app *application) newRuntimeRollbackCommand() *cobra.Command {
	command := &cobra.Command{Use: "rollback [runtime]", Short: "Swap desired and rollback Images", Args: rangeArgs(0, 1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		name := optionalRuntimeName(args)
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		view, err := home.RuntimeRollback(command.Context(), name)
		if err != nil {
			return err
		}
		return app.output(view, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Rolled back %s -> %s\n", name, shortDigest(view.DesiredImage.ImageID))
			return err
		})
	}
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func (app *application) newRuntimeCommandCommand() *cobra.Command {
	command := &cobra.Command{Use: "command", Short: "Set or clear the default Runtime command"}
	set := &cobra.Command{Use: "set [runtime] -- argv...", Short: "Set the default command"}
	set.RunE = func(command *cobra.Command, args []string) error {
		before, argv, hasArgv, err := splitRuntimeArgv(command, args)
		if err != nil {
			return err
		}
		if !hasArgv || len(argv) == 0 {
			return usageErrorf("runtime command set requires argv after --")
		}
		name := optionalRuntimeName(before)
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		view, err := home.RuntimeCommand(command.Context(), name, argv)
		if err != nil {
			return err
		}
		return app.output(view, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Updated command for %s\n", name)
			return err
		})
	}
	set.ValidArgsFunction = app.completeRuntimeNames(false)
	clear := &cobra.Command{Use: "clear [runtime]", Short: "Clear the default command", Args: rangeArgs(0, 1)}
	clear.RunE = func(command *cobra.Command, args []string) error {
		name := optionalRuntimeName(args)
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		view, err := home.RuntimeCommand(command.Context(), name, []string{})
		if err != nil {
			return err
		}
		return app.output(view, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Cleared command for %s\n", name)
			return err
		})
	}
	clear.ValidArgsFunction = app.completeRuntimeNames(false)
	command.AddCommand(set, clear)
	return command
}

func (app *application) newRuntimeRemoveCommand() *cobra.Command {
	var purge bool
	command := &cobra.Command{Use: "rm <runtime>", Aliases: []string{"remove", "delete"}, Short: "Delete a stopped Runtime", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		if err := home.RuntimeDelete(command.Context(), args[0], purge); err != nil {
			return err
		}
		output := map[string]any{"deleted": args[0], "purged": purge}
		return app.output(output, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Deleted %s\n", args[0])
			return err
		})
	}
	command.Flags().BoolVar(&purge, "purge", false, "delete Runtime state and logs")
	command.ValidArgsFunction = app.completeRuntimeNames(false)
	return command
}

func optionalRuntimeName(args []string) string {
	if len(args) == 0 {
		return defaultRuntimeName
	}
	return args[0]
}

func splitArgsAtDash(command *cobra.Command, args []string, minimum, maximum int) ([]string, []string, bool, error) {
	index := command.ArgsLenAtDash()
	if index < 0 {
		if len(args) < minimum || len(args) > maximum {
			return nil, nil, false, usageErrorf("requires between %d and %d argument(s) before --", minimum, maximum)
		}
		return args, nil, false, nil
	}
	if index < minimum || index > maximum {
		return nil, nil, false, usageErrorf("requires between %d and %d argument(s) before --", minimum, maximum)
	}
	return args[:index], append([]string{}, args[index:]...), true, nil
}

func writeRuntimeTable(writer io.Writer, views []ingothome.RuntimeView) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "NAME\tSTATE\tIMAGE\tPROCESS\tRESTART")
	for _, view := range views {
		processID := "-"
		if view.Process != nil {
			processID = view.Process.ProcessID
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%t\n", view.Name, view.State, shortDigest(view.DesiredImage.ImageID), processID, view.RestartRequired)
	}
	return table.Flush()
}

func writeRuntimeDetail(writer io.Writer, view ingothome.RuntimeView) error {
	_, err := fmt.Fprintf(writer, "Name: %s\nState: %s\nImage: %s\nTarget: %s\nGeneration: %d\nRestart required: %t\nCommand: %q\n", view.Name, view.State, view.DesiredImage.ImageID, view.DesiredImage.Target.Platform(), view.Generation, view.RestartRequired, view.DefaultArgv)
	if err != nil {
		return err
	}
	if view.Process != nil {
		_, err = fmt.Fprintf(writer, "Process: %s (%s)\n", view.Process.ProcessID, view.Process.Mode)
	}
	return err
}
