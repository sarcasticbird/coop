package projectinit

import (
	"slices"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestRenderProducesDeterministicAppendBlock(t *testing.T) {
	got, err := Render(
		[]string{"web/node_modules", "target"},
		[]config.Publish{{HostPort: 5173, GuestPort: 5173}, {HostPort: 8080, GuestPort: 80}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`[[volume]]
path = "target"

[[volume]]
path = "web/node_modules"

[[publish]]
guest_port = 5173
host_port = 5173

[[publish]]
guest_port = 80
host_port = 8080
`)
	if !slices.Equal(got, want) {
		t.Fatalf("rendered block =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderEscapesTOMLStringsAndRejectsControls(t *testing.T) {
	want := []byte("[[volume]]\npath = \"web/\\\"quoted\\\"\\\\node_modules\"\n")
	if got, err := Render([]string{`web/"quoted"\node_modules`}, nil); err != nil || !slices.Equal(got, want) {
		t.Fatalf("escaped block = %q, want %q", got, want)
	}
	if got, err := Render([]string{"web/\x01node_modules"}, nil); err == nil || got != nil {
		t.Fatalf("control character result = %q, error = %v", got, err)
	}
	if got, err := Render(nil, nil); err != nil || got != nil {
		t.Fatalf("empty selection rendered: %q", got)
	}
}
