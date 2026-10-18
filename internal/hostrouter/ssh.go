package hostrouter

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/sarcasticbird/coop/internal/runtime"
)

var targetID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,254}$`)

func safeText(s string, max int) bool {
	return len(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

// SSH uses only the user's trusted SSH destination/configuration references.
// It does not read credentials, forward agents or copy files.
type SSH struct {
	// Command is injectable for transport fixtures; nil uses os/exec.
	Command func(context.Context, []string, io.Writer) error
}

func (s SSH) Inventory(ctx context.Context, h Host) ([]InventoryEntry, error) {
	if h == Local || h.Validate() != nil {
		return nil, errors.New("invalid remote host")
	}
	args := []string{"-T", "-x", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=3", "--", h.Address, "coop --host local inventory --json"}
	stdout := runtime.NewBoundedCapture(4 << 20)
	var err error
	if s.Command != nil {
		err = s.Command(ctx, args, stdout)
	} else {
		cmd := exec.CommandContext(ctx, "ssh", args...)
		cmd.Stdout = stdout
		cmd.Stderr = io.Discard // SSH/configuration diagnostics can contain private material
		cmd.WaitDelay = 200 * time.Millisecond
		err = cmd.Run()
	}
	if err != nil {
		return nil, errors.New("SSH inventory unavailable")
	}
	if stdout.Dropped() != 0 {
		return nil, errors.New("inventory response too large")
	}
	var snapshot Snapshot
	if decodeJSON(stdout.Bytes(), &snapshot) != nil || snapshot.Version != 1 || snapshot.RegistryError != "" || len(snapshot.Hosts) != 1 || snapshot.Hosts[0].Host != Local || snapshot.Hosts[0].Error != "" || snapshot.Hosts[0].Entries == nil {
		return nil, errors.New("invalid remote inventory")
	}
	rows := snapshot.Hosts[0].Entries
	for _, row := range rows {
		if row.Target.Host != Local {
			return nil, errors.New("invalid remote identity")
		}
	}
	if err := ValidateEntries(rows); err != nil {
		return nil, err
	}
	return rows, nil
}
