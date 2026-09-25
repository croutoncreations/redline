package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/domain"
)

// maxInlinePromptBytes bounds prompts read from flags or stdin so a runaway
// pipe cannot queue an unbounded task body.
const maxInlinePromptBytes = 256 * 1024

// taskFlagValues holds the flag-based task definition shared by `task add`
// (without --file) and `later`.
type taskFlagValues struct {
	name       string
	prompt     string
	promptFile string
	profile    string
	defaultPro string
	harness    string
	cwd        string
	taskType   string
	tier       string
	priority   int
	interval   string
	disabled   bool
	jsonOutput bool
}

type laterResult struct {
	Task              domain.Task `json:"task"`
	ProfileResolution string      `json:"profile_resolution"`
	Repository        string      `json:"repository,omitempty"`
}

// runTaskAddFlags creates a task from flags instead of a YAML file so hooks and
// scripts can queue work without writing temporary definitions.
func runTaskAddFlags(client apiclient.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	values := taskFlagValues{}
	flags.StringVar(&values.name, "name", "", "task name")
	flags.StringVar(&values.prompt, "prompt", "", "inline agent instructions; '-' reads them from stdin")
	flags.StringVar(&values.promptFile, "prompt-file", "", "workspace-relative prompt file read when the task runs")
	flags.StringVar(&values.profile, "profile", "", "execution profile ID, or 'auto' to match the current repository")
	flags.StringVar(&values.defaultPro, "default-profile", "", "profile used when --profile auto finds no repository match")
	flags.StringVar(&values.harness, "harness", "claude-code", "harness type matched by --profile auto")
	flags.StringVar(&values.cwd, "cwd", "", "directory used by --profile auto (default: current directory)")
	flags.StringVar(&values.taskType, "type", string(domain.OneOff), "one_off or recurring")
	flags.StringVar(&values.tier, "tier", string(domain.DispatchBehind), "behind, well_behind, or expiring")
	flags.IntVar(&values.priority, "priority", 50, "priority within the unlocked tier")
	flags.StringVar(&values.interval, "min-interval", "", "minimum interval between recurring runs, such as 6h or 7d")
	flags.BoolVar(&values.disabled, "disabled", false, "create the task disabled as a draft")
	flags.BoolVar(&values.jsonOutput, "json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument %q; quote the prompt and pass it with --prompt\n", flags.Arg(0))
		return 1
	}
	if strings.TrimSpace(values.name) == "" {
		fmt.Fprintln(stderr, "--name is required (or use --file)")
		return 1
	}
	if values.profile == "" {
		fmt.Fprintln(stderr, "--profile is required; pass a profile ID or 'auto'")
		return 1
	}
	if values.prompt == "-" {
		prompt, err := readBoundedPrompt(stdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		values.prompt = prompt
	}
	if strings.TrimSpace(values.prompt) == "" && strings.TrimSpace(values.promptFile) == "" {
		fmt.Fprintln(stderr, "--prompt or --prompt-file is required")
		return 1
	}
	result, err := createTaskFromFlags(context.Background(), client, values)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if values.jsonOutput {
		writeJSON(stdout, result)
	} else {
		fmt.Fprintf(stdout, "created task %s (profile %s, tier %s)\n",
			result.Task.ID, result.Task.ExecutionProfileID, result.Task.DispatchTier)
	}
	return 0
}

// runLater queues free text as a one-off task for the repository in the
// current directory. It never dispatches: the scheduler admits the task only
// when the provider is behind pace and above its reserve.
func runLater(client apiclient.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("later", flag.ContinueOnError)
	flags.SetOutput(stderr)
	values := taskFlagValues{taskType: string(domain.OneOff)}
	flags.StringVar(&values.name, "name", "", "task name (default: derived from the text)")
	flags.StringVar(&values.profile, "profile", "auto", "execution profile ID, or 'auto' to match the current repository")
	flags.StringVar(&values.defaultPro, "default-profile", "", "profile used when no profile matches the current repository")
	flags.StringVar(&values.harness, "harness", "claude-code", "harness type matched by --profile auto")
	flags.StringVar(&values.cwd, "cwd", "", "directory used to find the repository (default: current directory)")
	flags.StringVar(&values.tier, "tier", string(domain.DispatchBehind), "behind, well_behind, or expiring")
	flags.IntVar(&values.priority, "priority", 50, "priority within the unlocked tier")
	flags.BoolVar(&values.jsonOutput, "json", false, "emit JSON")
	flags.Usage = func() {
		fmt.Fprintln(stderr, `usage: redline later [flags] "<what to do>"   (or "-" to read the text from stdin)`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 1
	}
	text := strings.Join(flags.Args(), " ")
	if text == "-" {
		prompt, err := readBoundedPrompt(stdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		text = prompt
	}
	text = strings.TrimSpace(text)
	if text == "" {
		flags.Usage()
		return 1
	}
	if len(text) > maxInlinePromptBytes {
		fmt.Fprintf(stderr, "task text exceeds %d bytes\n", maxInlinePromptBytes)
		return 1
	}
	values.prompt = text
	if strings.TrimSpace(values.name) == "" {
		values.name = laterTaskName(text)
	}
	result, err := createTaskFromFlags(context.Background(), client, values)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if values.jsonOutput {
		writeJSON(stdout, result)
		return 0
	}
	fmt.Fprintf(stdout, "Queued Redline task %s (tier %s, profile %s).\n",
		result.Task.ID, result.Task.DispatchTier, result.Task.ExecutionProfileID)
	fmt.Fprintln(stdout, "It runs only when usage is behind pace and above your reserve. It is never forced.")
	return 0
}

func createTaskFromFlags(ctx context.Context, client apiclient.Client, values taskFlagValues) (laterResult, error) {
	profileID, resolution, repository, err := resolveTaskProfile(ctx, client, values)
	if err != nil {
		return laterResult{}, err
	}
	body := map[string]any{
		"name":                 strings.TrimSpace(values.name),
		"priority":             values.priority,
		"execution_profile_id": profileID,
		"type":                 values.taskType,
		"dispatch_tier":        values.tier,
	}
	if values.prompt != "" {
		body["prompt"] = values.prompt
	}
	if values.promptFile != "" {
		body["prompt_file"] = values.promptFile
	}
	if values.interval != "" {
		body["min_interval"] = values.interval
	}
	if values.disabled {
		body["enabled"] = false
	}
	var task domain.Task
	if err := client.Do(ctx, http.MethodPost, "/v1/tasks", body, &task); err != nil {
		return laterResult{}, fmt.Errorf("create task: %w", err)
	}
	return laterResult{Task: task, ProfileResolution: resolution, Repository: repository}, nil
}

// resolveTaskProfile returns the profile ID and how it was chosen. An explicit
// ID wins. "auto" matches the git top level of cwd against profile
// repositories for the requested harness, then falls back to the default.
func resolveTaskProfile(ctx context.Context, client apiclient.Client, values taskFlagValues) (string, string, string, error) {
	if values.profile != "" && values.profile != "auto" {
		return values.profile, "explicit", "", nil
	}
	directory := values.cwd
	if directory == "" {
		var err error
		if directory, err = os.Getwd(); err != nil {
			return "", "", "", fmt.Errorf("resolve profile: %w", err)
		}
	}
	repository, repoErr := gitTopLevel(ctx, directory)
	if repoErr == nil {
		var profiles []domain.ExecutionProfile
		if err := client.Do(ctx, http.MethodGet, "/v1/profiles", nil, &profiles); err != nil {
			return "", "", "", fmt.Errorf("list profiles: %w", err)
		}
		matches := matchRepositoryProfiles(profiles, repository, values.harness)
		switch len(matches) {
		case 1:
			return matches[0], "repository", repository, nil
		case 0:
		default:
			if values.defaultPro != "" && contains(matches, values.defaultPro) {
				return values.defaultPro, "default", repository, nil
			}
			return "", "", repository, fmt.Errorf(
				"several %s profiles use %s (%s); pass --profile or set a default profile",
				values.harness, repository, strings.Join(matches, ", "))
		}
	}
	if values.defaultPro != "" {
		return values.defaultPro, "default", repository, nil
	}
	if repoErr != nil {
		return "", "", "", fmt.Errorf("%s is not inside a git repository and no default profile is set; pass --profile or --default-profile", directory)
	}
	return "", "", repository, fmt.Errorf(
		"no %s execution profile uses repository %s; create one in Redline (or run /redline:setup), or pass --profile",
		values.harness, repository)
}

func matchRepositoryProfiles(profiles []domain.ExecutionProfile, repository, harness string) []string {
	want := canonicalPath(repository)
	matches := make([]string, 0)
	for _, profile := range profiles {
		if profile.HarnessType != harness || strings.TrimSpace(profile.Repository) == "" {
			continue
		}
		if samePath(canonicalPath(expandHome(profile.Repository)), want) {
			matches = append(matches, profile.ID)
		}
	}
	sort.Strings(matches)
	return matches
}

func gitTopLevel(ctx context.Context, directory string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", directory, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("find git repository for %s: %w", directory, err)
	}
	top := strings.TrimSpace(string(output))
	if top == "" {
		return "", errors.New("git returned an empty repository path")
	}
	return filepath.FromSlash(top), nil
}

func canonicalPath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}
	return cleaned
}

// samePath compares canonical paths, case-insensitively on Windows where the
// filesystem is case-insensitive and git may report a different drive case.
func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// laterTaskName derives a short, single-line task name from free text.
func laterTaskName(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	line = strings.Join(strings.Fields(line), " ")
	const limit = 60
	if utf8.RuneCountInString(line) > limit {
		runes := []rune(line)
		line = strings.TrimSpace(string(runes[:limit-3])) + "..."
	}
	return "Later: " + line
}

func readBoundedPrompt(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", errors.New("no stdin available for '-'")
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxInlinePromptBytes+1))
	if err != nil {
		return "", fmt.Errorf("read prompt from stdin: %w", err)
	}
	if len(data) > maxInlinePromptBytes {
		return "", fmt.Errorf("prompt from stdin exceeds %d bytes", maxInlinePromptBytes)
	}
	return string(data), nil
}

// runWatchEvent is one finished run, printed as a single JSON line.
type runWatchEvent struct {
	TaskID      string     `json:"task_id"`
	Task        string     `json:"task,omitempty"`
	RunID       string     `json:"run_id"`
	Status      string     `json:"status"`
	Summary     string     `json:"summary,omitempty"`
	PRURL       string     `json:"pr_url,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// runWatch prints one JSON line per run that finishes after the watch starts.
// It polls the loopback API at a modest interval; runs already finished when
// the watch starts are never reported.
func runWatch(ctx context.Context, client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run watch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonl := flags.Bool("jsonl", false, "emit one JSON object per line (required)")
	interval := flags.Duration("interval", 10*time.Second, "poll interval (minimum 100ms)")
	count := flags.Int("count", 0, "exit after this many finished runs; 0 watches until interrupted")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if !*jsonl {
		fmt.Fprintln(stderr, "usage: redline run watch --jsonl [--interval 10s] [--count N]")
		return 1
	}
	if *interval < 100*time.Millisecond || *count < 0 {
		fmt.Fprintln(stderr, "--interval must be at least 100ms and --count must not be negative")
		return 1
	}
	const window = 50
	listPath := "/v1/runs?limit=" + fmt.Sprint(window)
	var initial []domain.Run
	if err := client.Do(ctx, http.MethodGet, listPath, nil, &initial); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	reported := make(map[string]bool, len(initial))
	for _, run := range initial {
		if runFinished(run.State) {
			reported[run.ID] = true
		}
	}
	names := map[string]string{}
	emitted := 0
	failing := false
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
		var runs []domain.Run
		if err := client.Do(ctx, http.MethodGet, listPath, nil, &runs); err != nil {
			if ctx.Err() != nil {
				return 0
			}
			if !failing {
				fmt.Fprintf(stderr, "redline run watch: %v (retrying)\n", err)
				failing = true
			}
			continue
		}
		failing = false
		// The API lists newest first; report oldest first.
		for index := len(runs) - 1; index >= 0; index-- {
			run := runs[index]
			if !runFinished(run.State) || reported[run.ID] {
				continue
			}
			reported[run.ID] = true
			if _, ok := names[run.TaskID]; !ok {
				names[run.TaskID] = lookupTaskName(ctx, client, run.TaskID)
			}
			writeJSONLine(stdout, runWatchEvent{
				TaskID: run.TaskID, Task: names[run.TaskID], RunID: run.ID,
				Status: string(run.State), Summary: truncateRunes(run.Summary, 200),
				PRURL: pullRequestURL(run.Artifacts), CompletedAt: run.CompletedAt,
			})
			emitted++
			if *count > 0 && emitted >= *count {
				return 0
			}
		}
	}
}

func runFinished(state domain.RunState) bool {
	return state == domain.RunCompleted || state == domain.RunFailed
}

func lookupTaskName(ctx context.Context, client apiclient.Client, taskID string) string {
	var task domain.Task
	if err := client.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID), nil, &task); err != nil {
		return ""
	}
	return task.Name
}

func pullRequestURL(artifacts []domain.RunArtifact) string {
	for _, artifact := range artifacts {
		if artifact.Type == "pull_request" && artifact.URL != "" {
			return artifact.URL
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-3]) + "..."
}
