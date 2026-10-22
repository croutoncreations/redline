package greenlane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type receiptFixture struct {
	config         Config
	pr             PRInfo
	keys           map[string]ed25519.PrivateKey
	author, review ProvenanceReceipt
}

func newReceiptFixture(t *testing.T) receiptFixture {
	t.Helper()
	f := receiptFixture{config: DefaultConfig(), pr: passingPR(PRFile{Filename: "README.md", Additions: 2}), keys: map[string]ed25519.PrivateKey{}}
	f.pr.Repository = "owner/repo"
	for _, role := range []string{"author", "reviewer"} {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		f.keys[role] = key
		provider := "anthropic"
		app := int64(10)
		reviewer := ""
		if role == "reviewer" {
			provider = "openai"
			app = 11
			reviewer = "reviewer"
		}
		f.config.ProvenanceIssuers[role] = ReceiptIssuer{PublicKey: base64.StdEncoding.EncodeToString(pub), Role: role, Provider: provider, AppID: app, Reviewer: reviewer}
	}
	f.author = ProvenanceReceipt{Version: 1, Role: "author", Repository: f.pr.Repository, PRNumber: f.pr.Number, HeadSHA: f.pr.HeadSHA, BaseSHA: f.pr.BaseSHA, Provider: "anthropic", Model: "author-model", Harness: "trusted-author", RunID: "author-run-1", IssuedAt: time.Now().UTC(), Outcome: "authored"}
	f.review = f.author
	f.review.Role = "reviewer"
	f.review.Provider = "openai"
	f.review.Model = "review-model"
	f.review.Harness = "trusted-reviewer"
	f.review.RunID = "review-run-1"
	f.review.ReviewID = 1
	f.review.Reviewer = "reviewer"
	f.review.Outcome = "approved"
	return f
}
func (f receiptFixture) sign(t *testing.T, key string, r ProvenanceReceipt) SignedReceipt {
	t.Helper()
	payload, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return SignedReceipt{KeyID: key, Payload: payload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(f.keys[key], append([]byte(ReceiptDomain), payload...))), SourceAppID: f.config.ProvenanceIssuers[key].AppID}
}
func TestSignedProvenanceAndAdvisoryCandidates(t *testing.T) {
	f := newReceiptFixture(t)
	eval := NewEvaluator(f.config)
	res := eval.Evaluate(f.pr, nil, nil)
	if !res.AdvisoryCandidate || res.Verdict != VerdictWouldNotMerge {
		t.Fatalf("missing provenance must stay blocked but observable: %+v", res)
	}
	f.pr.Provenance = []SignedReceipt{f.sign(t, "author", f.author), f.sign(t, "reviewer", f.review)}
	res = eval.Evaluate(f.pr, nil, nil)
	if !res.AdvisoryCandidate || res.Verdict != VerdictWouldMerge {
		t.Fatalf("valid independent review should exercise positive shadow path: %+v", res)
	}
	f.pr.ReviewThreads = []ReviewThread{{ID: "unresolved", Outdated: true}}
	res = eval.Evaluate(f.pr, nil, nil)
	if res.AdvisoryCandidate || res.Verdict != VerdictWouldNotMerge {
		t.Fatal("valid provenance bypassed feedback readiness")
	}
}
func TestProvenanceRejectsUntrustedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*receiptFixture)
	}{
		{"different repo", func(f *receiptFixture) { f.review.Repository = "other/repo" }},
		{"different PR", func(f *receiptFixture) { f.review.PRNumber++ }},
		{"stale head", func(f *receiptFixture) { f.review.HeadSHA = "older" }},
		{"stale base", func(f *receiptFixture) { f.review.BaseSHA = "older" }},
		{"same provider", func(f *receiptFixture) {
			i := f.config.ProvenanceIssuers["reviewer"]
			i.Provider = "anthropic"
			f.config.ProvenanceIssuers["reviewer"] = i
			f.review.Provider = "anthropic"
		}},
		{"provider claim outside issuer", func(f *receiptFixture) { f.review.Provider = "other" }},
		{"unknown version", func(f *receiptFixture) { f.review.Version = 2 }},
		{"no model", func(f *receiptFixture) { f.review.Model = "" }},
		{"future receipt", func(f *receiptFixture) { f.review.IssuedAt = time.Now().Add(time.Hour) }},
		{"not approved", func(f *receiptFixture) { f.review.Outcome = "commented" }},
		{"wrong review", func(f *receiptFixture) { f.review.ReviewID = 100 }},
		{"dismissed review", func(f *receiptFixture) { f.pr.Reviews[0].State = "DISMISSED" }},
		{"approval superseded", func(f *receiptFixture) {
			f.pr.Reviews = append(f.pr.Reviews, Review{ID: 2, User: "reviewer", State: "CHANGES_REQUESTED", CommitID: f.pr.HeadSHA})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReceiptFixture(t)
			tc.edit(&f)
			f.pr.Provenance = []SignedReceipt{f.sign(t, "author", f.author), f.sign(t, "reviewer", f.review)}
			if NewEvaluator(f.config).checkProvenance(f.pr).Pass {
				t.Fatal("untrusted provenance passed")
			}
		})
	}
	for _, kind := range []string{"forged signature", "untrusted app", "unknown key", "missing author", "missing reviewer", "altered payload"} {
		t.Run(kind, func(t *testing.T) {
			f := newReceiptFixture(t)
			a := f.sign(t, "author", f.author)
			r := f.sign(t, "reviewer", f.review)
			switch kind {
			case "forged signature":
				r.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
			case "untrusted app":
				r.SourceAppID++
			case "unknown key":
				r.KeyID = "unknown"
			case "altered payload":
				r.Payload = []byte(strings.ReplaceAll(string(r.Payload), "openai", "other"))
			}
			f.pr.Provenance = []SignedReceipt{a, r}
			if kind == "missing author" {
				f.pr.Provenance = []SignedReceipt{r}
			}
			if kind == "missing reviewer" {
				f.pr.Provenance = []SignedReceipt{a}
			}
			if NewEvaluator(f.config).checkProvenance(f.pr).Pass {
				t.Fatal("untrusted provenance passed")
			}
		})
	}
}
func TestMissingProvenanceCannotAuthorizeCodeOrLocks(t *testing.T) {
	f := newReceiptFixture(t)
	f.pr.Files = []PRFile{{Filename: "internal/fix.go", Additions: 2}}
	r := NewEvaluator(f.config).Evaluate(f.pr, &ReproResult{Pass: true, HeadSHA: "older", BaseSHA: f.pr.BaseSHA}, nil)
	if r.AdvisoryCandidate {
		t.Fatal("stale repro admitted candidate")
	}
	f.pr.Files = []PRFile{{Filename: "go.mod", Patch: goModBump}, {Filename: "go.sum", Patch: "@@ -1 +1 @@\n+example.org/x v1.0.0 h1:abc="}}
	r = NewEvaluator(f.config).Evaluate(f.pr, nil, nil)
	if r.AdvisoryCandidate {
		t.Fatal("unverified lockfile admitted candidate")
	}
}
