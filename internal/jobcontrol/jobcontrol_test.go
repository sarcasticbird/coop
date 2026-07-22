package jobcontrol

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"syscall"
	"testing"
)

func TestMonitorKillsStoppedChildWhenInspectionFails(t *testing.T) {
	inspectErr := errors.New("inspect failed")
	changes := make(chan os.Signal, 1)
	var signaledGroup int
	var sentSignal syscall.Signal
	terminal := &Terminal{
		childChanges: changes,
		stopped:      func(int) (bool, error) { return false, inspectErr },
		signalGroup: func(group int, sig syscall.Signal) error {
			signaledGroup = group
			sentSignal = sig
			return nil
		},
	}
	done := make(chan struct{})
	result := terminal.Monitor(20, done)
	changes <- syscall.SIGCHLD

	err := <-result
	if !errors.Is(err, inspectErr) {
		t.Fatalf("monitor error = %v", err)
	}
	if signaledGroup != -20 || sentSignal != syscall.SIGKILL {
		t.Fatalf("termination signal = (%d, %s)", signaledGroup, sentSignal)
	}
}

func TestSuspendForChildRestoresParentAndContinuesForegroundChild(t *testing.T) {
	defer signal.Reset(syscall.SIGTTOU)
	foreground := 20
	var assigned []int
	suspended := false
	var continuedGroup int
	terminal := &Terminal{
		foregroundGroup: 10,
		childGroup:      20,
		restoreNeeded:   true,
		getForeground:   func() (int, error) { return foreground, nil },
		setForeground: func(group int) error {
			foreground = group
			assigned = append(assigned, group)
			return nil
		},
		suspendSelf: func() error {
			suspended = true
			// Simulate the parent shell's fg command resuming Coop in its
			// original foreground process group.
			foreground = 10
			return nil
		},
		signalGroup: func(group int, sig syscall.Signal) error {
			if sig != syscall.SIGCONT {
				t.Fatalf("continued with %s", sig)
			}
			continuedGroup = group
			return nil
		},
	}

	if err := terminal.suspendForChild(20); err != nil {
		t.Fatal(err)
	}
	if !suspended {
		t.Fatal("Coop was not suspended with its stopped child")
	}
	if !slices.Equal(assigned, []int{10, 20}) {
		t.Fatalf("foreground assignments = %v", assigned)
	}
	if continuedGroup != -20 {
		t.Fatalf("continued process group = %d", continuedGroup)
	}
}

func TestSuspendForChildDoesNotStealTerminalAfterBackgroundResume(t *testing.T) {
	foreground := 20
	var assigned []int
	suspensions := 0
	continues := 0
	terminal := &Terminal{
		foregroundGroup: 10,
		childGroup:      20,
		restoreNeeded:   true,
		getForeground:   func() (int, error) { return foreground, nil },
		setForeground: func(group int) error {
			foreground = group
			assigned = append(assigned, group)
			return nil
		},
		suspendSelf: func() error {
			// Simulate bg: the shell remains the terminal foreground group.
			foreground = 99
			suspensions++
			return nil
		},
		signalGroup: func(int, syscall.Signal) error {
			continues++
			return nil
		},
	}

	if err := terminal.suspendForChild(20); err != nil {
		t.Fatal(err)
	}
	// The background child stops again on terminal input. The shell still
	// owns the terminal, so Coop must suspend without trying TIOCSPGRP.
	if err := terminal.suspendForChild(20); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Restore(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(assigned, []int{10}) {
		t.Fatalf("background resume stole terminal: assignments = %v", assigned)
	}
	if suspensions != 2 || continues != 2 {
		t.Fatalf("repeated background stop: suspensions=%d continues=%d", suspensions, continues)
	}
}

func TestConfigureForegroundPreservesSIGTTOUAndCompleteArgv(t *testing.T) {
	if os.Getenv("COOP_JOBCONTROL_SIGNAL_PROBE") == "1" {
		if signal.Ignored(syscall.SIGTTOU) {
			os.Exit(42)
		}
		return
	}
	signal.Reset(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	cmd := exec.Command("/bin/echo", "arg with spaces", "$literal")
	cmd.Args[0] = "custom-argv-zero"
	wantPath := cmd.Path
	wantArgs := slices.Clone(cmd.Args)
	terminal := &Terminal{fd: 7}

	configureForegroundChild(cmd, terminal, 10)
	defer terminal.stopChildNotifications()
	probe := exec.Command(os.Args[0], "-test.run=TestConfigureForegroundPreservesSIGTTOUAndCompleteArgv")
	probe.Env = append(os.Environ(), "COOP_JOBCONTROL_SIGNAL_PROBE=1")
	if err := probe.Run(); err != nil {
		t.Fatalf("foreground configuration leaked ignored SIGTTOU to child: %v", err)
	}
	if cmd.Path != wantPath || !slices.Equal(cmd.Args, wantArgs) {
		t.Fatalf("foreground configuration changed command: path=%q args=%q", cmd.Path, cmd.Args)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Foreground || cmd.SysProcAttr.Ctty != 7 {
		t.Fatalf("foreground process attributes = %#v", cmd.SysProcAttr)
	}
}
