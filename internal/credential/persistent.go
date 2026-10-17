package credential

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
)

// PersistentManager publishes machine-scoped credential generations. They live
// in guest tmpfs until shutdown; refresh never revokes a generation a live shell
// or managed agent may still reference.
type PersistentManager struct{ Executor Executor }

const publishPersistentScript = `set -eu
umask 077
lease=$1
mkdir -p /run/coop
cat > "$lease/metadata.json"
tmp=/run/coop/.credentials-${lease##*/}
trap 'rm -f -- "$tmp"' EXIT
ln -s "$lease" "$tmp"
mv -Tf -- "$tmp" /run/coop/current-credentials
trap - EXIT`

func (m PersistentManager) Stage(ctx context.Context, name string, acquired []Acquired) (GuestLease, error) {
	if m.Executor == nil {
		return GuestLease{}, fmt.Errorf("persistent credential executor is not configured")
	}
	lease, err := NewGuestLease(rand.Reader)
	if err != nil {
		return lease, err
	}
	bundle, err := BuildBundle(acquired, lease)
	if err != nil {
		return lease, err
	}
	if err := Stage(ctx, m.Executor, name, lease, bundle); err != nil {
		return lease, err
	}
	metadata, err := json.Marshal(bundle.metadata)
	if err != nil {
		return lease, err
	}
	args := []string{"sh", "-c", publishPersistentScript, "coop-persistent-credentials", lease.Dir}
	if err := m.Executor.ExecContext(ctx, name, args, bytes.NewReader(metadata)); err != nil {
		// A failed response does not prove the atomic rename failed. A guest
		// process may already have selected this generation. Retain it in
		// tmpfs until shutdown, just like every previously published lease.
		return lease, fmt.Errorf("publish persistent credentials (generation retained until shutdown): %w", err)
	}
	return lease, nil
}
