package runtime

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestPortableSystemdSpecPreservesGuestAdministrationWithoutHostSharing(t *testing.T) {
	args, err := runArgs(RunSpec{Name: "coop-agent-pilot", Image: "image", CPUs: 2, Memory: "2G", GuestAdmin: true, Command: []string{"/opt/coop-machine/boot"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--cap-add ALL", "--masked-path NONE", "--read-only-path NONE", "image /opt/coop-machine/boot"} {
		if !strings.Contains(joined, want) {
			t.Fatal("systemd guest setting missing", want, joined)
		}
	}
	if strings.Contains(joined, "--ssh") || strings.Contains(joined, "--mount") || strings.Contains(joined, "sleep infinity") {
		t.Fatal("portable guest inherited interactive behavior", joined)
	}
}

func TestPortableRuntimeRejectsNullListings(t *testing.T) {
	m := AppleMachine{Command: func(_ context.Context, _ []string, _ io.Reader, out, _ io.Writer) error {
		_, err := io.WriteString(out, "null")
		return err
	}}
	if _, err := m.NativeContainers(t.Context()); err == nil {
		t.Fatal("null container listing interpreted as absence")
	}
	if _, err := m.NativeVolumes(t.Context()); err == nil {
		t.Fatal("null volume listing interpreted as absence")
	}
}
