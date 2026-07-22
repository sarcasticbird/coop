package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/runtime"
)

// Run performs one credential-aware interactive entry. Host acquisition is
// deliberately completed before Up can mutate container state.
func (s *Session) Run(cwd string, argv, requestedCredentials []string) (retErr error) {
	ctx, stopSignals := runtime.NotifyContext(context.Background())
	defer stopSignals()
	defer func() {
		retErr = operationError(ctx, retErr)
	}()
	selected, err := credential.Resolve(s.Cfg, requestedCredentials)
	if err != nil {
		return operationError(ctx, err)
	}
	if err := operationError(ctx, nil); err != nil {
		return err
	}
	if s.CredentialManager == nil {
		return errors.New("credential manager is not configured")
	}
	acquired, err := s.CredentialManager.AcquireAll(ctx, s.HostHome, selected)
	if err != nil {
		return operationError(ctx, err)
	}
	revokeCredentials := s.revokeCredentials
	if revokeCredentials == nil {
		revokeCredentials = credential.RevokeAll
	}
	defer func() {
		retErr = combineErrors(retErr, revokeCredentials(context.WithoutCancel(ctx), acquired))
	}()
	if err := operationError(ctx, nil); err != nil {
		return err
	}

	if err := s.UpContext(ctx); err != nil {
		return operationError(ctx, err)
	}
	if err := operationError(ctx, nil); err != nil {
		return err
	}
	if err := credential.Scrub(ctx, s.RT, s.Name); err != nil {
		return operationError(ctx, fmt.Errorf("scrub credential leases: %w", err))
	}
	if err := operationError(ctx, nil); err != nil {
		return err
	}
	for _, summary := range credential.Summaries(acquired) {
		fmt.Fprintf(os.Stderr, "coop: credential %s\n", summary)
	}

	workdir, command := s.entryArgv(cwd, argv)
	if len(acquired) == 0 {
		return operationError(ctx, s.RT.ExecInteractive(ctx, s.Name, workdir, command))
	}

	lease, err := credential.NewGuestLease(rand.Reader)
	if err != nil {
		return operationError(ctx, fmt.Errorf("create credential lease: %w", err))
	}
	bundle, err := credential.BuildBundle(acquired, lease)
	if err != nil {
		return operationError(ctx, err)
	}
	defer func() {
		retErr = combineErrors(retErr, credential.Cleanup(context.WithoutCancel(ctx), s.RT, s.Name, lease))
	}()
	if err := credential.Stage(ctx, s.RT, s.Name, lease, bundle); err != nil {
		return operationError(ctx, err)
	}
	if err := operationError(ctx, nil); err != nil {
		return err
	}

	wrapped, err := credential.Wrap(lease, command)
	if err != nil {
		return operationError(ctx, err)
	}
	return operationError(ctx, s.RT.ExecInteractive(ctx, s.Name, workdir, wrapped))
}

func operationError(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(err, cause) {
		return err
	}
	return combineErrors(cause, err)
}

func combineErrors(current, additional error) error {
	if additional == nil {
		return current
	}
	if current == nil {
		return additional
	}
	return errors.Join(current, additional)
}
