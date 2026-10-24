package hostrouter

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// WorkUIRequest carries immutable identities only. The receiving owner selects
// provider executables, credentials and paths from its own private configuration.
type WorkUIRequest struct {
	MasterID   string `json:"master_id"`
	ProjectUID string `json:"project_uid"`
	IssueUID   string `json:"issue_uid,omitempty"`
}

var workMasterID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (v WorkUIRequest) Validate() error {
	if !workMasterID.MatchString(v.MasterID) {
		return errors.New("invalid work master identity")
	}
	issue := v.IssueUID
	if issue == "" {
		issue = v.ProjectUID
	}
	if _, err := c.KataTaskReference(v.ProjectUID, issue, "validation/repository"); err != nil {
		return errors.New("invalid immutable work identity")
	}
	return nil
}

func (v WorkUIRequest) Encode() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func DecodeWorkUIRequest(encoded string) (WorkUIRequest, error) {
	var v WorkUIRequest
	if len(encoded) > 4096 {
		return v, errors.New("work request too large")
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || validateInvocationJSON(b) != nil || decodeJSON(b, &v) != nil {
		return v, errors.New("invalid work request")
	}
	return v, v.Validate()
}

// Action captures the current registration. Existing SSH/terminal transports
// revalidate its address immediately before launch and never fall back locally.
func (v WorkUIRequest) Action(registry Registry, hostName string) (ActionRequest, error) {
	encoded, err := v.Encode()
	if err != nil {
		return ActionRequest{}, err
	}
	host, err := registry.Resolve(hostName)
	if err != nil {
		return ActionRequest{}, err
	}
	return ActionRequest{Target: Target{Host: host}, Operation: "command", Args: []string{"work-ui-exec", encoded}}, nil
}
