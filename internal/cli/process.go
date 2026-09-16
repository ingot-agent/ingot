package cli

import (
	"fmt"

	ingothome "github.com/ingot-agent/ingot/internal/home"
	"github.com/spf13/cobra"
)

func (app *application) newSuperviseCommand() *cobra.Command {
	var runtimeName, processID, argv string
	command := &cobra.Command{
		Use:    "supervise",
		Hidden: true,
		Args:   exactArgs(0),
		RunE: func(command *cobra.Command, _ []string) error {
			if runtimeName == "" || processID == "" || argv == "" {
				return usageErrorf("supervise requires --runtime, --process, and --argv")
			}
			home, err := ingothome.OpenForSupervisor(app.homePath)
			if err != nil {
				return err
			}
			code, err := home.SuperviseDetached(command.Context(), runtimeName, processID, argv)
			if err != nil {
				return err
			}
			if code != 0 {
				return commandError{code: code, err: fmt.Errorf("supervisor exited with code %d", code)}
			}
			return nil
		},
	}
	command.Flags().StringVar(&runtimeName, "runtime", "", "Runtime name")
	command.Flags().StringVar(&processID, "process", "", "process ID")
	command.Flags().StringVar(&argv, "argv", "", "encoded argv")
	return command
}
