package coordination

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const MaxDiagnosticsBytes = 32 << 10
const MaxDiagnosticTransitions = 32

// Diagnostics contain observations only. They never authorize work or recovery.
type WorkerDiagnostics struct {
	Placement
	Version      int                    `json:"version"`
	RestoreEpoch int64                  `json:"restore_epoch"`
	ObservedAt   time.Time              `json:"observed_at"`
	StartedAt    time.Time              `json:"started_at"`
	Work         DiagnosticWork         `json:"work"`
	Wait         DiagnosticWait         `json:"wait"`
	GitHub       DiagnosticService      `json:"github"`
	OpenCode     DiagnosticService      `json:"opencode"`
	Transitions  []DiagnosticTransition `json:"transitions,omitempty"`
}

type AgentDiagnostics struct {
	Report     WorkerDiagnostics `json:"report"`
	ReceivedAt time.Time         `json:"received_at"`
	Stale      bool              `json:"stale"`
}

// DiagnosticBinding is an explicit host-owned observation pin for one fleet
// target. It neither registers placement with master nor authorizes VM actions.
type DiagnosticBinding struct {
	MasterID string `json:"master_id" toml:"master_id"`
	Placement
}

func ValidateDiagnosticBinding(b DiagnosticBinding) error {
	if b.Generation < 1 {
		return ErrInvalid
	}
	for _, id := range []string{b.MasterID, b.AgentID, b.HostID, b.InstanceID} {
		if !diagnosticID.MatchString(id) {
			return ErrInvalid
		}
	}
	return nil
}

type DiagnosticWork struct {
	Continuation    *TaskContinuationTarget `json:"continuation_target,omitempty"`
	ObservedAt      time.Time               `json:"observed_at"`
	JobID           string                  `json:"job_id,omitempty"`
	Stage           string                  `json:"stage,omitempty"`
	Phase           string                  `json:"phase,omitempty"`
	TaskURL         string                  `json:"task,omitempty"`
	PRURL           string                  `json:"pr,omitempty"`
	HeadSHA         string                  `json:"head,omitempty"`
	SessionID       string                  `json:"session_id,omitempty"`
	SubmissionState string                  `json:"submission_state,omitempty"`
	SubmittedModel  *DiagnosticModel        `json:"submitted_model,omitempty"`
	ExecutedModel   *DiagnosticModel        `json:"executed_model,omitempty"`
}

type DiagnosticModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Variant  string `json:"variant,omitempty"`
}

type DiagnosticWait struct {
	ObservedAt   time.Time `json:"observed_at"`
	Handling     string    `json:"handling"`
	Reason       string    `json:"reason,omitempty"`
	Dependencies []string  `json:"dependencies,omitempty"`
	NextRetry    time.Time `json:"next_retry"`
	Deadline     time.Time `json:"deadline"`
}

type DiagnosticService struct {
	LastRead    time.Time `json:"last_read"`
	Available   bool      `json:"available"`
	ObservedAt  time.Time `json:"observed_at"`
	LastSuccess time.Time `json:"last_success"`
	Origin      string    `json:"origin,omitempty"`
	Category    string    `json:"category,omitempty"`
	RetryAt     time.Time `json:"retry_at"`
	Requests    int       `json:"requests"`
	DurationMS  int64     `json:"duration_ms"`
}

type DiagnosticTransition struct {
	At       time.Time `json:"at"`
	Stage    string    `json:"stage,omitempty"`
	Phase    string    `json:"phase,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Origin   string    `json:"origin,omitempty"`
	Category string    `json:"category,omitempty"`
}

var diagnosticCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,95}$`)
var diagnosticID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var diagnosticModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var diagnosticRef = regexp.MustCompile(`^/[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+/(issues|pull)/[1-9][0-9]*$`)
var diagnosticSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func DiagnosticCode(v string) bool { return v == "" || diagnosticCode.MatchString(v) }
func ValidDiagnosticModel(m *DiagnosticModel) bool {
	return m != nil && diagnosticModelID.MatchString(m.Provider) && diagnosticModelID.MatchString(m.Model) && (m.Variant == "" || diagnosticID.MatchString(m.Variant))
}
func DiagnosticReference(v string) bool {
	if v == "" {
		return true
	}
	u, err := url.Parse(v)
	return err == nil && len(v) <= 512 && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && diagnosticRef.MatchString(u.Path)
}

func ValidateDiagnostics(d WorkerDiagnostics) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return ErrInvalid
	}
	if len(raw) > MaxDiagnosticsBytes || len(d.Transitions) > MaxDiagnosticTransitions || len(d.Wait.Dependencies) > MaxDiagnosticTransitions {
		return ErrSize
	}
	if d.Version != 1 || d.RestoreEpoch < 1 || d.Generation < 1 || d.ObservedAt.IsZero() || d.StartedAt.IsZero() {
		return ErrInvalid
	}
	for _, id := range []string{d.AgentID, d.HostID, d.InstanceID} {
		if !diagnosticID.MatchString(id) {
			return ErrInvalid
		}
	}
	if !slices.Contains([]string{"working", "waiting", "auto_retry", "action_required", "unknown"}, d.Wait.Handling) {
		return ErrInvalid
	}
	for _, code := range []string{d.Work.Stage, d.Work.Phase, d.Work.SubmissionState, d.Wait.Reason, d.GitHub.Category, d.OpenCode.Category} {
		if !DiagnosticCode(code) {
			return ErrInvalid
		}
	}
	if !DiagnosticReference(d.Work.TaskURL) || !DiagnosticReference(d.Work.PRURL) {
		return ErrInvalid
	}
	for _, ref := range d.Wait.Dependencies {
		if ref == "" || !DiagnosticReference(ref) {
			return ErrInvalid
		}
	}
	if d.Work.JobID != "" && !diagnosticSHA.MatchString(d.Work.JobID) || d.Work.HeadSHA != "" && !diagnosticSHA.MatchString(d.Work.HeadSHA) {
		return ErrInvalid
	}
	if d.Work.SessionID != "" && (!diagnosticID.MatchString(d.Work.SessionID) || !strings.HasPrefix(d.Work.SessionID, "ses_")) {
		return ErrInvalid
	}
	if d.Work.Continuation != nil && ValidateContinuationTarget(*d.Work.Continuation) != nil {
		return ErrInvalid
	}
	for _, m := range []*DiagnosticModel{d.Work.SubmittedModel, d.Work.ExecutedModel} {
		if m != nil && !ValidDiagnosticModel(m) {
			return ErrInvalid
		}
	}
	for _, s := range []DiagnosticService{d.GitHub, d.OpenCode} {
		if !slices.Contains([]string{"", "github", "opencode", "worker", "master"}, s.Origin) || s.Requests < 0 || s.DurationMS < 0 {
			return ErrInvalid
		}
	}
	for _, t := range d.Transitions {
		if t.At.IsZero() || !DiagnosticCode(t.Stage) || !DiagnosticCode(t.Phase) || !DiagnosticCode(t.Reason) || !DiagnosticCode(t.Category) || !slices.Contains([]string{"", "github", "opencode", "worker", "master"}, t.Origin) {
			return ErrInvalid
		}
	}
	return nil
}
