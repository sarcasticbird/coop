// Package credential acquires trusted host credentials and prepares them for
// temporary injection into one interactive Coop entry.
package credential

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
)

const (
	MaxPayloadBytes  = 1 << 20
	MaxBundleBytes   = 8 << 20
	commandTimeout   = 10 * time.Minute
	commandWaitDelay = 2 * time.Second
)

var ErrPayloadTooLarge = errors.New("credential payload exceeds 1 MiB limit")
var ErrBundleTooLarge = errors.New("credential bundle exceeds 8 MiB limit")

// Selected is a validated credential grant selected for one entry.
type Selected struct {
	Name string
	Spec config.Credential
}

// Metadata contains non-secret facts safe to display to the user.
type Metadata struct {
	Provider  string
	Profile   string
	AccountID string
	ExpiresAt time.Time
}

// Acquired owns secret material for a selected grant. Secret fields remain
// private so normal formatting and serialization cannot expose them.
type Acquired struct {
	Selected Selected
	payload  []byte
	aws      *AWSCredentials
	metadata Metadata
	revoke   func(context.Context) error
}

// Format prevents diagnostic formatting from recursively exposing private
// secret fields. Safe display data is available through Metadata instead.
func (Acquired) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<credential redacted>")
}

// GuestLease identifies a temporary guest credential directory.
type GuestLease struct {
	Dir string
}

// AWSCredentials is the structured secret material returned by the AWS CLI's
// process credential format.
type AWSCredentials struct {
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
}

// Format prevents AWS secret material from appearing in diagnostics.
func (AWSCredentials) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<AWS credentials redacted>")
}

// Metadata returns non-secret acquisition facts.
func (a Acquired) Metadata() Metadata {
	return a.metadata
}

// Revoke invalidates upstream material when its source supports revocation.
func (a *Acquired) Revoke(ctx context.Context) error {
	if a.revoke == nil {
		return nil
	}
	return a.revoke(ctx)
}

// RevokeAll revokes grants in reverse acquisition order.
func RevokeAll(ctx context.Context, acquired []Acquired) error {
	var errs []error
	for i := len(acquired) - 1; i >= 0; i-- {
		if err := acquired[i].Revoke(ctx); err != nil {
			errs = append(errs, fmt.Errorf("revoke credential %q: %w", acquired[i].Selected.Name, err))
		}
	}
	return errors.Join(errs...)
}
