package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sarcasticbird/coop/internal/githubapp"
)

func main() {
	args := os.Args[1:]
	if filepath.Base(os.Args[0]) == "gh" {
		args = append([]string{"gh"}, args...)
	}
	if err := run(args, os.Stdin, os.Stdout); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		_, _ = fmt.Fprintln(os.Stderr, "coop-github:", err)
		os.Exit(1)
	}
}
func run(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected mint, env, gh, git-credential or status")
	}
	if args[0] == "mint" {
		flags := flag.NewFlagSet("mint", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("registration", "", "")
		if flags.Parse(args[1:]) != nil || *path == "" || len(flags.Args()) > 0 {
			return errors.New("mint requires --registration /absolute/path")
		}
		r, err := githubapp.LoadRegistration(*path)
		if err != nil {
			return err
		}
		b, err := (githubapp.Client{}).Mint(context.Background(), r)
		if err != nil {
			return err
		}
		return b.Encode(output)
	}
	b, err := githubapp.ReadCurrentBundle(githubapp.RuntimeDir, time.Now())
	if err != nil {
		return err
	}
	expected, err := githubapp.ExpectedAppID()
	if err != nil && args[0] != "env" {
		return err
	}
	if args[0] == "env" {
		flags := flag.NewFlagSet("env", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		id := flags.String("app-id", expected, "")
		if flags.Parse(args[1:]) != nil || len(flags.Args()) == 0 {
			return errors.New("env requires --app-id ID -- command")
		}
		if expected != "" && expected != *id {
			return errors.New("GitHub App identity conflicts with guest configuration")
		}
		if err := githubapp.VerifyApp(b, *id); err != nil {
			return err
		}
		root := "/run/coop/github"
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
			return errors.New("cannot prepare GitHub helper directory")
		}
		self, err := os.Executable()
		if err != nil {
			return errors.New("cannot resolve GitHub helper executable")
		}
		link := filepath.Join(root, "bin", "gh")
		if target, e := os.Readlink(link); e != nil || target != self {
			if e == nil {
				if err := os.Remove(link); err != nil {
					return errors.New("cannot update GitHub helper link")
				}
			}
			if err := os.Symlink(self, link); err != nil {
				return errors.New("cannot install GitHub helper link")
			}
		}
		env := githubapp.CleanEnvironment(os.Environ())
		env = replaceEnv(env, "PATH", filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
		env = replaceEnv(env, "COOP_GITHUB_APP_ID", *id)
		// Use process configuration rather than a stored token or global Git rewrite.
		for _, entry := range []string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=", "GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=" + self + " git-credential", "GIT_CONFIG_KEY_2=credential.useHttpPath", "GIT_CONFIG_VALUE_2=true"} {
			k, v, _ := strings.Cut(entry, "=")
			env = replaceEnv(env, k, v)
		}
		return execCommand(flags.Args(), env)
	}
	if err := githubapp.VerifyApp(b, expected); err != nil {
		return err
	}
	switch args[0] {
	case "status":
		return json.NewEncoder(output).Encode(b.Status())
	case "git-credential":
		if len(args) != 2 {
			return errors.New("git credential action required")
		}
		if args[1] == "store" || args[1] == "erase" {
			return nil
		}
		if args[1] != "get" {
			return errors.New("unsupported Git credential action")
		}
		return githubapp.WriteGitCredential(output, b, input, time.Now())
	case "gh":
		ghArgs := args[1:]
		explicit := os.Getenv("COOP_GITHUB_OWNER")
		if len(ghArgs) > 0 && ghArgs[0] == "--owner" {
			if len(ghArgs) < 3 {
				return errors.New("--owner requires owner and command")
			}
			explicit = ghArgs[1]
			ghArgs = ghArgs[2:]
			if len(ghArgs) > 0 && ghArgs[0] == "--" {
				ghArgs = ghArgs[1:]
			}
		}
		var origin string
		if out, e := exec.Command("git", "config", "--get", "remote.origin.url").Output(); e == nil {
			origin = strings.TrimSpace(string(out))
		}
		owner, err := githubapp.SelectOwnerWithRepo(ghArgs, explicit, origin, os.Getenv("GH_REPO"))
		if err != nil {
			return err
		}
		env, err := b.Environment(owner, os.Environ(), time.Now())
		if err != nil {
			return err
		}
		return runGH(githubapp.CoreGH, ghArgs, env, input, output, os.Stderr)
	default:
		return errors.New("unsupported GitHub helper operation")
	}
}

func runGH(binary string, args, env []string, input io.Reader, output, stderr io.Writer) error {
	// gh expands ordinary and shell aliases after owner validation. Never load
	// persistent configuration into a process carrying an installation token.
	config, err := os.MkdirTemp("", "coop-gh-config-")
	if err != nil {
		return errors.New("cannot prepare GitHub CLI configuration")
	}
	defer func() { _ = os.RemoveAll(config) }()
	cmd := exec.Command(binary, args...)
	cmd.Env = replaceEnv(env, "GH_CONFIG_DIR", config)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, stderr
	return cmd.Run()
}

func replaceEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}
func execCommand(args, env []string) error {
	// Resolve using the new child's PATH rather than the parent's stale PATH.
	program := args[0]
	if !strings.ContainsRune(program, '/') {
		var path string
		for _, entry := range env {
			if strings.HasPrefix(entry, "PATH=") {
				path = strings.TrimPrefix(entry, "PATH=")
			}
		}
		found := ""
		for _, dir := range filepath.SplitList(path) {
			if !filepath.IsAbs(dir) {
				continue
			}
			candidate := filepath.Join(dir, program)
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				found = candidate
				break
			}
		}
		if found == "" {
			return errors.New("configured agent command is unavailable")
		}
		program = found
	}
	if err := syscall.Exec(program, args, env); err != nil {
		return errors.New("cannot execute configured agent command")
	}
	return nil
}
