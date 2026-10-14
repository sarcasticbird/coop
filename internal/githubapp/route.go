package githubapp

import (
	"errors"
	"net/url"
	"strings"
)

func repoOwner(value string) (string, error) {
	value = strings.TrimPrefix(value, "git@github.com:")
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("unsupported GitHub repository URL")
		}
		value = strings.TrimPrefix(u.Path, "/")
	}
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || !ownerName.MatchString(parts[0]) || parts[1] == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("invalid GitHub repository context")
	}
	return strings.ToLower(parts[0]), nil
}

func SelectOwner(args []string, explicitOwner, repoOrigin string) (string, error) {
	return SelectOwnerWithRepo(args, explicitOwner, repoOrigin, "")
}

// SelectOwnerWithRepo treats GH_REPO as a target, never as an unexamined gh override.
func SelectOwnerWithRepo(args []string, explicitOwner, repoOrigin, ghRepo string) (string, error) {
	chosen := ""
	add := func(owner string) error {
		if !ownerName.MatchString(owner) {
			return errors.New("invalid GitHub owner context")
		}
		owner = strings.ToLower(owner)
		if chosen != "" && chosen != owner {
			return errors.New("conflicting GitHub owner contexts")
		}
		chosen = owner
		return nil
	}
	if explicitOwner != "" {
		if err := add(explicitOwner); err != nil {
			return "", err
		}
	}
	if ghRepo != "" {
		owner, err := repoOwner(ghRepo)
		if err != nil {
			return "", err
		}
		if err := add(owner); err != nil {
			return "", err
		}
	}
	if len(args) == 0 {
		return "", errors.New("GitHub command is required")
	}
	if args[0] == "auth" {
		return "", errors.New("credential-printing and login commands are disabled; use coop-github status")
	}
	api := args[0] == "api"
	targetCommand := args[0] == "repo"
	if len(args) > 1 && (args[0] == "pr" || args[0] == "issue") {
		switch args[1] {
		case "view", "checkout", "checks", "merge", "review", "comment", "close", "reopen", "edit", "diff", "ready", "lock", "unlock", "transfer", "pin", "unpin", "develop":
			targetCommand = true
		}
	}
	if !api && targetCommand && len(args) >= 3 {
		positionals := make([]string, 0)
		for i := 2; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "-") {
				if !strings.Contains(arg, "=") {
					switch arg {
					case "--repo", "-R", "--owner", "--json", "--jq", "-q", "--template", "-t", "--limit", "-L", "--hostname", "--body", "-b", "--body-file", "-F", "--title", "--base", "-B", "--head", "-H", "--label", "-l", "--assignee", "-a", "--reviewer", "-r", "--milestone", "--project", "--state", "--author", "--search", "--sort", "--order", "--branch", "--visibility", "--description", "--homepage":
						i++
					}
				}
				continue
			}
			positionals = append(positionals, arg)
		}
		for _, target := range positionals {
			if strings.Contains(target, "://") {
				u, err := url.Parse(target)
				if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.Fragment != "" {
					return "", errors.New("unsupported positional GitHub URL")
				}
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) < 2 {
					return "", errors.New("invalid positional GitHub URL")
				}
				if err := add(parts[0]); err != nil {
					return "", err
				}
			} else if args[0] == "repo" {
				if args[1] == "list" {
					if err := add(target); err != nil {
						return "", err
					}
				} else if strings.Contains(target, "/") {
					owner, err := repoOwner(target)
					if err != nil {
						return "", err
					}
					if err := add(owner); err != nil {
						return "", err
					}
				}
			}
			// Only the repository operand identifies repo view/clone/fork targets;
			// subsequent clone operands are local destinations.
			if args[0] == "repo" {
				break
			}
		}
	}
	opaque := false
	endpoints := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.ContainsRune(arg, '\x00') {
			return "", errors.New("invalid GitHub argument")
		}
		if arg == "--hostname" || strings.HasPrefix(arg, "--hostname=") {
			return "", errors.New("GitHub host override is disabled")
		}
		for _, flag := range []string{"--owner", "--repo", "-R"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				var value string
				if arg == flag {
					i++
					if i >= len(args) {
						return "", errors.New("missing GitHub context argument")
					}
					value = args[i]
				} else {
					value = strings.TrimPrefix(arg, flag+"=")
				}
				if flag != "--owner" {
					var err error
					value, err = repoOwner(value)
					if err != nil {
						return "", err
					}
				}
				if err := add(value); err != nil {
					return "", err
				}
				arg = ""
				break
			}
		}
		if !api || i == 0 || arg == "" {
			continue
		}
		switch arg {
		case "-f", "--raw-field", "-F", "--field", "-H", "--header", "-X", "--method", "--input", "--jq", "-q", "--template", "-t":
			i++
			if i >= len(args) {
				return "", errors.New("missing GitHub API argument")
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		endpoints++
		endpoint := arg
		if strings.Contains(arg, "://") {
			u, err := url.Parse(arg)
			if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.Fragment != "" {
				return "", errors.New("GitHub API URL must use api.github.com")
			}
			endpoint = u.Path
		}
		endpoint = strings.SplitN(endpoint, "?", 2)[0]
		parts := strings.Split(strings.Trim(endpoint, "/"), "/")
		if len(parts) >= 2 && (parts[0] == "repos" || parts[0] == "orgs" || parts[0] == "users") {
			if err := add(parts[1]); err != nil {
				return "", err
			}
		} else {
			opaque = true
		}
	}
	if api && endpoints != 1 {
		return "", errors.New("exactly one GitHub API endpoint is required")
	}
	if opaque && chosen == "" {
		return "", errors.New("this GitHub API operation requires explicit owner context")
	}
	if chosen == "" && repoOrigin != "" {
		owner, err := repoOwner(repoOrigin)
		if err != nil {
			return "", err
		}
		if err := add(owner); err != nil {
			return "", err
		}
	}
	if chosen == "" {
		return "", errors.New("GitHub owner is ambiguous; set COOP_GITHUB_OWNER or select a repository")
	}
	return chosen, nil
}
