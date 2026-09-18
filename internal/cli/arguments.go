package cli

import (
	"fmt"
	"os"
	"path/filepath"

	ingothome "github.com/ingot-agent/ingot/internal/home"
	"github.com/spf13/cobra"
)

type projectSelector struct {
	file    string
	lock    string
	profile string
	locked  bool
	tag     string
}

func (selector *projectSelector) addFlags(command *cobra.Command, build bool) {
	command.Flags().StringVarP(&selector.file, "file", "f", "", "plugin recipe path")
	command.Flags().StringVar(&selector.lock, "lock", "", "lock file path")
	command.Flags().StringVar(&selector.profile, "profile", "", "managed profile name")
	if build {
		selector.addLockedFlag(command)
		command.Flags().StringVarP(&selector.tag, "tag", "t", "", "tag the built image")
	}
	_ = command.RegisterFlagCompletionFunc("profile", fixedCompletion("default", "minimal"))
}

func (selector *projectSelector) addLockedFlag(command *cobra.Command) {
	command.Flags().BoolVar(&selector.locked, "locked", false, "require an up-to-date lock")
}

func (selector projectSelector) options(home *ingothome.Home) (ingothome.RecipeOptions, error) {
	if selector.profile != "" && (selector.file != "" || selector.lock != "") {
		return ingothome.RecipeOptions{}, usageErrorf("--profile cannot be combined with --file or --lock")
	}
	use := selector.file
	lock := selector.lock
	if selector.profile != "" {
		use = home.ProfileRecipePath(selector.profile)
	} else if use == "" {
		discovered, err := discoverRecipe("")
		if err != nil {
			return ingothome.RecipeOptions{}, err
		}
		use = discovered
	}
	return ingothome.RecipeOptions{Use: use, Lock: lock, Locked: selector.locked, Tag: selector.tag}, nil
}

func discoverRecipe(cwd string) (string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	directory, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(directory, "plugins.toml")
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return candidate, nil
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return "", fmt.Errorf("INGOT-BUILD-INPUT-RECIPE: no plugins.toml found in the current directory or its parents; run ingot init or pass --file")
}

func splitRuntimeArgv(command *cobra.Command, args []string) ([]string, []string, bool, error) {
	index := command.ArgsLenAtDash()
	if index < 0 {
		if len(args) > 1 {
			return nil, nil, false, usageErrorf("accepts at most one Runtime name before --")
		}
		return args, nil, false, nil
	}
	if index > 1 {
		return nil, nil, false, usageErrorf("accepts at most one Runtime name before --")
	}
	return args[:index], append([]string{}, args[index:]...), true, nil
}

func fixedCompletion(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return append([]string{}, values...), cobra.ShellCompDirectiveNoFileComp
	}
}
