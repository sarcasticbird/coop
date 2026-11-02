package coordination

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SourceIdentity is observed source history, not a retrospective native binding.
// Account IDs, including aliases, must be resolved before admission. An unknown
// commit identity cannot be made independent by omitting it from Contributors.
type SourceIdentity struct {
	Repository     string `json:"repository"`
	RepositoryID   int64  `json:"repository_id"`
	PRURL          string `json:"pr_url"`
	PRID           string `json:"pr_id"`
	AuthorID       int64  `json:"author_id"`
	AuthorKind     string `json:"author_kind"`
	OwnerID        int64  `json:"owner_id"`
	SourceRepo     string `json:"source_repository"`
	SourceRepoID   int64  `json:"source_repository_id"`
	Branch         string `json:"branch"`
	BaseBranch     string `json:"original_base_branch"`
	BaseSHA        string `json:"original_base_sha"`
	HeadSHA        string `json:"head_sha"`
	TreeSHA        string `json:"tree_sha"`
	BundleKind     string `json:"bundle_kind"`
	BundleRevision int    `json:"bundle_revision"`
	BundleSHA      string `json:"bundle_sha256"`
	PayloadSHA     string `json:"payload_sha256"`
	// Native history is present only for a genuinely native published source.
	OriginalGoal          *GoalRef         `json:"original_goal,omitempty"`
	OriginalTaskURL       string           `json:"original_task_url,omitempty"`
	OriginalAssignmentSHA string           `json:"original_assignment_sha256,omitempty"`
	OriginalPolicySHA     string           `json:"original_policy_sha256,omitempty"`
	OriginalRegistration  string           `json:"original_registration,omitempty"`
	Contributors          []int64          `json:"contributors"`
	ContributorsComplete  bool             `json:"contributors_complete"`
	Evidence              []SourceEvidence `json:"external_evidence"`
}

type SourceEvidence struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	BodySHA string `json:"body_sha256"`
}

// SourceTarget is distinct from the immutable published source base. A receipt
// proves integration into main, not simply that a predecessor PR was merged.
type SourceTarget struct {
	Repository      string                    `json:"repository"`
	RepositoryID    int64                     `json:"repository_id"`
	Branch          string                    `json:"branch"`
	SHA             string                    `json:"sha"`
	ResultTreeSHA   string                    `json:"result_tree_sha"`
	ResultBundleSHA string                    `json:"result_bundle_sha256"`
	Predecessor     *SourceIntegrationReceipt `json:"predecessor,omitempty"`
}

type SourceIntegrationReceipt struct {
	Goal         GoalRef `json:"goal"`
	ID           string  `json:"id"`
	DecisionID   string  `json:"decision_id"`
	PRURL        string  `json:"pr_url"`
	SourceSHA    string  `json:"source_sha256"`
	TargetRepoID int64   `json:"target_repository_id"`
	TargetBranch string  `json:"target_branch"`
	BeforeSHA    string  `json:"before_sha"`
	CommitSHA    string  `json:"commit_sha"`
	TreeSHA      string  `json:"tree_sha"`
	RequestID    string  `json:"request_id"`
}

// SourceEffectReceipt records a single acknowledged effect under its original
// request. Uncertain remote writes are never inferred from state equality.
type SourceEffectReceipt struct {
	Goal       GoalRef `json:"goal"`
	DecisionID string  `json:"decision_id"`
	RequestID  string  `json:"request_id"`
	Kind       string  `json:"kind"`
	CommitSHA  string  `json:"commit_sha,omitempty"`
	TreeSHA    string  `json:"tree_sha,omitempty"`
	ReportID   string  `json:"report_id,omitempty"`
	BodySHA    string  `json:"body_sha256,omitempty"`
}

// SourceDecisionInput grants supplemental gates or one exact effect. It never
// alters the existing source, native assignment, registration or report bodies.
type SourceDecisionInput struct {
	Kind                string         `json:"kind"`
	Goal                GoalRef        `json:"goal"`
	ApprovalSequence    int64          `json:"approval_sequence"`
	GateTaskURL         string         `json:"gate_task_url"`
	GateAssignmentSHA   string         `json:"gate_assignment_sha256"`
	PolicySHA           string         `json:"policy_sha256"`
	Source              SourceIdentity `json:"source"`
	Target              SourceTarget   `json:"target"`
	AcceptanceSHA       string         `json:"acceptance_sha256"`
	Environment         string         `json:"environment"`
	EnvironmentRevision string         `json:"environment_revision"`
	ReviewerID          string         `json:"reviewer_id"`
	QAID                string         `json:"qa_id"`
	ParentID            string         `json:"parent_id,omitempty"`
	EngineeringReportID string         `json:"engineering_report_id,omitempty"`
	EngineeringBodySHA  string         `json:"engineering_body_sha256,omitempty"`
	QAReportID          string         `json:"qa_report_id,omitempty"`
	QABodySHA           string         `json:"qa_body_sha256,omitempty"`
	ActorID             string         `json:"actor_id,omitempty"`
	EvidenceMessageID   string         `json:"evidence_message_id"`
	RequestID           string         `json:"request_id"`
	RevokeID            string         `json:"revoke_id,omitempty"`
}

type SourceDecision struct {
	ID        string              `json:"id"`
	Sequence  int64               `json:"sequence"`
	OwnerID   string              `json:"owner_id"`
	CreatedAt time.Time           `json:"created_at"`
	Input     SourceDecisionInput `json:"input"`
}

type SourceDecisionQuery struct {
	Goal  GoalRef `json:"goal"`
	After int64   `json:"after,omitempty"`
}

type SourceDecisionPage struct {
	Decisions []SourceDecision `json:"decisions"`
	Next      int64            `json:"next,omitempty"`
}

type SourceCompletionInput struct {
	Goal       GoalRef `json:"goal"`
	DecisionID string  `json:"decision_id"`
	RequestID  string  `json:"request_id"`
}

type SourceTaskState struct {
	Reference     string `json:"reference"`
	AssignmentSHA string `json:"assignment_sha256"`
	State         string `json:"state"`
}

type SourceDecisionValidation struct {
	Goal GoalRef `json:"goal"`
	ID   string  `json:"id"`
}

type SourceGateReport struct {
	DecisionID string `json:"decision_id"`
	ScopeSHA   string `json:"scope_sha256"`
	Stage      string `json:"stage"`
	Verdict    string `json:"verdict"`
	ReviewID   string `json:"review_id"`
	ReportBody string `json:"report_body"`
}

func SourceDigest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func ValidateSourceIdentity(s SourceIdentity) error {
	u, err := url.Parse(s.PRURL)
	parts := strings.Split(strings.TrimPrefix(uPath(u), "/"), "/")
	if err != nil || u == nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.ForceQuery || len(parts) != 4 || parts[2] != "pull" || parts[0]+"/"+parts[1] != s.Repository {
		return ErrInvalid
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 || strconv.Itoa(n) != parts[3] || s.RepositoryID < 1 || s.SourceRepoID < 1 || s.OwnerID < 1 || s.AuthorID < 1 || s.PRID == "" || !sourceRepoName(s.Repository) || !sourceRepoName(s.SourceRepo) || !sourceBranch(s.Branch) || !sourceBranch(s.BaseBranch) || !s.ContributorsComplete || len(s.Contributors) == 0 || len(s.Contributors) > 10000 || !slices.Contains(s.Contributors, s.AuthorID) {
		return ErrInvalid
	}
	if !slices.IsSorted(s.Contributors) {
		return ErrInvalid
	}
	for i, id := range s.Contributors {
		if id < 1 || i > 0 && s.Contributors[i-1] == id {
			return ErrInvalid
		}
	}
	for _, sha := range []string{s.BaseSHA, s.HeadSHA, s.TreeSHA} {
		if !handoffHead.MatchString(sha) {
			return ErrInvalid
		}
	}
	if s.BundleKind != "source-bundle-v1" || s.BundleRevision != 1 || !handoffSHA.MatchString(s.BundleSHA) || !handoffSHA.MatchString(s.PayloadSHA) {
		return ErrInvalid
	}
	switch s.AuthorKind {
	case "User":
		if s.OriginalGoal != nil || s.OriginalTaskURL != "" || s.OriginalAssignmentSHA != "" || s.OriginalPolicySHA != "" || s.OriginalRegistration != "" {
			return ErrInvalid
		}
	case "Bot":
		if s.OriginalGoal == nil || s.OriginalTaskURL == "" || !handoffSHA.MatchString(s.OriginalAssignmentSHA) || !handoffSHA.MatchString(s.OriginalPolicySHA) || s.OriginalRegistration == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, e := range s.Evidence {
		if e.ID == "" || len(e.ID) > 2048 || e.Kind == "" || !handoffSHA.MatchString(e.BodySHA) || seen[e.ID] {
			return ErrInvalid
		}
		seen[e.ID] = true
	}
	return nil
}

func uPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Path
}

func sourceRepoName(s string) bool {
	p := strings.Split(s, "/")
	return len(p) == 2 && handoffID.MatchString(p[0]) && handoffID.MatchString(p[1]) && !strings.ContainsAny(s, ":")
}

func sourceBranch(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n ~^:?*[\\") && !strings.Contains(s, "..") && !strings.HasPrefix(s, "-") && !strings.HasSuffix(s, "/")
}

func ValidateSourceDecision(in SourceDecisionInput) error {
	if in.Goal.Source != "master" || in.Goal.MasterID == "" || in.Goal.GoalID == "" || !handoffSHA.MatchString(in.Goal.RevisionSHA256) || !handoffID.MatchString(in.RequestID) || len(in.RequestID) > 128 {
		return ErrInvalid
	}
	// Withdrawal does not depend on source, actor or goal execution eligibility.
	if in.Kind == "revoke" {
		if !handoffID.MatchString(in.RevokeID) {
			return ErrInvalid
		}
		minimal := SourceDecisionInput{Kind: in.Kind, Goal: in.Goal, RequestID: in.RequestID, RevokeID: in.RevokeID}
		if SourceDigest(minimal) != SourceDigest(in) {
			return ErrInvalid
		}
		return nil
	}
	if ValidateSourceIdentity(in.Source) != nil || in.ApprovalSequence < 1 || in.GateTaskURL == "" || !handoffSHA.MatchString(in.GateAssignmentSHA) || !handoffSHA.MatchString(in.PolicySHA) || !handoffSHA.MatchString(in.AcceptanceSHA) || in.Environment == "" || len(in.Environment) > MaxText || in.EnvironmentRevision == "" || len(in.EnvironmentRevision) > 256 || strings.ContainsAny(in.EnvironmentRevision, "\x00\r\n") || !handoffID.MatchString(in.ReviewerID) || !handoffID.MatchString(in.QAID) || in.ReviewerID == in.QAID || !handoffID.MatchString(in.EvidenceMessageID) || in.RevokeID != "" {
		return ErrInvalid
	}
	if in.Target.Repository != in.Source.Repository || in.Target.RepositoryID != in.Source.RepositoryID || in.Target.Branch != "main" || !handoffHead.MatchString(in.Target.SHA) || !handoffHead.MatchString(in.Target.ResultTreeSHA) || !handoffSHA.MatchString(in.Target.ResultBundleSHA) {
		return ErrInvalid
	}
	if in.Source.BaseBranch != "main" {
		r := in.Target.Predecessor
		if r == nil || !handoffID.MatchString(r.ID) || !handoffID.MatchString(r.DecisionID) || r.PRURL == in.Source.PRURL || r.PRURL == "" || r.TargetRepoID != in.Source.RepositoryID || r.TargetBranch != "main" || !handoffSHA.MatchString(r.SourceSHA) || !handoffHead.MatchString(r.BeforeSHA) || !handoffHead.MatchString(r.CommitSHA) || !handoffHead.MatchString(r.TreeSHA) || !handoffID.MatchString(r.RequestID) {
			return ErrInvalid
		}
	} else if in.Target.Predecessor != nil {
		return ErrInvalid
	}
	switch in.Kind {
	case "intake":
		if in.Source.AuthorKind != "User" || in.ParentID != "" {
			return ErrInvalid
		}
	case "transition":
		if in.Source.AuthorKind == "User" && in.ParentID == "" {
			return ErrInvalid
		}
	case "retarget", "deliver":
		if in.ActorID != DeliveryPrincipalID || !handoffID.MatchString(in.ParentID) || !handoffID.MatchString(in.EngineeringReportID) || !handoffSHA.MatchString(in.EngineeringBodySHA) || !handoffID.MatchString(in.QAReportID) || !handoffSHA.MatchString(in.QABodySHA) {
			return ErrInvalid
		}
		return nil
	default:
		return ErrInvalid
	}
	if in.ActorID != "" || in.EngineeringReportID != "" || in.EngineeringBodySHA != "" || in.QAReportID != "" || in.QABodySHA != "" {
		return ErrInvalid
	}
	return nil
}
