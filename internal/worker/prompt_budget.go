package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const MaxPromptBytes = 32 << 10

// PromptSizeError contains measurements only, never quoted input or credentials.
// Section counts include JSON keys, escaping, separators and instruction framing.
type PromptSizeError struct {
	ActualBytes int                 `json:"actual_bytes"`
	LimitBytes  int                 `json:"limit_bytes"`
	JobID       string              `json:"job_id"`
	SourceURL   string              `json:"source_url"`
	Sections    []PromptSectionSize `json:"sections"`
}

type PromptSectionSize struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

func (e *PromptSizeError) Error() string {
	var sections []string
	for _, s := range e.Sections {
		sections = append(sections, fmt.Sprintf("%s=%d", s.Name, s.Bytes))
	}
	return fmt.Sprintf("prompt_size: %d bytes exceeds %d-byte limit (%s). Split the source into smaller scoped inputs; preserve this blocked record, do not replay it or request redundant approval.", e.ActualBytes, e.LimitBytes, strings.Join(sections, ", "))
}

func validatePromptFailure(e *PromptSizeError) error {
	if e == nil {
		return nil
	}
	if e.LimitBytes != MaxPromptBytes || e.ActualBytes <= e.LimitBytes || e.ActualBytes > MaxSnapshot*4 || !hexSHA.MatchString(e.JobID) || !validGitHubURL(e.SourceURL) || len(e.Sections) == 0 || len(e.Sections) > 24 {
		return errors.New("invalid prompt-size diagnostic")
	}
	total := 0
	seen := map[string]bool{}
	for _, s := range e.Sections {
		switch s.Name {
		case "instructions", "identity", "policy", "framing", "Job", "Goal", "Task", "PR", "Event", "Approval", "Owner", "ResultMarker", "ReviewHandoffs", "PlanningGoals", "RequestID", "Kind", "Prompt", "Source", "ReplyToID":
		default:
			return errors.New("invalid prompt-size section")
		}
		if seen[s.Name] || s.Bytes < 0 || s.Bytes > e.ActualBytes-total {
			return errors.New("invalid prompt-size section counts")
		}
		seen[s.Name] = true
		total += s.Bytes
	}
	if total != e.ActualBytes {
		return errors.New("invalid prompt-size total")
	}
	return nil
}

func validateJobPromptFailure(j Job) error {
	if err := validatePromptFailure(j.PromptFailure); err != nil {
		return err
	}
	if j.PromptReportAttempts < 0 || j.PromptReportAttempts > 3 || j.PromptReportAttempts > 0 && j.PromptFailure == nil || j.PromptFailure != nil && j.PromptFailure.JobID != j.ID {
		return errors.New("invalid prompt-size job binding")
	}
	return nil
}

type promptSection struct{ name, text string }
type promptField struct {
	name  string
	value any
}

// Keep field-level accounting exact without retaining a second copy of the
// quoted text in diagnostics. All names come from the builders, not GitHub.
func promptInput(fields ...promptField) ([]promptSection, error) {
	sections := []promptSection{{"framing", "{"}}
	for i, field := range fields {
		raw, err := json.Marshal(field.value)
		if err != nil {
			return nil, errors.New("cannot build worker input")
		}
		prefix := ""
		if i > 0 {
			prefix = ","
		}
		key, _ := json.Marshal(field.name)
		sections = append(sections, promptSection{field.name, prefix + string(key) + ":" + string(raw)})
	}
	return append(sections, promptSection{"framing", "}"}), nil
}

func assemblePrompt(jobID, sourceURL string, sections ...promptSection) (string, error) {
	var prompt strings.Builder
	var sizes []PromptSectionSize
	indices := map[string]int{}
	for _, s := range sections {
		prompt.WriteString(s.text)
		if i, ok := indices[s.name]; ok {
			sizes[i].Bytes += len(s.text)
		} else {
			indices[s.name] = len(sizes)
			sizes = append(sizes, PromptSectionSize{s.name, len(s.text)})
		}
	}
	if prompt.Len() > MaxPromptBytes {
		return "", &PromptSizeError{prompt.Len(), MaxPromptBytes, jobID, sourceURL, sizes}
	}
	return prompt.String(), nil
}

// Other goal bodies are discovery context, not authority for this selected turn.
type promptGoalRef struct {
	URL, BodySHA            string
	Repositories            []string
	CreatedAt, LastEditedAt string
	Readable                bool
}

type promptBodyRef struct{ GoalURL, BodySHA string }

type promptEvent struct {
	Event
	BodyRef *promptBodyRef `json:",omitempty"`
}

type promptSource struct {
	ConversationSource
	BodyRef *promptBodyRef `json:",omitempty"`
}

func canonicalBody(body *string, goal *Goal) *promptBodyRef {
	if goal != nil && *body == goal.Body {
		*body = ""
		return &promptBodyRef{goal.URL, goal.BodySHA}
	}
	return nil
}
