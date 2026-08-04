package projectinit

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDefaultsDetectedVolumesToNo(t *testing.T) {
	root := initProject(t)
	writeProjectFile(t, root, "package.json", `{}`)
	var output bytes.Buffer
	if err := Run(root, strings.NewReader("\n\n"), &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".coop.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default-no init wrote config: %v", err)
	}
	if !strings.Contains(output.String(), "Add project volume node_modules? [y/N]") || !strings.Contains(output.String(), "No changes selected.") {
		t.Fatalf("init output = %q", output.String())
	}
}

func TestRunPreviewsAndWritesExplicitSelections(t *testing.T) {
	root := initProject(t)
	writeProjectFile(t, root, "package.json", `{}`)
	var output bytes.Buffer
	input := "y\n5173\n\n8080\n18080\n\ny\n"
	if err := Run(root, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	want := []byte(`[[volume]]
path = "node_modules"

[[publish]]
guest_port = 5173
host_port = 5173

[[publish]]
guest_port = 8080
host_port = 18080
`)
	got, err := os.ReadFile(filepath.Join(root, ".coop.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("config =\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(output.String(), "Proposed .coop.toml append:\n\n"+string(want)) || !strings.Contains(output.String(), "Apply these changes? [y/N]") {
		t.Fatalf("preview output = %q", output.String())
	}
	if !strings.Contains(output.String(), "Run `coop up` or enter the Coop to apply the new container configuration.") {
		t.Fatalf("recreation guidance missing: %q", output.String())
	}
}

func TestRunRejectsSelectedVolumeInvalidForRuntimeBeforePreview(t *testing.T) {
	root := initProject(t)
	writeProjectFile(t, root, "apps,legacy/package.json", `{}`)
	var output bytes.Buffer
	err := Run(root, strings.NewReader("y\n\ny\n"), &output)
	if err == nil || !strings.Contains(err.Error(), "reserved runtime grammar") {
		t.Fatalf("invalid selected volume error = %v", err)
	}
	if strings.Contains(output.String(), "Proposed .coop.toml append:") {
		t.Fatalf("invalid selected volume was previewed: %q", output.String())
	}
	if _, err := os.Lstat(filepath.Join(root, ".coop.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid selected volume wrote config: %v", err)
	}
}

func TestRunDeclineEOFFailureAndInvalidConfigNeverWrite(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     io.Reader
		wantError string
	}{
		{name: "decline", input: strings.NewReader("y\n\nn\n")},
		{name: "eof", input: strings.NewReader("y\n\n")},
		{name: "read failure", input: &dataErrorReader{data: []byte("y\n\n"), err: errors.New("injected input")}, wantError: "injected input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initProject(t)
			writeProjectFile(t, root, "package.json", `{}`)
			err := Run(root, tc.input, io.Discard)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".coop.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("declined/failed init wrote config: %v", err)
			}
		})
	}

	t.Run("invalid existing config", func(t *testing.T) {
		root := initProject(t)
		path := filepath.Join(root, ".coop.toml")
		before := []byte("[[volume]\ninvalid")
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := Run(root, strings.NewReader("y\n"), &output); err == nil {
			t.Fatal("invalid existing config accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) || output.Len() != 0 {
			t.Fatalf("invalid config changed or prompted: bytes=%q output=%q", after, output.String())
		}
	})
}

func TestRunOmitsExistingDeclarationsAndRerunIsIdempotent(t *testing.T) {
	root := initProject(t)
	writeProjectFile(t, root, "package.json", `{}`)
	writeProjectFile(t, root, "node_modules/host.txt", "host")
	configPath := filepath.Join(root, ".coop.toml")
	existing := []byte("[[volume]]\npath = \"node_modules\"\n")
	if err := os.WriteFile(configPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(root, strings.NewReader("\n"), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Add project volume node_modules?") || !strings.Contains(output.String(), "No changes selected.") {
		t.Fatalf("existing declaration was proposed: %q", output.String())
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("rerun changed config: %q", got)
	}
}

func initProject(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return t.TempDir()
}

type dataErrorReader struct {
	data []byte
	err  error
}

func (r *dataErrorReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}
