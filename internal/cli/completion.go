package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	ingothome "github.com/ingot-agent/ingot/internal/home"
	"github.com/ingot-agent/ingot/internal/image"
	"github.com/ingot-agent/ingot/internal/managedruntime"
	"github.com/spf13/cobra"
)

func (app *application) completeRuntimeNames(_ bool) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		root, err := app.completionHomeRoot()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		entries, err := managedruntime.New(filepath.Join(root, "runtimes")).List()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		result := make([]string, 0, len(entries))
		for _, entry := range entries {
			description := "Runtime"
			if entry.Name == defaultRuntimeName {
				description = "default Runtime"
			}
			result = append(result, entry.Name+"\t"+description)
		}
		return result, cobra.ShellCompDirectiveNoFileComp
	}
}

func (app *application) completeImageReferences(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return app.imageReferenceCandidates(), cobra.ShellCompDirectiveNoFileComp
}

func (app *application) completeRunArguments(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 1 {
		return app.imageReferenceCandidates(), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func (app *application) completeRuntimeThenImage(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return app.completeRuntimeNames(false)(command, args, toComplete)
	}
	if len(args) == 1 {
		return app.imageReferenceCandidates(), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func (app *application) imageReferenceCandidates() []string {
	root, err := app.completionHomeRoot()
	if err != nil {
		return nil
	}
	catalog, err := image.LoadCatalog(filepath.Join(root, "images", "catalog.json"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	result := []string{}
	for _, entry := range catalog.Tags {
		value := entry.Name + ":" + entry.Tag
		if !seen[value] {
			seen[value] = true
			result = append(result, value+"\tImage tag")
		}
	}
	for _, pin := range catalog.Pins {
		if !seen[pin.ImageID] {
			seen[pin.ImageID] = true
			result = append(result, pin.ImageID+"\tPinned Image")
		}
	}
	sort.Strings(result)
	return result
}

func (app *application) completePluginReferences(selector projectSelector) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		root, err := app.completionHomeRoot()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		options, err := selector.options(&ingothome.Home{Root: root})
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		lockPath := options.Lock
		if lockPath == "" {
			if strings.HasSuffix(strings.ToLower(options.Use), ".toml") {
				lockPath = strings.TrimSuffix(options.Use, filepath.Ext(options.Use)) + ".lock"
			} else {
				lockPath = options.Use + ".lock"
			}
		}
		lock, err := builder.ParseLock(lockPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		result := make([]string, 0, len(lock.Plugins)*2)
		for _, plugin := range lock.Plugins {
			result = append(result, plugin.Name+"\tPlugin", plugin.ID+"\tPlugin module")
		}
		sort.Strings(result)
		return result, cobra.ShellCompDirectiveNoFileComp
	}
}

func (app *application) completionHomeRoot() (string, error) {
	root := app.homePath
	if root == "" {
		root = os.Getenv("INGOT_HOME")
	}
	if root == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(userHome, ".ingot")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}
