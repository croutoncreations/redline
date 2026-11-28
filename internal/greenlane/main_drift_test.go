package greenlane

import (
	"strings"
	"testing"
)

func TestMainDriftRule(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MergeQueue = true
	e := NewEvaluator(cfg)
	for _, tc := range []struct {
		name  string
		drift []string
		known bool
		pass  bool
		inc   bool
	}{
		{"current with main", nil, true, true, false},
		{"main changed unrelated code", []string{"internal/cli/x.go", "docs/a.md"}, true, true, false},
		{"main changed an auth path", []string{"internal/cli/x.go", "internal/apiauth/check.go"}, true, false, false},
		{"main changed a workflow", []string{".github/workflows/ci.yml"}, true, false, false},
		{"main changed the lane itself", []string{"internal/greenlane/rules.go"}, true, false, false},
		{"drift unknown", nil, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.checkMainDrift(PRInfo{MainDrift: tc.drift, MainDriftKnown: tc.known})
			if r.Pass != tc.pass || r.Inconclusive != tc.inc {
				t.Fatalf("pass=%v inconclusive=%v (%s)", r.Pass, r.Inconclusive, r.Message)
			}
		})
	}
}

func TestMainDriftOnlyInQueueMode(t *testing.T) {
	p := passingPR(PRFile{Filename: "README.md", Status: "modified", Additions: 1, Changes: 1})
	direct := NewEvaluator(DefaultConfig()).Evaluate(p, nil, nil)
	for _, r := range direct.Rules {
		if r.Name == "Main Drift" {
			t.Fatal("Main Drift must not apply in direct mode")
		}
	}
	cfg := DefaultConfig()
	cfg.MergeQueue = true
	queued := NewEvaluator(cfg).Evaluate(p, nil, nil)
	if r := ruleByName(t, queued, "Main Drift"); r.Pass || !r.Inconclusive || !strings.Contains(r.Message, "Could not list") {
		t.Fatalf("queue mode without drift evidence must be inconclusive: %+v", r)
	}
	if queued.Verdict == VerdictWouldMerge {
		t.Fatal("unknown drift must block")
	}
	p.MainDriftKnown = true
	p.MainDrift = []string{"internal/cli/x.go"}
	if got := NewEvaluator(cfg).Evaluate(p, nil, nil); ruleByName(t, got, "Main Drift").Pass != true {
		t.Fatal("harmless drift must pass")
	}
}
