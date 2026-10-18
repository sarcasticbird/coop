package coordination

type ArtifactLink struct {
	Goal                  GoalRef `json:"goal"`
	Kind                  string  `json:"kind"`
	URL                   string  `json:"url"`
	TaskURL               string  `json:"task_url,omitempty"`
	AssignmentSHA         string  `json:"assignment_sha256"`
	ExpectedAssignmentSHA string  `json:"expected_assignment_sha256,omitempty"`
	RequestID             string  `json:"request_id"`
}
type ArtifactQuery struct {
	Goal  GoalRef `json:"goal"`
	After string  `json:"after,omitempty"`
}
type ArtifactPage struct {
	Links []ArtifactLink `json:"links"`
	Next  string         `json:"next,omitempty"`
}
