package runtime

import (
	"context"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestMachineArgumentsSurviveNativeShellWrapper(t *testing.T) {
	want := []string{"spaces and tabs\t", "$(false)", "'quote'", "", "a;b", "line\nbreak"}
	args := machineExecArgs("coop-agent-robin", "/workspace", append([]string{"printf", "%s\\000"}, want...), false, false)
	index := 0
	for args[index] != "--" {
		index++
	}
	out, err := exec.Command("/bin/sh", "-c", strings.Join(args[index+1:], " ")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), want) {
		t.Fatalf("argument corruption: %q", out)
	}
}

func TestMachineFirstBootGetsPrivateTerminal(t *testing.T) {
	var got []string
	m := AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) error { got = args; return nil }}
	if err := m.Start(context.Background(), "coop-agent-robin"); err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 || !reflect.DeepEqual(got[:4], []string{"-q", "/dev/null", "container", "machine"}) {
		t.Fatalf("bootstrap without terminal: %q", got)
	}
}

func TestMachineExecUsesAuthoritativeChildAndLiteralArguments(t *testing.T) {
	var got []string
	m := AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
		switch strings.Join(args, " ") {
		case "machine list --format json":
			_, err := io.WriteString(out, `[{"id":"coop-agent-robin"}]`)
			return err
		case "machine inspect coop-agent-robin":
			_, err := io.WriteString(out, `[{"id":"coop-agent-robin","status":"running","homeMount":"none","cpus":2,"memory":4294967296,"containerId":"native-child","image":{"reference":"test"}}]`)
			return err
		default:
			got = args
		}
		return nil
	}}
	if err := m.Exec(context.Background(), "coop-agent-robin", "/workspace", []string{"printf", "%s", "$(false) spaces"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--user", "root", "--workdir", "/workspace", "native-child", "printf", "%s", "$(false) spaces"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestMachineCreationDisablesHomeSharing(t *testing.T) {
	var got []string
	m := AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) error {
		got = append([]string(nil), args...)
		return nil
	}}
	if err := m.Create(context.Background(), "coop-agent-robin", "coop-machine:test", 6, "12G"); err != nil {
		t.Fatal(err)
	}
	want := []string{"machine", "create", "--name", "coop-agent-robin", "--home-mount", "none", "--cpus", "6", "--memory", "12G", "--no-boot", "coop-machine:test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMachineInspectRejectsSchemaDriftAndHostShare(t *testing.T) {
	for _, data := range []string{`[]`, `[{"id":"other","status":"running"}]`, `[{"id":"coop-agent-robin","status":"running","homeMount":"rw"}]`, `[{"id":"coop-agent-robin","status":"unknown","homeMount":"none"}]`} {
		if _, err := parseMachineInspect([]byte(data), "coop-agent-robin"); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestMachineVersionUsesNumericComparison(t *testing.T) {
	for _, v := range []string{"container CLI version 1.5.0", "container CLI version 1.10.0", "container CLI version 2.0.1"} {
		if err := validateMachineVersion(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"container CLI version 1.4.9", "container CLI version 0.15.0", "garbage"} {
		if err := validateMachineVersion(v); err == nil {
			t.Fatalf("accepted %s", v)
		}
	}
}
