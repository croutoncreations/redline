package greenlane

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ReviewSnapshot hashes only GitHub-owned review/thread state. The publisher
// repeats this collection immediately before publishing, including changes
// which do not bump the PR's updated_at timestamp.
func ReviewSnapshot(pr PRInfo) string {
	type review struct {
		ID     int64  `json:"id"`
		User   string `json:"user"`
		State  string `json:"state"`
		Commit string `json:"commit"`
		Body   string `json:"body"`
	}
	type thread struct {
		ID       string `json:"id"`
		Resolved bool   `json:"resolved"`
		Outdated bool   `json:"outdated"`
	}
	state := struct {
		Automated []AutomatedReviewComment `json:"automated"`
		Threads   []thread                 `json:"threads"`
		Reviews   []review                 `json:"reviews"`
	}{Threads: []thread{}, Reviews: []review{}, Automated: []AutomatedReviewComment{}}
	for _, c := range pr.AutomatedReviews {
		if TrustedAutomatedSummary(c) {
			c.Body = base64.StdEncoding.EncodeToString([]byte(c.Body))
			state.Automated = append(state.Automated, c)
		}
	}
	sort.Slice(state.Automated, func(i, j int) bool { return state.Automated[i].ID < state.Automated[j].ID })
	for _, r := range pr.Reviews {
		state.Reviews = append(state.Reviews, review{r.ID, r.User, r.State, r.CommitID, base64.StdEncoding.EncodeToString([]byte(r.Body))})
	}
	for _, r := range pr.ReviewThreads {
		state.Threads = append(state.Threads, thread{r.ID, r.Resolved, r.Outdated})
	}
	sort.Slice(state.Reviews, func(i, j int) bool { return state.Reviews[i].ID < state.Reviews[j].ID })
	sort.Slice(state.Threads, func(i, j int) bool { return state.Threads[i].ID < state.Threads[j].ID })
	payload, _ := json.Marshal(state)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
