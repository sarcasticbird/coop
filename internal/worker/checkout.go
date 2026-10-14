package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Checkouts interface {
	Prepare(context.Context, Job) (Job, error)
	Progress(context.Context, Job) (string, error)
}
type TaskCheckouts struct {
	config Config
	runner CommandRunner
	origin func(string) string
}

func NewCheckouts(c Config, r CommandRunner) *TaskCheckouts {
	return &TaskCheckouts{config: c, runner: r, origin: func(repo string) string { return "https://github.com/" + repo + ".git" }}
}

type checkoutOwner struct{ TaskURL, GoalURL, Repo, Branch, Origin, Mode string }

func BindingKey(c Config, j Job) string {
	if j.Kind == "wake" {
		return BodyHash(c.Name + ":wake:" + j.GoalURL + ":" + j.TaskURL)
	}
	if j.Kind == "plan" {
		return BodyHash(c.Role + ":plan:" + j.GoalURL)
	}
	return BodyHash(c.Role + ":task:" + j.TaskURL)
}
func (c *TaskCheckouts) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var result CommandResult
	var err error
	if runner, ok := c.runner.(interface {
		RunIn(context.Context, string, []string, []string, io.Reader) (CommandResult, error)
	}); ok {
		result, err = runner.RunIn(ctx, dir, args, nil, nil)
	} else {
		result, err = c.runner.Run(ctx, append([]string{"sh", "-c", `cd -- "$1" && shift && exec "$@"`, "coop-worker-checkout", dir}, args...), nil, nil)
	}
	if err != nil || result.ExitCode != 0 {
		return nil, errors.New("checkout command failed")
	}
	return result.Stdout, nil
}
func (c *TaskCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	key := BindingKey(c.config, j)
	root := filepath.Join(c.config.WorkRoot, key[:32])
	checkout := filepath.Join(root, "repo")
	owner := checkoutOwner{TaskURL: j.TaskURL, GoalURL: j.GoalURL, Mode: "clone"}
	if j.Kind == "plan" {
		owner.TaskURL = ""
		owner.Mode = "planning"
		checkout = filepath.Join(root, "planning")
	} else {
		repo, _, err := parseRef(j.TaskURL)
		if err != nil || !containsFold(c.config.CodeOwners, strings.Split(repo, "/")[0]) {
			return j, errors.New("task checkout outside configured scope")
		}
		owner.Repo = repo
		owner.Origin = c.origin(repo)
		owner.Branch = "agent/" + c.config.Name + "/" + key[:16]
		if c.config.CheckoutModes[repo] == "wt" {
			owner.Mode = "wt"
			checkout = filepath.Join(root, "repo", "agent-"+key[:16])
		}
	}
	if j.Checkout != "" && j.Checkout != checkout {
		return j, errors.New("job checkout ownership mismatch")
	}
	if j.Branch != "" && j.Branch != owner.Branch {
		return j, errors.New("job branch ownership mismatch")
	}
	if err := ensurePrivateDir(c.config.WorkRoot, true); err != nil {
		return j, err
	}
	if err := safePath(root); err != nil {
		return j, err
	}
	markerPath := filepath.Join(root, "owner.json")
	_, existsErr := os.Lstat(root)
	if existsErr == nil {
		raw, err := readPrivate(markerPath, MaxConfig)
		if err != nil {
			return j, errors.New("existing checkout has no private ownership record")
		}
		var saved checkoutOwner
		if strictJSON(raw, &saved) != nil || saved != owner {
			return j, errors.New("foreign checkout ownership record")
		}
	} else if os.IsNotExist(existsErr) {
		if err := ensurePrivateDir(root, true); err != nil {
			return j, err
		}
		raw, _ := json.Marshal(owner)
		if err := atomicPrivate(markerPath, raw); err != nil {
			return j, err
		}
	} else {
		return j, errors.New("cannot inspect task checkout")
	}
	if err := safePath(checkout); err != nil {
		return j, err
	}
	_, checkoutErr := os.Lstat(checkout)
	if os.IsNotExist(checkoutErr) {
		switch owner.Mode {
		case "planning":
			if err := ensurePrivateDir(checkout, true); err != nil {
				return j, err
			}
		case "wt":
			container := filepath.Join(root, "repo")
			if _, err := os.Lstat(container); os.IsNotExist(err) {
				if _, err := c.run(ctx, root, "wt", "clone", owner.Origin, container); err != nil {
					return j, err
				}
			}
			if _, err := c.run(ctx, container, "wt", "add", "agent-"+key[:16], "-b", owner.Branch); err != nil {
				return j, err
			}
		default:
			if _, err := c.run(ctx, root, "git", "clone", "--", owner.Origin, checkout); err != nil {
				return j, err
			}
			if _, err := c.run(ctx, checkout, "git", "switch", "-c", owner.Branch); err != nil {
				return j, err
			}
		}
	} else if checkoutErr != nil {
		return j, errors.New("cannot inspect checkout")
	}
	if owner.Mode != "planning" {
		origin, err := c.run(ctx, checkout, "git", "config", "--get", "remote.origin.url")
		if err != nil || strings.TrimSpace(string(origin)) != owner.Origin {
			return j, errors.New("checkout origin changed")
		}
		branch, err := c.run(ctx, checkout, "git", "branch", "--show-current")
		if err != nil || strings.TrimSpace(string(branch)) != owner.Branch {
			return j, errors.New("checkout branch changed")
		}
		if c.config.Role == "planner" && j.PRURL != "" {
			repo, n, err := parseRef(j.PRURL)
			if err != nil || repo != owner.Repo {
				return j, errors.New("review target outside checkout")
			}
			status, err := c.run(ctx, checkout, "git", "status", "--porcelain")
			if err != nil || len(status) != 0 {
				return j, errors.New("review checkout has uncheckpointed changes")
			}
			if _, err := c.run(ctx, checkout, "git", "fetch", "origin", "refs/pull/"+stringID(n)+"/head"); err != nil {
				return j, err
			}
			head, err := c.run(ctx, checkout, "git", "rev-parse", "FETCH_HEAD")
			if err != nil || strings.TrimSpace(string(head)) != j.HeadSHA {
				return j, errors.New("PR head changed during checkout")
			}
			if _, err := c.run(ctx, checkout, "git", "reset", "--hard", "FETCH_HEAD"); err != nil {
				return j, err
			}
		}
	}
	j.Checkout = checkout
	j.Branch = owner.Branch
	return j, nil
}
func (c *TaskCheckouts) Progress(ctx context.Context, j Job) (string, error) {
	if j.Checkout == "" {
		return "", errors.New("missing task checkout")
	}
	if err := safePath(j.Checkout); err != nil {
		return "", err
	}
	if j.Kind == "plan" {
		return BodyHash("planning:" + j.GoalURL), nil
	}
	head, err := c.run(ctx, j.Checkout, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	files, err := c.run(ctx, j.Checkout, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	items := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
	if len(items) > MaxConnection {
		return "", errors.New("checkout file count limit")
	}
	hash := sha256.New()
	_, _ = hash.Write(head)
	var total int64
	for _, name := range items {
		if name == "" || name == ".env" || strings.HasSuffix(name, ".pem") || strings.Contains(name, "credentials/") {
			continue
		}
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return "", errors.New("unsafe checkout path")
		}
		_, _ = hash.Write([]byte("\x00" + name + "\x00"))
		path := filepath.Join(j.Checkout, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			_, _ = hash.Write([]byte("deleted"))
			continue
		}
		if err != nil {
			return "", errors.New("cannot inspect checkout content")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			_, _ = hash.Write([]byte(link))
			continue
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("nonregular checkout content")
		}
		if err := safePath(path); err != nil {
			return "", err
		}
		f, err := os.Open(path)
		if err != nil {
			return "", errors.New("cannot read checkout content")
		}
		n, readErr := io.Copy(hash, io.LimitReader(f, (64<<20)-total+1))
		closeErr := f.Close()
		total += n
		if readErr != nil || closeErr != nil || total > 64<<20 {
			return "", errors.New("checkout content limit")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
