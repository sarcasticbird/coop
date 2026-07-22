package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sarcasticbird/coop/internal/runtime"
)

func execute(cmd *cobra.Command) int {
	err := cmd.Execute()
	if err == nil {
		return 0
	}

	if _, bareGuestExit := err.(*runtime.ExitError); !bareGuestExit {
		fmt.Fprintln(os.Stderr, "coop:", err)
	}

	var guestExit *runtime.ExitError
	if errors.As(err, &guestExit) {
		return guestExit.ExitCode()
	}
	var signalExit *runtime.SignalError
	if errors.As(err, &signalExit) {
		return signalExit.ExitCode()
	}
	return 1
}
