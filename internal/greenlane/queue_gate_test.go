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
		Queue:            QueueConfig{MergeMethod: "SQUASH", MergingStrategy: "ALLGREEN", MaxEntriesBuild: 1},
		LandedFiles:      []string{"docs/a.md", "docs/new.md", "docs/old.md"},
		Landed:           []FileChange{{Path: "docs/a.md", Status: "modified", Blob: "b1"}, {Path: "docs/new.md", Previous: "docs/old.md", Status: "renamed", Blob: "b2"}},
		LandedFilesKnown: true,
		PRChanges:        []FileChange{{Path: "docs/new.md", Previous: "docs/old.md", Status: "renamed", Blob: "b2"}, {Path: "docs/a.md", Status: "modified", Blob: "b1"}},
		PRChangesKnown:   true,
		EnqueuerType:     "Bot", EnqueuerLogin: "green-lane-merge", EnqueuerID: 999,
		HumanEnqueuerIDs: []int64{jonID}, GreenLaneBots: []string{"green-lane-merge", "green-lane-merge[bot]"},
		HoldLabels: []string{"green-lane-hold"}, LandingDriftKnown: true, DeniedPaths: DefaultDeniedPaths(),
		Evaluation: &EvaluationResult{PRNumber: 12, HeadSHA: gateHead, Rules: []RuleResult{{Name: "Review Readiness", Pass: true}, {Name: "CI", Pass: true}}},
	}
}

func cloneGate(in QueueGateInput) QueueGateInput {
	in.LandedFiles = slices.Clone(in.LandedFiles)
	in.Landed = slices.Clone(in.Landed)
	in.PRChanges = slices.Clone(in.PRChanges)
	e := *in.Evaluation
	e.Rules = slices.Clone(e.Rules)
	in.Evaluation = &e
	return in
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
		"merge commit unknown": {func(in *QueueGateInput) { in.LandedFilesKnown = false }, "merge commit changes"},
		"merge commit extra file": {func(in *QueueGateInput) {
			in.Landed = append(in.Landed, FileChange{Path: "README.md", Status: "modified", Blob: "x"})
		}, "does not land"},
		"merge commit missing file": {func(in *QueueGateInput) { in.Landed = in.Landed[:1] }, "does not land"},
		"older head's content":      {func(in *QueueGateInput) { in.Landed[0].Blob = "old" }, "does not land"},
		"different status":          {func(in *QueueGateInput) { in.Landed[0].Status = "added" }, "does not land"},
		"different rename source":   {func(in *QueueGateInput) { in.Landed[1].Previous = "docs/other.md" }, "does not land"},
		"duplicate landed path": {func(in *QueueGateInput) {
			// Same count, every PR path present: only the duplicate check catches it.
			in.PRChanges = []FileChange{in.Landed[0], {Path: "docs/b.md", Status: "added", Blob: "z"}}
			in.Landed = []FileChange{in.Landed[0], {Path: "docs/b.md", Status: "added", Blob: "z"}, in.Landed[0]}
			in.PRChanges = append(in.PRChanges, FileChange{Path: "docs/c.md", Status: "added", Blob: "y"})
		}, "does not land"},
		"PR changes unknown":      {func(in *QueueGateInput) { in.PRChangesKnown = false }, "does not land"},
		"no PR changes":           {func(in *QueueGateInput) { in.PRChanges, in.Landed = nil, nil }, "does not land"},
		"other bot":               {func(in *QueueGateInput) { in.EnqueuerLogin = "github-actions" }, "neither Jon nor Green Lane"},
		"user named like the App": {func(in *QueueGateInput) { in.EnqueuerType = "User" }, "neither Jon nor Green Lane"},
		"other user":              {func(in *QueueGateInput) { in.EnqueuerType, in.EnqueuerLogin, in.EnqueuerID = "User", "newwriter", 5 }, "neither Jon nor Green Lane"},
		"unknown enqueuer":        {func(in *QueueGateInput) { in.EnqueuerType = "" }, "who queued"},
		"group from other entry":  {func(in *QueueGateInput) { in.EntryHead = gateHead }, "current queue entry"},
		"entry on other base":     {func(in *QueueGateInput) { in.EntryBase = gateHead }, "current queue entry"},
		"merge-commit group":      {func(in *QueueGateInput) { in.GroupParent = []string{gateBase, gateHead} }, "current queue entry"},
		"group not on base":       {func(in *QueueGateInput) { in.GroupParent = []string{gateHead} }, "current queue entry"},
		"bad PR head":             {func(in *QueueGateInput) { in.PRHead = "abc" }, "current queue entry"},
		"bad base sha":            {func(in *QueueGateInput) { in.BaseSHA = "2222" }, "full SHAs"},
		"bad group sha":           {func(in *QueueGateInput) { in.GroupHeadSHA = "" }, "full SHAs"},
		"ref names another PR":    {func(in *QueueGateInput) { in.HeadRef = "refs/heads/gh-readonly-queue/main/pr-13-" + gateBase }, "does not name"},
		"not a queue ref":         {func(in *QueueGateInput) { in.HeadRef = "refs/heads/feature" }, "does not name"},
		"merge method":            {func(in *QueueGateInput) { in.Queue.MergeMethod = "MERGE" }, "must be squash"},
		"HEADGREEN":               {func(in *QueueGateInput) { in.Queue.MergingStrategy = "HEADGREEN" }, "must be squash"},
		"parallel builds":         {func(in *QueueGateInput) { in.Queue.MaxEntriesBuild = 5 }, "must be squash"},
		"unreadable queue config": {func(in *QueueGateInput) { in.Queue = QueueConfig{} }, "must be squash"},
		"no evaluation":           {func(in *QueueGateInput) { in.Evaluation = nil }, "no Green Lane evaluation"},
		"evaluation of old head":  {func(in *QueueGateInput) { in.Evaluation.HeadSHA = gateBase }, "no Green Lane evaluation"},
		"evaluation of other PR":  {func(in *QueueGateInput) { in.Evaluation.PRNumber = 13 }, "no Green Lane evaluation"},
		"empty evaluation":        {func(in *QueueGateInput) { in.Evaluation.Rules = nil }, "no Green Lane evaluation"},
	} {
		t.Run(name, func(t *testing.T) {
			in := cloneGate(gateInput())
			tc.edit(&in)
			err := QueueGate(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

// A file main also changed since the PR's base is merged by the queue, so
// its landed content legitimately differs; every other file must match.
func TestQueueGateMergedFiles(t *testing.T) {
	in := cloneGate(gateInput())
	in.LandingDrift = []string{"docs/a.md"}
	in.Landed[0].Blob = "merged"
	if err := QueueGate(in); err != nil {
		t.Fatalf("file merged with main blocked: %v", err)
	}
	in.Landed[1].Blob = "other"
	if QueueGate(in) == nil {
		t.Fatal("content change in a file main didn't touch accepted")
	}
	// A rename whose source main changed is merged too.
	in = cloneGate(gateInput())
	in.LandingDrift = []string{"docs/old.md"}
	in.Landed[1].Blob = "merged"
	if err := QueueGate(in); err != nil {
		t.Fatalf("rename merged with main blocked: %v", err)
	}
}

// A PR Jon queued by hand is his call: only the path and binding guards apply.
func TestQueueGateHumanEnqueue(t *testing.T) {
	jon := func() QueueGateInput {
		in := cloneGate(gateInput())
		in.EnqueuerType, in.EnqueuerLogin, in.EnqueuerID = "User", "jfox85", jonID
		in.Evaluation, in.PRChanges, in.PRChangesKnown = nil, nil, false
		in.Labels = []string{"green-lane-hold"}
		in.LandedFiles = []string{"cmd/redline/main.go"}
		return in
	}
	if err := QueueGate(jon()); err != nil {
		t.Fatalf("Jon's enqueue blocked by Green Lane rules: %v", err)
	}
	// Denied-path PRs are exactly the ones only Jon merges, so his own
	// enqueue of one must pass (this blocked #295 and #296 on Nov 28).
	denied := jon()
	denied.LandedFiles, denied.LandedFilesKnown = []string{".github/workflows/green-lane-live.yml", "internal/greenlane/queue_gate.go"}, false
	if err := QueueGate(denied); err != nil {
		t.Fatalf("Jon's enqueue of a denied-path PR blocked: %v", err)
	}
	// The App's enqueue of the same merge is still refused.
	app := cloneGate(gateInput())
	app.LandedFiles = append(app.LandedFiles, ".github/workflows/green-lane-live.yml")
	if QueueGate(app) == nil {
		t.Fatal("App enqueue of a denied-path merge accepted")
	}
	for name, edit := range map[string]func(*QueueGateInput){
		"main drift":           func(in *QueueGateInput) { in.LandingDrift = []string{".github/workflows/ci.yml"} },
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

func TestQueueGateAppLogins(t *testing.T) {
	in := cloneGate(gateInput())
	in.EnqueuerLogin = "green-lane-merge[bot]"
	if err := QueueGate(in); err != nil {
		t.Fatalf("App with [bot] suffix rejected: %v", err)
	}
	if !in.GreenLaneEnqueue() || in.HumanEnqueue() {
		t.Fatal("App enqueue misclassified")
	}
	in.Labels = []string{"green-lane-hold"}
	if QueueGate(in) == nil {
		t.Fatal("App enqueue skipped the rules")
	}
	in.GreenLaneBots = []string{""}
	in.EnqueuerLogin = ""
	if in.GreenLaneEnqueue() {
		t.Fatal("empty login matched")
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
	// An override that already requires the gate still gets its source.
	o := DefaultConfig()
	o.RequiredChecks = append(slices.Clone(o.RequiredChecks), QueueGateCheck)
	o.EnableMergeQueue()
	if o.CheckSources[QueueGateCheck] != o.CheckSources["go"] || len(o.RequiredChecks) != len(before)+1 {
		t.Fatalf("pre-listed gate: sources %v, checks %v", o.CheckSources[QueueGateCheck], o.RequiredChecks)
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
