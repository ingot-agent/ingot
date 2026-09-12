package cli

import (
	"context"
	"flag"
	"strconv"

	ingothome "github.com/ingot-agent/ingot/internal/home"
)

func (cli CLI) runImage(ctx context.Context, home *ingothome.Home, arguments []string) int {
	if len(arguments) == 0 {
		return cli.usageError("image requires a subcommand")
	}
	command, rest := arguments[0], arguments[1:]
	switch command {
	case "list":
		if len(rest) != 0 {
			return cli.usageError("image list takes no arguments")
		}
		views, err := home.ImageList(ctx)
		if err == nil {
			err = writeJSON(cli.Stdout, views)
		}
		return cli.result(err)
	case "inspect":
		if len(rest) != 1 {
			return cli.usageError("image inspect requires one reference")
		}
		views, err := home.ImageInspect(ctx, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, views)
		}
		return cli.result(err)
	case "verify":
		if len(rest) != 1 {
			return cli.usageError("image verify requires one reference")
		}
		views, err := home.ImageInspect(ctx, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"verified": views})
		}
		return cli.result(err)
	case "tag":
		if len(rest) != 2 {
			return cli.usageError("image tag requires source and destination name:tag")
		}
		binding, err := home.ImageTag(ctx, rest[0], rest[1])
		if err == nil {
			err = writeJSON(cli.Stdout, binding)
		}
		return cli.result(err)
	case "untag":
		if len(rest) != 1 {
			return cli.usageError("image untag requires name:tag")
		}
		changed, err := home.ImageUntag(ctx, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"untagged": changed, "reference": rest[0]})
		}
		return cli.result(err)
	case "pin", "unpin":
		if len(rest) != 1 {
			return cli.usageError("image " + command + " requires one reference")
		}
		id, err := home.ImagePin(ctx, rest[0], command == "pin")
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"image_id": id, "pinned": command == "pin"})
		}
		return cli.result(err)
	case "remove":
		if len(rest) != 1 {
			return cli.usageError("image remove requires one digest")
		}
		removed, refs, err := home.ImageRemove(ctx, rest[0])
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"image_id": rest[0], "removed": removed, "references": refs})
		}
		return cli.result(err)
	case "export":
		flags := flag.NewFlagSet("image export", flag.ContinueOnError)
		flags.SetOutput(cli.Stderr)
		target := flags.String("target", "", "goos/goarch")
		output := flags.String("output", "", "output path")
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() != 1 || *output == "" {
			return cli.usageError("image export requires one reference and --output")
		}
		err := home.ImageExport(ctx, flags.Arg(0), *target, *output)
		if err == nil {
			err = writeJSON(cli.Stdout, map[string]any{"output": *output})
		}
		return cli.result(err)
	case "import":
		flags := flag.NewFlagSet("image import", flag.ContinueOnError)
		flags.SetOutput(cli.Stderr)
		noTag := flags.Bool("no-tag", false, "do not restore bundle tag")
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() != 1 {
			return cli.usageError("image import requires one path")
		}
		result, err := home.ImageImport(ctx, flags.Arg(0), *noTag)
		if err == nil {
			err = writeJSON(cli.Stdout, result)
		}
		return cli.result(err)
	default:
		return cli.usageError("unknown image subcommand " + strconv.Quote(command))
	}
}
