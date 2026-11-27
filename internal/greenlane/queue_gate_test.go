package greenlane

import (
	"slices"
	"strings"
	"testing"
)

const (
	gateBase  = "2222222222222222222222222222222222222222"
	gateGroup = "5555555555555555555555555555555555555555"
	gateHead  = "3333333333333333333333333333333333333333"
	jonID     = 441017
)

func gateInput() QueueGateInput {
	return QueueGateInput{
		BaseSHA: gateBase, GroupHeadSHA: gateGroup, HeadRef: "refs/heads/gh-readonly-queue/main/pr-12-" + gateBase,
		PRNumber: 12, PRHead: gateHead, EntryHead: gateGroup, EntryBase: gateBase, GroupParent: []string{gateBase},
		Queue:       QueueConfig{MergeMethod: "SQUASH", MergingStrategy: "ALLGREEN", MaxEntriesBuild: 1},
		LandedFiles: []string{"docs/a.md", "docs/old.md", "docs/new.md"}, LandedFilesKnown: true,
		EnqueuerType: "Bot", EnqueuerID: 999, HumanEnqueuerIDs: []int64{jonID},
		HoldLabels: []string{"green-lane-hold"}, LandingDriftKnown: true, DeniedPaths: DefaultDeniedPaths(),
		Evaluation: &EvaluationResult{PRNumber: 12, HeadSHA: gateHead, Rules: []RuleResult{{Name: "Review Readiness", Pass: true}, {Name: "CI", Pass: true}}},
		PRFiles:    []PRFile{{Filename: "docs/a.md"}, {Filename: "docs/new.md", PreviousFilename: "docs/old.md"}},
	}
}

func TestQueueGate(t *testing.T) {
	if err := QueueGate(gateInput()); err != nil {
		t.Fatalf("qualifying PR blocked: %v", err)
	}
	for name, tc := range map[string]struct {
		edit func(*QueueGateInput)
		want string
	}{
		"new review prose": {func(in *QueueGateInput) {
			in.Evaluation.Rules[0] = RuleResult{Name: "Review Readiness", Message: "Unclassified review bodies"}
		}, "no longer qualifies"},
		"inconclusive rule": {func(in *QueueGateInput) {
			in.Evaluation.Rules[1] = RuleResult{Name: "CI", Pass: true, Inconclusive: true}
		}, "no longer qualifies"},
		"mergeability is not exempt": {func(in *QueueGateInput) {
			in.Evaluation.Rules[0] = RuleResult{Name: "PR State", Message: "PR mergeability is unknown or conflicted"}
		}, "no longer qualifies"},
		"hold label":              {func(in *QueueGateInput) { in.Labels = []string{"Green-Lane-Hold"} }, "label"},
		"sensitive landing drift": {func(in *QueueGateInput) { in.LandingDrift = []string{"internal/apiauth/x.go"} }, "main changed sensitive"},
		"unknown landing drift":   {func(in *QueueGateInput) { in.LandingDriftKnown = false }, "what main changed"},
		"merge commit sensitive": {func(in *QueueGateInput) {
			in.LandedFiles = append(in.LandedFiles, ".github/workflows/ci.yml")
		}, "merge commit changes sensitive"},
		"merge commit unknown":      {func(in *QueueGateInput) { in.LandedFilesKnown = false }, "merge commit changes"},
		"merge commit extra file":   {func(in *QueueGateInput) { in.LandedFiles = append(in.LandedFiles, "README.md") }, "different files"},
		"merge commit missing file": {func(in *QueueGateInput) { in.LandedFiles = in.LandedFiles[:1] }, "different files"},
		"no PR files":               {func(in *QueueGateInput) { in.PRFiles, in.LandedFiles = nil, nil }, "different files"},
		"group from other entry":    {func(in *QueueGateInput) { in.EntryHead = gateHead }, "current queue entry"},
		"entry on other base":       {func(in *QueueGateInput) { in.EntryBase = gateHead }, "current queue entry"},
		"merge-commit group":        {func(in *QueueGateInput) { in.GroupParent = []string{gateBase, gateHead} }, "current queue entry"},
		"group not on base":         {func(in *QueueGateInput) { in.GroupParent = []string{gateHead} }, "current queue entry"},
		"bad PR head":               {func(in *QueueGateInput) { in.PRHead = "abc" }, "current queue entry"},
		"bad base sha":              {func(in *QueueGateInput) { in.BaseSHA = "2222" }, "full SHAs"},
		"bad group sha":             {func(in *QueueGateInput) { in.GroupHeadSHA = "" }, "full SHAs"},
		"ref names another PR":      {func(in *QueueGateInput) { in.HeadRef = "refs/heads/gh-readonly-queue/main/pr-13-" + gateBase }, "does not name"},
		"not a queue ref":           {func(in *QueueGateInput) { in.HeadRef = "refs/heads/feature" }, "does not name"},
		"merge method":              {func(in *QueueGateInput) { in.Queue.MergeMethod = "MERGE" }, "must be squash"},
		"HEADGREEN":                 {func(in *QueueGateInput) { in.Queue.MergingStrategy = "HEADGREEN" }, "must be squash"},
		"parallel builds":           {func(in *QueueGateInput) { in.Queue.MaxEntriesBuild = 5 }, "must be squash"},
		"unreadable queue config":   {func(in *QueueGateInput) { in.Queue = QueueConfig{} }, "must be squash"},
		"no evaluation":             {func(in *QueueGateInput) { in.Evaluation = nil }, "no Green Lane evaluation"},
		"evaluation of old head":    {func(in *QueueGateInput) { in.Evaluation.HeadSHA = gateBase }, "no Green Lane evaluation"},
		"evaluation of other PR":    {func(in *QueueGateInput) { in.Evaluation.PRNumber = 13 }, "no Green Lane evaluation"},
		"empty evaluation":          {func(in *QueueGateInput) { in.Evaluation.Rules = nil }, "no Green Lane evaluation"},
		"unknown enqueuer":          {func(in *QueueGateInput) { in.EnqueuerType = "" }, "who queued"},
	} {
		t.Run(name, func(t *testing.T) {
			in := gateInput()
			in.LandedFiles = slices.Clone(in.LandedFiles)
			tc.edit(&in)
			err := QueueGate(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

// A PR Jon queued by hand is his call: only the path guards apply.
func TestQueueGateHumanEnqueue(t *testing.T) {
	jon := func() QueueGateInput {
		in := gateInput()
		in.EnqueuerType, in.EnqueuerID = "User", jonID
		in.Evaluation = &EvaluationResult{PRNumber: 12, HeadSHA: gateHead, Rules: []RuleResult{{Name: "Size", Message: "too big"}}}
		in.Labels = []string{"green-lane-hold"}
		in.LandedFiles = []string{"cmd/redline/main.go"}
		return in
	}
	if err := QueueGate(jon()); err != nil {
		t.Fatalf("Jon's enqueue blocked by Green Lane rules: %v", err)
	}
	for name, edit := range map[string]func(*QueueGateInput){
		"main drift":           func(in *QueueGateInput) { in.LandingDrift = []string{".github/workflows/ci.yml"} },
		"sensitive merge":      func(in *QueueGateInput) { in.LandedFiles = []string{".github/workflows/ci.yml"} },
		"wrong group":          func(in *QueueGateInput) { in.EntryHead = gateHead },
		"bot with Jon's id":    func(in *QueueGateInput) { in.EnqueuerType = "Bot" },
		"other user":           func(in *QueueGateInput) { in.EnqueuerID = 1 },
		"user with no id":      func(in *QueueGateInput) { in.EnqueuerID = 0; in.HumanEnqueuerIDs = []int64{0} },
		"queue not configured": func(in *QueueGateInput) { in.Queue.MergingStrategy = "HEADGREEN" },
	} {
		in := jon()
		edit(&in)
		if QueueGate(in) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEnableMergeQueue(t *testing.T) {
	c := DefaultConfig()
	before := slices.Clone(c.RequiredChecks)
	c.EnableMergeQueue()
	c.EnableMergeQueue()
	if !c.MergeQueue || len(c.RequiredChecks) != len(before)+1 || c.RequiredChecks[len(before)] != QueueGateCheck {
		t.Fatalf("required checks %v", c.RequiredChecks)
	}
	if c.CheckSources[QueueGateCheck] != c.CheckSources["go"] {
		t.Fatal("queue gate must come from the CI workflow")
	}
	d := DefaultConfig()
	if slices.Contains(d.RequiredChecks, QueueGateCheck) || d.CheckSources[QueueGateCheck].AppID != 0 {
		t.Fatal("EnableMergeQueue mutated the defaults")
	}
}

func TestQueueRefPR(t *testing.T) {
	for ref, want := range map[string]int{
		"refs/heads/gh-readonly-queue/main/pr-12-" + gateBase:                   12,
		"refs/heads/gh-readonly-queue/release/v1/pr-7-" + gateBase:              7,
		"refs/heads/gh-readonly-queue/main/pr-0-" + gateBase:                    0,
		"refs/heads/gh-readonly-queue/main/pr-12-2222":                          0,
		"refs/heads/feature/pr-12-" + gateBase:                                  0,
		"refs/heads/gh-readonly-queue/main/pr-99999999999999999999-" + gateBase: 0,
	} {
		got, ok := QueueRefPR(ref)
		if got != want || ok != (want > 0) {
			t.Errorf("%s: got %d %v want %d", ref, got, ok, want)
		}
	}
}
