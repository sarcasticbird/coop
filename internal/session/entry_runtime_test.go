package session

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/runtime"
)

func TestRunConvertsLifecycleSignalToCancellation(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.InteractiveFunc = func(ctx context.Context, _, _ string, _ []string) error {
		if ctx.Done() == nil {
			return errors.New("interactive entry received a non-cancelable context")
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Second):
			return errors.New("interactive entry did not relay the lifecycle signal")
		}
	}

	err := s.Run(s.Project, []string{"agent"}, nil)
	var signalErr *runtime.SignalError
	if !errors.As(err, &signalErr) || signalErr.ExitCode() != 143 {
		t.Fatalf("signal error = %v, want exit 143", err)
	}
}

func TestRunPreservesBareGuestExitWithoutCleanupErrors(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	m.InteractiveErr = &runtime.ExitError{Code: 23}

	err := s.Run(s.Project, []string{"agent"}, nil)
	if _, ok := err.(*runtime.ExitError); !ok {
		t.Fatalf("lone guest exit was wrapped: %T (%v)", err, err)
	}
}
