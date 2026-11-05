package worker

import "errors"

// WakeSummaries reads historical diagnostics without running the retired wake
// coordinator or changing its receipts.
func WakeSummaries(store WakeStore, limit int, id string) ([]WakeSummary, error) {
	records, err := store.Requests()
	if err != nil {
		return nil, err
	}
	var summaries []WakeSummary
	for _, r := range records {
		if id != "" && r.ID != id {
			continue
		}
		v, err := store.Receipt(r.ID)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, WakeSummary{ID: r.ID, Kind: r.Kind, Phase: v.Phase, Reason: v.Reason, PromptFailure: v.Job.PromptFailure, ReportAttempts: v.Job.PromptReportAttempts})
	}
	if id != "" && len(summaries) == 0 {
		return nil, errors.New("unknown wake request")
	}
	if len(summaries) > limit {
		summaries = summaries[len(summaries)-limit:]
	}
	return summaries, nil
}
