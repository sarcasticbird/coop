package credential

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
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
		var stagedFile []byte
		fileStaged := false
		ensureFile := func(data []byte) error {
			if fileStaged {
				if !bytes.Equal(stagedFile, data) {
					return errors.New("credential exposures require different file contents")
				}
				return nil
			}
			if err := addFile(relative, data); err != nil {
				return err
			}
			stagedFile = bytes.Clone(data)
			fileStaged = true
			return nil
		}

		for _, expose := range config.Exposures(item.Selected.Spec) {
			switch expose.Type {
			case "environment":
				value, err := environmentExposureValue(item, expose)
				if err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
				if err := addEnv(expose.Name, value); err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
			case "file":
				if item.userPass != nil || item.aws != nil {
					return Bundle{}, fmt.Errorf("credential %q: file exposure requires opaque material", item.Selected.Name)
				}
				if err := ensureFile(item.payload); err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
				if err := addEnv(expose.PathEnv, []byte(guestPath)); err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
			case "git-credential-store":
				store := item.payload
				gitConfigCount := "2"
				gitConfigNames := []string{
					"GIT_CONFIG_COUNT",
					"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
					"GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
				}
				if item.userPass != nil {
					var err error
					store, err = buildGitCredentialStore(*item.userPass)
					if err != nil {
						return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
					}
					gitConfigCount = "3"
					gitConfigNames = append(gitConfigNames, "GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2")
				} else if item.aws != nil {
					return Bundle{}, fmt.Errorf("credential %q: Git store exposure requires username-password or opaque material", item.Selected.Name)
				}
				if err := ensureFile(store); err != nil {
					return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
				}
				gitEnv := map[string]string{
					"GIT_CONFIG_COUNT":   gitConfigCount,
					"GIT_CONFIG_KEY_0":   "credential.helper",
					"GIT_CONFIG_VALUE_0": "",
					"GIT_CONFIG_KEY_1":   "credential.helper",
					"GIT_CONFIG_VALUE_1": "store --file " + guestPath,
				}
				if item.userPass != nil {
					key, err := gitCredentialUseHTTPPathKey(*item.userPass)
					if err != nil {
						return Bundle{}, fmt.Errorf("credential %q: %w", item.Selected.Name, err)
					}
					gitEnv["GIT_CONFIG_KEY_2"] = key
					gitEnv["GIT_CONFIG_VALUE_2"] = "false"
				}
				for _, name := range gitConfigNames {
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
				if err := ensureFile(awsFile); err != nil {
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
				return Bundle{}, fmt.Errorf("credential %q: unsupported injection type %q", item.Selected.Name, expose.Type)
			}
		}
	}

	slices.SortFunc(bundle.files, func(a, b SecretFile) int { return strings.Compare(a.path, b.path) })
	slices.Sort(bundle.unsetEnv)
	return bundle, nil
}

func environmentExposureValue(item *Acquired, expose config.CredentialInjection) ([]byte, error) {
	if expose.Field == "" {
		if item.userPass != nil || item.aws != nil {
			return nil, errors.New("environment exposure without field requires opaque material")
		}
		return item.payload, nil
	}
	if item.userPass == nil {
		return nil, fmt.Errorf("environment field %q requires username-password material", expose.Field)
	}
	switch expose.Field {
	case "username":
		return []byte(item.userPass.username), nil
	case "password":
		return []byte(item.userPass.password), nil
	default:
		return nil, fmt.Errorf("unsupported username-password field %q", expose.Field)
	}
}

func buildGitCredentialStore(material userPasswordMaterial) ([]byte, error) {
	if material.protocol != "https" || material.host == "" || material.username == "" || material.password == "" {
		return nil, errors.New("git credential material is incomplete")
	}
	credentialURL, err := gitCredentialTargetURL(material)
	if err != nil {
		return nil, err
	}
	credentialURL.User = url.UserPassword(material.username, material.password)
	return []byte(credentialURL.String() + "\n"), nil
}

func gitCredentialUseHTTPPathKey(material userPasswordMaterial) (string, error) {
	target, err := gitCredentialTargetURL(material)
	if err != nil {
		return "", err
	}
	return "credential." + target.String() + ".useHttpPath", nil
}

func gitCredentialTargetURL(material userPasswordMaterial) (url.URL, error) {
	if material.protocol != "https" || material.host == "" {
		return url.URL{}, errors.New("git credential target is incomplete")
	}
	for _, value := range []string{material.protocol, material.host, material.path} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return url.URL{}, errors.New("git credential target contains invalid control characters")
		}
	}
	credentialURL := url.URL{
		Scheme: material.protocol,
		Host:   material.host,
	}
	if material.path != "" {
		credentialURL.Path = "/" + material.path
	}
	return credentialURL, nil
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
