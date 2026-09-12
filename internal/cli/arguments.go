package cli

import ingothome "github.com/ingot-agent/ingot/internal/home"

func splitDashDash(arguments []string) ([]string, []string) {
	for i, argument := range arguments {
		if argument == "--" {
			return arguments[:i], append([]string{}, arguments[i+1:]...)
		}
	}
	return arguments, nil
}

func extractRecipeOptions(arguments []string) (ingothome.RecipeOptions, []string, error) {
	remaining, use, _, err := extractStringOption(arguments, "use")
	if err != nil {
		return ingothome.RecipeOptions{}, nil, err
	}
	remaining, lock, _, err := extractStringOption(remaining, "lock")
	if err != nil {
		return ingothome.RecipeOptions{}, nil, err
	}
	return ingothome.RecipeOptions{Use: use, Lock: lock}, remaining, nil
}
