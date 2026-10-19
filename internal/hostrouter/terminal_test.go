package hostrouter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestTerminalProcess is an SSH stand-in running in a real OS PTY. It uses no
// network and spawns no descendants that could outlive a failed test.
func TestTerminalProcess(t *testing.T) {
	if os.Getenv("COOP_TERMINAL_FIXTURE") != "1" {
		return
	}
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if _, err := pty.GetsizeFull(f); err != nil {
			os.Exit(21)
		}
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	args := os.Args[separator+1:]
	if len(args) < 4 || args[0] != "-tt" || args[len(args)-2] != "fixture-alias" || args[len(args)-1] != os.Getenv("COOP_TERMINAL_COMMAND") {
		os.Exit(22)
	}
	// Distinct reply markers distinguish child IO from terminal echo.
	fmt.Println("READY")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "size":
			rows, cols, err := pty.Getsize(os.Stdin)
			if err != nil {
				os.Exit(23)
			}
			fmt.Printf("SIZE %d %d\n", rows, cols)
		case "exit":
			fmt.Println("BYE")
			os.Exit(0)
		case "fail":
			fmt.Fprintln(os.Stderr, "private-sentinel")
			os.Exit(7)
		default:
			fmt.Printf("REPLY %s\n", scanner.Text())
		}
	}
	os.Exit(0)
}

func terminalFixture(t *testing.T, kind TargetKind) (Target, context.Context) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "fixture", Address: "fixture-alias"}
	if err := r.Change("add", h.Name, h.Address); err != nil {
		t.Fatal(err)
	}
	target := Target{Host: h, Kind: kind, ID: "fixture-profile"}
	args := []string{"--"}
	if kind == Agent {
		args = []string{"--agent", target.ID, "--"}
	}
	command, err := actionCommand(ActionRequest{Target: target, Operation: "command", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_TERMINAL_FIXTURE", "1")
	t.Setenv("COOP_TERMINAL_COMMAND", command)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestTerminalProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return target, ctx
}

func terminalLine(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	// Ignore terminal echo; require the distinct child-generated response.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for %q: %v (last line %q)", want, err, line)
		}
		if strings.TrimSpace(line) == want {
			return
		}
	}
}

func TestTerminalTTYRoundTripResizeAndExit(t *testing.T) {
	for _, kind := range []TargetKind{Agent, Interactive} {
		t.Run(string(kind), func(t *testing.T) {
			target, ctx := terminalFixture(t, kind)
			a, err := (SSH{}).Attach(ctx, target, TerminalSize{Rows: 24, Columns: 80})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Close() })
			reader := bufio.NewReader(a)
			terminalLine(t, reader, "READY")
			_, _ = io.WriteString(a, "size\nhello\n")
			terminalLine(t, reader, "SIZE 24 80")
			terminalLine(t, reader, "REPLY hello")
			if err := a.Resize(TerminalSize{Rows: 41, Columns: 123}); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(a, "size\n")
			terminalLine(t, reader, "SIZE 41 123")
			if err := a.Resize(TerminalSize{}); err == nil {
				t.Fatal("accepted empty size")
			}
			_, _ = io.WriteString(a, "exit\n")
			if err := a.Wait(); err != nil {
				t.Fatal(err)
			}
			// Wait must not close the master before buffered output is drained.
			terminalLine(t, reader, "BYE")
			if _, err := reader.ReadByte(); err != io.EOF {
				t.Fatalf("PTY EOF = %v", err)
			}
			if err := a.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTerminalDetachCancelAndDisconnect(t *testing.T) {
	for _, mode := range []string{"detach", "cancel", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			target, parent := terminalFixture(t, Agent)
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			a, err := (SSH{}).Attach(ctx, target, TerminalSize{Rows: 24, Columns: 80})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			terminalLine(t, bufio.NewReader(a), "READY")
			if mode == "disconnect" {
				_, _ = io.WriteString(a, "fail\n")
			} else {
				readDone := make(chan struct{})
				go func() { _, _ = a.Read(make([]byte, 1)); close(readDone) }()
				if mode == "cancel" {
					cancel()
				} else if err := a.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-readDone:
				case <-time.After(time.Second):
					t.Fatal("terminal read leaked after detach/cancel")
				}
			}
			err = a.Wait()
			switch mode {
			case "detach":
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel = %v", err)
				}
			case "disconnect":
				var exit interface{ ExitCode() int }
				if !errors.As(err, &exit) || exit.ExitCode() != 7 || strings.Contains(err.Error(), "private-sentinel") {
					t.Fatalf("disconnect = %v", err)
				}
			}
			if a.(*sshTerminal).cmd.ProcessState == nil {
				t.Fatal("SSH process was not reaped")
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					if got := a.Wait(); got != err {
						t.Errorf("Wait changed: %v / %v", got, err)
					}
					_ = a.Close()
				})
			}
			wg.Wait()
		})
	}
}

func TestTerminalRejectsInvalidAndStaleRequests(t *testing.T) {
	target, ctx := terminalFixture(t, Agent)
	r, _ := DefaultRegistry()
	if err := r.Change("update", target.Host.Name, "changed-alias"); err != nil {
		t.Fatal(err)
	}
	if a, err := (SSH{}).Attach(ctx, target, TerminalSize{Rows: 24, Columns: 80}); err == nil {
		_ = a.Close()
		t.Fatal("stale target launched SSH")
	}
	if a, err := (SSH{}).AttachCommand(ctx, ActionRequest{Target: target, Operation: "private-sentinel"}, TerminalSize{Rows: 24, Columns: 80}); err == nil || strings.Contains(err.Error(), "private-sentinel") {
		if a != nil {
			_ = a.Close()
		}
		t.Fatalf("invalid operation accepted or echoed: %v", err)
	}
}

func TestTerminalStartupAndCanceledContext(t *testing.T) {
	target, ctx := terminalFixture(t, Agent)
	if _, err := (SSH{}).Attach(ctx, target, TerminalSize{}); err == nil {
		t.Fatal("empty size accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (SSH{}).Attach(canceled, target, TerminalSize{Rows: 24, Columns: 80}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attach = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (SSH{}).Attach(ctx, target, TerminalSize{Rows: 24, Columns: 80}); err == nil || err.Error() != "SSH terminal unavailable" {
		t.Fatalf("missing SSH = %v", err)
	}
}
