package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/credential"
)

const MaxConfig = 1 << 20

var slug = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
var repoSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var hexSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)
var numericID = regexp.MustCompile(`^[1-9][0-9]*$`)
var modelVariantName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func validModelVariant(s string) bool {
	return s == "" || (len(s) <= 128 && modelVariantName.MatchString(s))
}

func strictJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid JSON document")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing JSON")
	}
	return nil
}

type wireLimits struct {
	PollInterval    string `json:"poll_interval,omitempty"`
	TurnLimit       string `json:"turn_limit,omitempty"`
	RetryBase       string `json:"retry_base,omitempty"`
	RetryCap        string `json:"retry_cap,omitempty"`
	MaxAttempts     int    `json:"max_attempts,omitempty"`
	MaxReviewCycles int    `json:"max_review_cycles,omitempty"`
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errors.New("invalid duration")
	}
	return d, nil
}
func (l Limits) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireLimits{l.PollInterval.String(), l.TurnLimit.String(), l.RetryBase.String(), l.RetryCap.String(), l.MaxAttempts, l.MaxReviewCycles})
}
func (l *Limits) UnmarshalJSON(raw []byte) error {
	var w wireLimits
	if err := strictJSON(raw, &w); err != nil {
		return err
	}
	for _, p := range []struct {
		s string
		d *time.Duration
	}{{w.PollInterval, &l.PollInterval}, {w.TurnLimit, &l.TurnLimit}, {w.RetryBase, &l.RetryBase}, {w.RetryCap, &l.RetryCap}} {
		v, err := parseDuration(p.s)
		if err != nil {
			return err
		}
		*p.d = v
	}
	l.MaxAttempts = w.MaxAttempts
	l.MaxReviewCycles = w.MaxReviewCycles
	return nil
}

type wireOpenCode struct {
	BaseURL           string `json:"base_url"`
	PasswordFile      string `json:"password_file"`
	RequestTimeout    string `json:"request_timeout,omitempty"`
	ReconcileInterval string `json:"reconcile_interval,omitempty"`
}

func (c OpenCodeConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireOpenCode{c.BaseURL, c.PasswordFile, c.RequestTimeout.String(), c.ReconcileInterval.String()})
}
func (c *OpenCodeConfig) UnmarshalJSON(raw []byte) error {
	var w wireOpenCode
	if err := strictJSON(raw, &w); err != nil {
		return err
	}
	c.BaseURL = w.BaseURL
	c.PasswordFile = w.PasswordFile
	var err error
	c.RequestTimeout, err = parseDuration(w.RequestTimeout)
	if err != nil {
		return err
	}
	c.ReconcileInterval, err = parseDuration(w.ReconcileInterval)
	return err
}

func Defaults(c Config) Config {
	c.Projects = append([]ProjectRef(nil), c.Projects...)
	if c.Wake != nil {
		copy := *c.Wake
		c.Wake = &copy
		if c.Wake.PeerHopLimit == 0 {
			c.Wake.PeerHopLimit = 2
		}
	}
	if c.Limits.PollInterval == 0 {
		c.Limits.PollInterval = time.Minute
	}
	if c.Limits.TurnLimit == 0 {
		c.Limits.TurnLimit = 30 * time.Minute
	}
	if c.Limits.RetryBase == 0 {
		c.Limits.RetryBase = time.Minute
	}
	if c.Limits.RetryCap == 0 {
		c.Limits.RetryCap = 15 * time.Minute
	}
	if c.Limits.MaxAttempts == 0 {
		c.Limits.MaxAttempts = 3
	}
	if c.Limits.MaxReviewCycles == 0 {
		c.Limits.MaxReviewCycles = 5
	}
	if c.OpenCode.BaseURL == "" {
		c.OpenCode.BaseURL = "http://127.0.0.1:4096"
	}
	if c.OpenCode.PasswordFile == "" {
		c.OpenCode.PasswordFile = "/var/lib/coop/opencode/server-password"
	}
	if c.OpenCode.RequestTimeout == 0 {
		c.OpenCode.RequestTimeout = 10 * time.Second
	}
	if c.OpenCode.ReconcileInterval == 0 {
		c.OpenCode.ReconcileInterval = 5 * time.Second
	}
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.StatusField == "" {
			p.StatusField = "Status"
		}
		if p.ReadyValue == "" {
			p.ReadyValue = "Ready"
		}
		if p.InProgressValue == "" {
			p.InProgressValue = "In Progress"
		}
		if p.DoneValue == "" {
			p.DoneValue = "Done"
		}
	}
	return c
}
func LoadConfig(path string) (Config, error) {
	var c Config
	raw, err := credential.ReadPrivateFile(path)
	if err != nil {
		return c, errors.New("cannot read private worker config")
	}
	if len(raw) > MaxConfig {
		return c, errors.New("worker config too large")
	}
	// Inspect the envelope before decoding shared fields. A trailing version
	// must not select a weaker decoder, and null must not hide a v2-only field.
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return c, errors.New("invalid worker configuration envelope")
	}
	version, seenVersion, seenTeam := 0, false, false
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return c, errors.New("invalid worker configuration envelope")
		}
		name, ok := key.(string)
		if !ok {
			return c, errors.New("invalid worker configuration envelope")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return c, errors.New("invalid worker configuration envelope")
		}
		if strings.EqualFold(name, "version") {
			if seenVersion || json.Unmarshal(value, &version) != nil {
				return c, errors.New("ambiguous worker configuration version")
			}
			seenVersion = true
		}
		seenTeam = seenTeam || strings.EqualFold(name, "team")
	}
	if _, err := d.Token(); err != nil {
		return c, errors.New("invalid worker configuration envelope")
	}
	if !seenVersion || (version != 1 && version != 2) {
		return c, errors.New("unsupported worker configuration version")
	}
	if version == 1 && seenTeam {
		return c, errors.New("v1 configuration forbids team policy")
	}
	if version == 2 {
		if err := strictTeamJSON(raw, &c); err != nil {
			return c, err
		}
	} else if err := strictJSON(raw, &c); err != nil {
		return c, err
	}
	c = Defaults(c)
	return c, ValidateConfig(c)
}
func validRepo(repo string) bool {
	p := strings.Split(repo, "/")
	return len(p) == 2 && slug.MatchString(p[0]) && repoSlug.MatchString(p[1]) && p[1] != "." && p[1] != ".."
}
func cleanPrivatePath(s string) bool { return filepath.IsAbs(s) && s == filepath.Clean(s) && s != "/" }
func sameActor(a, b Actor) bool      { return a.ID > 0 && a.ID == b.ID && a.Kind == b.Kind }
func containsFold(items []string, item string) bool {
	for _, v := range items {
		if strings.EqualFold(v, item) {
			return true
		}
	}
	return false
}
func validActor(a Actor, kind string) bool {
	return a.ID > 0 && a.Login != "" && a.Kind == kind && !strings.ContainsAny(a.Login, "\x00\r\n ")
}

func ValidateConfig(c Config) error {
	c = Defaults(c)
	if err := validateWakeConfig(c); err != nil {
		return err
	}
	if (c.Version != 1 && c.Version != 2) || !slug.MatchString(c.Name) || len(strings.Split(c.Model, "/")) != 2 || strings.ContainsAny(c.Model, "\x00\r\n ") {
		return errors.New("invalid worker version, identity, role or model")
	}
	if !validModelVariant(c.ModelVariant) {
		return errors.New("invalid model variant: use a bounded explicit variant name")
	}
	if !cleanPrivatePath(c.StateDir) || !cleanPrivatePath(c.WorkRoot) || c.StateDir == c.WorkRoot || strings.HasPrefix(c.WorkRoot, c.StateDir+"/") || strings.HasPrefix(c.StateDir, c.WorkRoot+"/") {
		return errors.New("worker state and work roots must be separate absolute directories")
	}
	if !validActor(c.Owner, "User") || !validActor(c.Self, "Bot") {
		return errors.New("invalid distinct owner and bot identities")
	}
	if c.Version == 1 {
		if c.Team != nil || (c.Role != "implementer" && c.Role != "planner") || !validActor(c.Implementer, "Bot") || !validActor(c.Reviewer, "Bot") || sameActor(c.Implementer, c.Reviewer) {
			return errors.New("invalid v1 worker authority")
		}
		if (c.Role == "implementer" && !sameActor(c.Self, c.Implementer)) || (c.Role == "planner" && !sameActor(c.Self, c.Reviewer)) {
			return errors.New("worker role does not match bot identity")
		}
	} else {
		if c.Team == nil || c.Role != "" || c.Implementer != (Actor{}) || c.Reviewer != (Actor{}) {
			return errors.New("v2 requires team policy and forbids legacy authority fields")
		}
		if err := ValidateTeamPolicy(*c.Team); err != nil {
			return err
		}
		if member, ok := TeamMemberForActor(*c.Team, c.Self); !ok || member.Name != c.Name {
			return errors.New("v2 self must match exactly one named roster actor")
		}
	}
	if len(c.CodeOwners) == 0 || len(c.CodeOwners) > 64 || len(c.ProjectOwners) > 64 || len(c.DiscussionRepositories) > 64 {
		return errors.New("invalid owner scope")
	}
	for _, owners := range [][]string{c.CodeOwners, c.ProjectOwners} {
		seen := map[string]bool{}
		for _, o := range owners {
			key := strings.ToLower(o)
			if !slug.MatchString(o) || seen[key] {
				return errors.New("invalid or duplicate owner")
			}
			seen[key] = true
		}
	}
	for _, repos := range [][]string{c.Repositories, c.DiscussionRepositories} {
		seen := map[string]bool{}
		for _, r := range repos {
			key := strings.ToLower(r)
			if !validRepo(r) || seen[key] {
				return errors.New("invalid or duplicate repository")
			}
			seen[key] = true
		}
	}
	for _, r := range c.Repositories {
		if !containsFold(c.CodeOwners, strings.Split(r, "/")[0]) {
			return errors.New("repository outside code owners")
		}
	}
	if len(c.ProjectOwners) > 0 && len(c.Projects) > 0 {
		return errors.New("choose Project discovery or explicit selectors")
	}
	seenProjects := map[string]bool{}
	for _, p := range c.Projects {
		key := strings.ToLower(p.Owner) + "/" + stringID(p.Number)
		if !slug.MatchString(p.Owner) || p.Number <= 0 || seenProjects[key] {
			return errors.New("invalid or duplicate Project")
		}
		seenProjects[key] = true
	}
	l := c.Limits
	if l.PollInterval < time.Second || l.PollInterval > time.Hour || l.TurnLimit < time.Second || l.TurnLimit > 30*time.Minute || l.RetryBase < time.Second || l.RetryCap < l.RetryBase || l.RetryCap > 15*time.Minute || l.MaxAttempts < 1 || l.MaxAttempts > 3 || l.MaxReviewCycles < 1 || l.MaxReviewCycles > 5 {
		return errors.New("worker limits exceed supported bounds")
	}
	u, err := url.Parse(c.OpenCode.BaseURL)
	if err != nil || u.Scheme != "http" || u.Host != "127.0.0.1:4096" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !cleanPrivatePath(c.OpenCode.PasswordFile) || c.OpenCode.RequestTimeout < time.Second || c.OpenCode.RequestTimeout > 30*time.Second || c.OpenCode.ReconcileInterval < time.Second || c.OpenCode.ReconcileInterval > time.Minute {
		return errors.New("invalid guest-local OpenCode configuration")
	}
	if _, err := os.Lstat(c.OpenCode.PasswordFile); err == nil {
		if _, err := credential.ReadPrivateFile(c.OpenCode.PasswordFile); err != nil {
			return errors.New("invalid private server password file")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect server password file")
	}
	for _, b := range c.BootstrapGoals {
		if !validGitHubURL(b.URL) || !hexSHA.MatchString(b.BodySHA) || b.OwnerID != c.Owner.ID || b.Source != "owner-confirmed" || len(b.Repositories) == 0 {
			return errors.New("invalid bootstrap approval")
		}
		for _, r := range b.Repositories {
			if !validRepo(r) || !containsFold(c.CodeOwners, strings.Split(r, "/")[0]) {
				return errors.New("bootstrap repository outside scope")
			}
		}
	}
	for r, mode := range c.CheckoutModes {
		if !validRepo(r) || !containsFold(c.CodeOwners, strings.Split(r, "/")[0]) || (mode != "clone" && mode != "wt") {
			return errors.New("invalid checkout mode")
		}
	}
	return nil
}
