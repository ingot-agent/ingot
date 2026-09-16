package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	ingothome "github.com/ingot-agent/ingot/internal/home"
	"github.com/spf13/cobra"
)

func (app *application) newImageCommand() *cobra.Command {
	command := &cobra.Command{Use: "image", Short: "Manage immutable Runtime Images", GroupID: "resources"}
	command.AddCommand(
		app.newImageListCommand(), app.newImageShowCommand(), app.newImageVerifyCommand(),
		app.newImageTagCommand(), app.newImageUntagCommand(), app.newImagePinCommand(true), app.newImagePinCommand(false),
		app.newImageRemoveCommand(), app.newImageExportCommand(), app.newImageImportCommand(),
	)
	return command
}

func (app *application) newImageListCommand() *cobra.Command {
	return &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List Images", Args: exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			views, err := home.ImageList(command.Context())
			if err != nil {
				return err
			}
			return app.output(views, func(writer io.Writer) error { return writeImageTable(writer, views) })
		},
	}
}

func (app *application) newImageShowCommand() *cobra.Command {
	command := &cobra.Command{Use: "show <reference>", Aliases: []string{"inspect"}, Short: "Show an Image", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		views, err := home.ImageInspect(command.Context(), args[0])
		if err != nil {
			return err
		}
		return app.output(views, func(writer io.Writer) error { return writeImageTable(writer, views) })
	}
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImageVerifyCommand() *cobra.Command {
	command := &cobra.Command{Use: "verify <reference>", Short: "Verify Image identity and contents", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		views, err := home.ImageInspect(command.Context(), args[0])
		if err != nil {
			return err
		}
		output := map[string]any{"verified": views}
		return app.output(output, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Verified %d Image variant(s)\n", len(views))
			return err
		})
	}
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImageTagCommand() *cobra.Command {
	command := &cobra.Command{Use: "tag <source> <name:tag>", Short: "Tag an Image", Args: exactArgs(2)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		binding, err := home.ImageTag(command.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		return app.output(binding, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Tagged %s as %s\n", shortDigest(binding.ImageID), args[1])
			return err
		})
	}
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImageUntagCommand() *cobra.Command {
	command := &cobra.Command{Use: "untag <name:tag>", Short: "Remove an Image tag", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		changed, err := home.ImageUntag(command.Context(), args[0])
		if err != nil {
			return err
		}
		output := map[string]any{"untagged": changed, "reference": args[0]}
		return app.output(output, func(writer io.Writer) error {
			if changed {
				_, err := fmt.Fprintf(writer, "Removed tag %s\n", args[0])
				return err
			}
			_, err := fmt.Fprintf(writer, "Tag %s was not present\n", args[0])
			return err
		})
	}
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImagePinCommand(pin bool) *cobra.Command {
	name := "pin"
	description := "Pin an Image for garbage collection"
	if !pin {
		name, description = "unpin", "Remove an Image pin"
	}
	command := &cobra.Command{Use: name + " <reference>", Short: description, Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		imageID, err := home.ImagePin(command.Context(), args[0], pin)
		if err != nil {
			return err
		}
		output := map[string]any{"image_id": imageID, "pinned": pin}
		return app.output(output, func(writer io.Writer) error {
			action := "Pinned"
			if !pin {
				action = "Unpinned"
			}
			_, err := fmt.Fprintf(writer, "%s %s\n", action, shortDigest(imageID))
			return err
		})
	}
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImageRemoveCommand() *cobra.Command {
	command := &cobra.Command{Use: "rm <digest>", Aliases: []string{"remove"}, Short: "Remove an unreferenced Image", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		removed, references, err := home.ImageRemove(command.Context(), args[0])
		if err != nil {
			return err
		}
		output := map[string]any{"image_id": args[0], "removed": removed, "references": references}
		return app.output(output, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Removed %s\n", shortDigest(args[0]))
			return err
		})
	}
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImageExportCommand() *cobra.Command {
	var target, output string
	command := &cobra.Command{Use: "export <reference>", Short: "Export an Image bundle", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		if output == "" {
			return usageErrorf("image export requires --output")
		}
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		if err := home.ImageExport(command.Context(), args[0], target, output); err != nil {
			return err
		}
		result := map[string]any{"output": output}
		return app.output(result, func(writer io.Writer) error {
			_, err := fmt.Fprintf(writer, "Exported %s\n", output)
			return err
		})
	}
	command.Flags().StringVar(&target, "target", "", "target os/arch")
	command.Flags().StringVarP(&output, "output", "o", "", "output bundle path")
	command.ValidArgsFunction = app.completeImageReferences
	return command
}

func (app *application) newImageImportCommand() *cobra.Command {
	var noTag bool
	command := &cobra.Command{Use: "import <bundle>", Short: "Import an Image bundle", Args: exactArgs(1)}
	command.RunE = func(command *cobra.Command, args []string) error {
		home, err := ingothome.Open(app.homePath)
		if err != nil {
			return err
		}
		result, err := home.ImageImport(command.Context(), args[0], noTag)
		if err != nil {
			return err
		}
		return app.output(result, func(writer io.Writer) error {
			action := "Reused"
			if result.Created {
				action = "Imported"
			}
			_, err := fmt.Fprintf(writer, "%s %s\n", action, shortDigest(result.Binding.ImageID))
			return err
		})
	}
	command.Flags().BoolVar(&noTag, "no-tag", false, "do not restore the bundle tag")
	return command
}

func (app *application) newGCCommand() *cobra.Command {
	var keepRecent int
	command := &cobra.Command{
		Use: "gc", Short: "Remove unreferenced Images", GroupID: "maintenance", Args: exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			home, err := ingothome.Open(app.homePath)
			if err != nil {
				return err
			}
			removed, err := home.GC(command.Context(), keepRecent)
			if err != nil {
				return err
			}
			output := map[string]any{"removed": removed}
			return app.output(output, func(writer io.Writer) error {
				_, err := fmt.Fprintf(writer, "Removed %d Image(s)\n", len(removed))
				return err
			})
		},
	}
	command.Flags().IntVar(&keepRecent, "keep-recent", 3, "recent unreferenced Images to retain")
	return command
}

func writeImageTable(writer io.Writer, views []ingothome.ImageView) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "IMAGE\tTARGET\tTAGS\tPINNED")
	for _, view := range views {
		tags := make([]string, len(view.Tags))
		for index, tag := range view.Tags {
			tags[index] = tag.Name + ":" + tag.Tag
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%t\n", shortDigest(view.ImageID), view.Target.Platform(), emptyDash(strings.Join(tags, ",")), view.Pinned)
	}
	return table.Flush()
}
