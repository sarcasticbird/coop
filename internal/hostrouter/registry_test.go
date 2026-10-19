package hostrouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testRegistry(t *testing.T) Registry {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return Registry{Path: filepath.Join(root, "registry.json")}
}

func registryBytes(t *testing.T, r Registry) []byte {
	t.Helper()
	b, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeRegistryFixture(t *testing.T, r Registry, b []byte) {
	t.Helper()
	if err := os.WriteFile(r.Path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryCRUDAndIdempotence(t *testing.T) {
	r := testRegistry(t)
	if got, err := r.List(); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("missing registry List() = %#v, %v", got, err)
	}
	for _, h := range []Host{{Name: "zulu", Address: "deploy@zulu-alias"}, {Name: "alpha", Address: "alpha.example"}} {
		if err := r.Change("add", h.Name, h.Address); err != nil {
			t.Fatal(err)
		}
	}
	want := []Host{{Name: "alpha", Address: "alpha.example"}, {Name: "zulu", Address: "deploy@zulu-alias"}}
	if got, err := r.List(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, %v; want %#v", got, err, want)
	}
	for _, op := range []string{"add", "update"} {
		before := registryBytes(t, r)
		if err := r.Change(op, "alpha", "alpha.example"); err != nil {
			t.Fatalf("idempotent %s: %v", op, err)
		}
		if !bytes.Equal(before, registryBytes(t, r)) {
			t.Fatalf("idempotent %s changed registry contents", op)
		}
	}
	if err := r.Change("update", "alpha", "new-alias"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Resolve("alpha"); err != nil || got != (Host{Name: "alpha", Address: "new-alias"}) {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}
	if err := r.Change("remove", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("alpha"); err == nil {
		t.Fatal("removed server still resolves")
	}
	if got, err := r.List(); err != nil || !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("remove changed another server: %#v, %v", got, err)
	}
	if err := r.Change("remove", "zulu", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := r.List(); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty registry List() = %#v, %v", got, err)
	}
	for _, path := range []string{r.Path, r.Path + ".lock"} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions = %o; want 600", filepath.Base(path), st.Mode().Perm())
		}
	}
}

func TestDefaultRegistryUsesCoopConfigurationRoot(t *testing.T) {
	home := filepath.Dir(testRegistry(t).Path)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	r, err := DefaultRegistry()
	if err != nil || r.Path != filepath.Join(home, ".config", "coop", "servers", "registry.json") {
		t.Fatalf("default registry = %+v, %v", r, err)
	}
	t.Setenv("XDG_CONFIG_HOME", home)
	r, err = DefaultRegistry()
	if err != nil || r.Path != filepath.Join(home, "coop", "servers", "registry.json") {
		t.Fatalf("XDG registry = %+v, %v", r, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := DefaultRegistry(); err == nil {
		t.Fatal("relative configuration root accepted")
	}
}

func TestRegistryRejectsInvalidChangesWithoutMutationOrInputEcho(t *testing.T) {
	r := testRegistry(t)
	if err := r.Change("add", "existing", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	before := registryBytes(t, r)
	for _, tc := range []struct{ operation, name, address string }{
		{"add", "local", "private-sentinel"},
		{"update", "local", "private-sentinel"},
		{"remove", "local", ""},
		{"add", "", "trusted-alias"},
		{"add", "Private-sentinel", "trusted-alias"},
		{"add", "private-sentinel-", "trusted-alias"},
		{"add", strings.Repeat("a", 64), "trusted-alias"},
		{"add", "private-sentinel/name", "trusted-alias"},
		{"add", "valid", ""},
		{"add", "valid", "-oProxyCommand=private-sentinel"},
		{"add", "valid", "user:private-sentinel@host"},
		{"add", "valid", "ssh://private-sentinel/host"},
		{"add", "valid", "host;private-sentinel"},
		{"add", "valid", "host\nprivate-sentinel"},
		{"add", "valid", "user@private-sentinel@host"},
		{"add", "valid", strings.Repeat("a", 256)},
		{"add", "existing", "private-sentinel"},
		{"update", "private-sentinel", "trusted-alias"},
		{"remove", "private-sentinel", ""},
		{"private-sentinel", "valid", "trusted-alias"},
	} {
		t.Run(tc.operation+"/"+tc.name+"/"+tc.address, func(t *testing.T) {
			err := r.Change(tc.operation, tc.name, tc.address)
			if err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("invalid change accepted or input echoed: %v", err)
			}
			if !bytes.Equal(before, registryBytes(t, r)) {
				t.Fatal("rejected change mutated registry")
			}
		})
	}
	if _, err := r.Resolve("private-sentinel"); err == nil || strings.Contains(err.Error(), "private-sentinel") {
		t.Fatalf("unknown resolution accepted or input echoed: %v", err)
	}
}

func TestRegistryStrictConfig(t *testing.T) {
	for name, document := range map[string]string{
		"empty":             "",
		"truncated":         `{"version":1,"servers":[`,
		"trailing document": `{"version":1,"servers":[]} {"private-sentinel":true}`,
		"trailing garbage":  `{"version":1,"servers":[]} private-sentinel`,
		"unknown field":     `{"version":1,"servers":[],"token":"private-sentinel"}`,
		"credential field":  `{"version":1,"servers":[{"name":"one","address":"alias","identity_file":"private-sentinel"}]}`,
		"wrong version":     `{"version":2,"servers":[]}`,
		"wrong type":        `{"version":"private-sentinel","servers":[]}`,
		"wrong servers":     `{"version":1,"servers":{}}`,
		"reserved":          `{"version":1,"servers":[{"name":"local"}]}`,
		"invalid address":   `{"version":1,"servers":[{"name":"one","address":"private-sentinel;cmd"}]}`,
		"duplicate name":    `{"version":1,"servers":[{"name":"one","address":"alias"},{"name":"one","address":"other"}]}`,
		"null entry":        `{"version":1,"servers":[null]}`,
		"oversized":         strings.Repeat(" ", 64<<10) + `{"version":1,"servers":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := testRegistry(t)
			writeRegistryFixture(t, r, []byte(document))
			if _, err := r.List(); err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("malformed config accepted or echoed: %v", err)
			}
			if err := r.Change("add", "new", "alias"); err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("malformed config overwritten or echoed: %v", err)
			}
			if got := registryBytes(t, r); string(got) != document {
				t.Fatal("malformed registry changed")
			}
			if got, err := r.Resolve("local"); err != nil || got != Local {
				t.Fatalf("malformed remote config broke local resolution: %#v, %v", got, err)
			}
		})
	}
}

func TestRegistryRequiresExplicitSchema(t *testing.T) {
	for name, document := range map[string]string{
		"null document":   `null`,
		"empty object":    `{}`,
		"missing version": `{"servers":[]}`,
		"missing servers": `{"version":1}`,
		"null servers":    `{"version":1,"servers":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := testRegistry(t)
			writeRegistryFixture(t, r, []byte(document))
			if _, err := r.List(); err == nil {
				t.Error("registry accepted a missing or null schema field")
			}
			if err := r.Change("add", "new", "alias"); err == nil {
				t.Error("change accepted an incomplete registry schema")
			}
			if string(registryBytes(t, r)) != document {
				t.Error("change replaced an incomplete registry schema")
			}
		})
	}
}

func TestRegistryRejectsUnsafePermissionsAndSymlinks(t *testing.T) {
	for _, kind := range []string{"directory permissions", "file permissions", "directory symlink", "file symlink", "lock symlink"} {
		t.Run(kind, func(t *testing.T) {
			r := testRegistry(t)
			if err := r.Change("add", "existing", "alias"); err != nil {
				t.Fatal(err)
			}
			original := r
			before := registryBytes(t, r)
			switch kind {
			case "directory permissions":
				if err := os.Chmod(filepath.Dir(r.Path), 0755); err != nil {
					t.Fatal(err)
				}
			case "file permissions":
				if err := os.Chmod(r.Path, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory symlink":
				link := filepath.Join(filepath.Dir(testRegistry(t).Path), "linked")
				if err := os.Symlink(filepath.Dir(r.Path), link); err != nil {
					t.Fatal(err)
				}
				r.Path = filepath.Join(link, "registry.json")
			case "file symlink":
				link := filepath.Join(filepath.Dir(r.Path), "linked.json")
				if err := os.Symlink(r.Path, link); err != nil {
					t.Fatal(err)
				}
				r.Path = link
			case "lock symlink":
				if err := os.Remove(r.Path + ".lock"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(r.Path, r.Path+".lock"); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "lock symlink" {
				if _, err := r.List(); err == nil {
					t.Fatal("unsafe registry read succeeded")
				}
			}
			if err := r.Change("add", "new", "alias"); err == nil {
				t.Fatal("unsafe registry write succeeded")
			}
			if !bytes.Equal(before, registryBytes(t, original)) {
				t.Fatal("unsafe write changed original registry")
			}
			if got, err := r.Resolve("local"); err != nil || got != Local {
				t.Fatalf("unsafe registry broke local resolution: %#v, %v", got, err)
			}
		})
	}
}

func TestRegistryStoresOnlySSHReferences(t *testing.T) {
	r := testRegistry(t)
	home := filepath.Join(filepath.Dir(r.Path), "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	secret := []byte("private-key-sentinel-not-registry-material")
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.WriteFile(key, secret, 0600); err != nil {
		t.Fatal(err)
	}
	config := []byte("Host fleet-alias\n IdentityFile " + key + "\n")
	configPath := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Change("add", "fleet", "deploy@fleet-alias"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(registryBytes(t, r), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": float64(1), "servers": []any{map[string]any{"name": "fleet", "address": "deploy@fleet-alias"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry contains more than the SSH reference: %#v", got)
	}
	for path, want := range map[string][]byte{key: secret, configPath: config} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("registration changed SSH material: %v", err)
		}
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Resolve("fleet"); err != nil || got.Address != "deploy@fleet-alias" {
		t.Fatalf("reference depended on credential material: %#v, %v", got, err)
	}
}

func TestRegistryRejectsForeignOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing file ownership requires root")
	}
	for _, kind := range []string{"directory", "file", "lock"} {
		t.Run(kind, func(t *testing.T) {
			r := testRegistry(t)
			if err := r.Change("add", "existing", "alias"); err != nil {
				t.Fatal(err)
			}
			path := r.Path
			if kind == "directory" {
				path = filepath.Dir(path)
			}
			if kind == "lock" {
				path += ".lock"
			}
			if err := os.Chown(path, 12345, -1); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chown(path, os.Geteuid(), -1); err != nil {
					t.Error(err)
				}
			})
			if kind != "lock" {
				if _, err := r.List(); err == nil {
					t.Fatal("foreign-owned registry read succeeded")
				}
			}
			if err := r.Change("update", "existing", "other-alias"); err == nil {
				t.Fatal("foreign-owned registry write succeeded")
			}
		})
	}
}

func TestRegistryLimit(t *testing.T) {
	r := testRegistry(t)
	f := registryFile{Version: 1, Servers: []Host{}}
	for i := range 64 {
		f.Servers = append(f.Servers, Host{Name: fmt.Sprintf("host-%02d", i), Address: "alias"})
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistryFixture(t, r, b)
	if err := r.Change("add", "host-00", "alias"); err != nil {
		t.Fatalf("idempotent add at limit: %v", err)
	}
	before := registryBytes(t, r)
	if err := r.Change("add", "overflow", "alias"); err == nil {
		t.Fatal("accepted more than 64 hosts")
	}
	if !bytes.Equal(before, registryBytes(t, r)) {
		t.Fatal("overflow changed registry")
	}
	f.Servers = append(f.Servers, Host{Name: "overflow", Address: "alias"})
	b, err = json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistryFixture(t, r, b)
	if _, err := r.List(); err == nil {
		t.Fatal("oversized server list accepted")
	}
}

func TestRegistryConcurrentWritesPreserveOtherHosts(t *testing.T) {
	r := testRegistry(t)
	const count = 24
	for _, operation := range []string{"add", "update", "remove"} {
		t.Run(operation, func(t *testing.T) {
			start := make(chan struct{})
			errs := make(chan error, count)
			var wg sync.WaitGroup
			for i := range count {
				wg.Go(func() {
					<-start
					if operation == "remove" && i%2 != 0 {
						return
					}
					address := "alias"
					if operation == "update" {
						address = fmt.Sprintf("updated-%02d", i)
					}
					errs <- r.Change(operation, fmt.Sprintf("host-%02d", i), address)
				})
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			want := []Host{}
			for i := range count {
				if operation == "remove" && i%2 == 0 {
					continue
				}
				address := "alias"
				if operation != "add" {
					address = fmt.Sprintf("updated-%02d", i)
				}
				want = append(want, Host{Name: fmt.Sprintf("host-%02d", i), Address: address})
			}
			if got, err := r.List(); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("concurrent %s lost writes: got %#v, %v; want %#v", operation, got, err, want)
			}
		})
	}
}
