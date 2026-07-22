package credential

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

const (
	maxEnvironmentValueBytes    = 64 << 10
	maxInjectedEnvironmentBytes = 256 << 10
)

var ErrEnvironmentTooLarge = errors.New("credential environment exceeds safe exec limit")

// SecretFile is one lease-relative file staged in guest tmpfs.
type SecretFile struct {
	path string
	mode fs.FileMode
	data []byte
}

// Format prevents diagnostic formatting from exposing file contents.
func (SecretFile) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<secret file redacted>")
}

// Bundle is the complete secret material and environment for one guest entry.
type Bundle struct {
	files    []SecretFile
	env      map[string][]byte
	unsetEnv []string
	metadata []NamedMetadata
}

// Format prevents diagnostic formatting from exposing bundled secrets.
func (Bundle) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<credential bundle redacted>")
}

// NamedMetadata associates safe metadata with its configured grant name.
type NamedMetadata struct {
	Name string
	Metadata
}

// BuildBundle adapts acquired material to guest files and environment values.
func BuildBundle(acquired []Acquired, lease GuestLease) (Bundle, error) {
	bundle := Bundle{
		env:      make(map[string][]byte),
		metadata: make([]NamedMetadata, 0, len(acquired)),
	}
	total := 0
	environmentTotal := 0
	addEnv := func(name string, value []byte) error {
		value = bytes.TrimSuffix(value, []byte{'\n'})
		value = bytes.TrimSuffix(value, []byte{'\r'})
		if bytes.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("environment injection %s contains a NUL or newline", name)
		}
		if len(value) > maxEnvironmentValueBytes {
			return fmt.Errorf("environment injection %s: %w", name, ErrEnvironmentTooLarge)
		}
		environmentTotal += len(name) + 1 + len(value) + 1
		if environmentTotal > maxInjectedEnvironmentBytes {
			return ErrEnvironmentTooLarge
		}
		total += len(name) + len(value)
		if total > MaxBundleBytes {
			return ErrBundleTooLarge
		}
		bundle.env[name] = bytes.Clone(value)
		return nil
	}
	addFile := func(relative string, data []byte) error {
		total += len(relative) + len(data)
		if total > MaxBundleBytes {
			return ErrBundleTooLarge
		}
		bundle.files = append(bundle.files, SecretFile{path: relative, mode: 0o600, data: bytes.Clone(data)})
		return nil
	}

	for i := range acquired {
		item := &acquired[i]
		bundle.metadata = append(bundle.metadata, NamedMetadata{Name: item.Selected.Name, Metadata: item.metadata})
		relative := fmt.Sprintf("files/%03d", i)
		guestPath := path.Join(lease.Dir, relative)
		inject := item.Selected.Spec.Inject
		switch inject.Type {
		case "environment":
			if err := addEnv(inject.Name, item.payload); err != nil {
				return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
			}
		case "file":
			if err := addFile(relative, item.payload); err != nil {
				return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
			}
			if err := addEnv(inject.PathEnv, []byte(guestPath)); err != nil {
				return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
			}
		case "git-credential-store":
			if err := addFile(relative, item.payload); err != nil {
				return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
			}
			gitEnv := map[string]string{
				"GIT_CONFIG_COUNT":   "2",
				"GIT_CONFIG_KEY_0":   "credential.helper",
				"GIT_CONFIG_VALUE_0": "",
				"GIT_CONFIG_KEY_1":   "credential.helper",
				"GIT_CONFIG_VALUE_1": "store --file " + guestPath,
			}
			for _, name := range []string{
				"GIT_CONFIG_COUNT",
				"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
				"GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
			} {
				if err := addEnv(name, []byte(gitEnv[name])); err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
			}
		case "aws":
			if item.aws == nil {
				return Bundle{}, fmt.Errorf("credential %q: AWS material is missing", item.Selected.Name)
			}
			awsFile, err := buildAWSFile(*item.aws)
			if err != nil {
				return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
			}
			if err := addFile(relative, awsFile); err != nil {
				return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
			}
			awsEnv := map[string]string{
				"AWS_SHARED_CREDENTIALS_FILE": guestPath,
				"AWS_PROFILE":                 "coop",
				"AWS_EC2_METADATA_DISABLED":   "true",
			}
			for _, name := range []string{"AWS_EC2_METADATA_DISABLED", "AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE"} {
				if err := addEnv(name, []byte(awsEnv[name])); err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
			}
			for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
				total += len(name)
				if total > MaxBundleBytes {
					return Bundle{}, ErrBundleTooLarge
				}
				bundle.unsetEnv = append(bundle.unsetEnv, name)
			}
		default:
			return Bundle{}, fmt.Errorf("credential %q: unsupported injection type %q", item.Selected.Name, inject.Type)
		}
	}

	slices.SortFunc(bundle.files, func(a, b SecretFile) int { return strings.Compare(a.path, b.path) })
	slices.Sort(bundle.unsetEnv)
	return bundle, nil
}

func buildAWSFile(credentials AWSCredentials) ([]byte, error) {
	for _, value := range []string{credentials.accessKeyID, credentials.secretAccessKey, credentials.sessionToken} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("AWS credentials contain invalid control characters")
		}
	}
	var out strings.Builder
	out.WriteString("[coop]\naws_access_key_id = ")
	out.WriteString(credentials.accessKeyID)
	out.WriteString("\naws_secret_access_key = ")
	out.WriteString(credentials.secretAccessKey)
	if credentials.sessionToken != "" {
		out.WriteString("\naws_session_token = ")
		out.WriteString(credentials.sessionToken)
	}
	out.WriteByte('\n')
	return []byte(out.String()), nil
}

// Summaries returns deterministic, non-secret descriptions of acquired grants.
func Summaries(acquired []Acquired) []string {
	summaries := make([]string, 0, len(acquired))
	for _, item := range acquired {
		provider := item.metadata.Provider
		if provider == "" {
			provider = item.Selected.Spec.Source.Type
		}
		description := provider
		profile := item.metadata.Profile
		if profile == "" {
			profile = item.Selected.Spec.Source.Profile
		}
		if profile != "" {
			description += " " + profile
		}
		if item.metadata.ExpiresAt.IsZero() {
			description += "; validity: source-managed"
		} else {
			description += "; expires " + item.metadata.ExpiresAt.UTC().Format(time.RFC3339)
		}
		summaries = append(summaries, fmt.Sprintf("%s (%s)", item.Selected.Name, description))
	}
	return summaries
}

// SortedEnvNames returns bundle environment names in serialization order.
func SortedEnvNames(env map[string][]byte) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
