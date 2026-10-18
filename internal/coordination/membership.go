package coordination

type SetMembers struct {
	ThreadID        string   `json:"thread_id"`
	Members         []string `json:"members"`
	ExpectedMembers []string `json:"expected_members"`
	RequestID       string   `json:"request_id"`
}
