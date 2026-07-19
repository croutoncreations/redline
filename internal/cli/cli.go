package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/apiclient"
	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	autoscheduler "github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
	"gopkg.in/yaml.v3"
)

type decisionResponse struct {
	Snapshot decision.UsageSnapshot `json:"snapshot"`
	Result   decision.Result        `json:"result"`
}

type schedulerResponse struct {
	Snapshot     decision.UsageSnapshot `json:"snapshot"`
	Result       decision.Result        `json:"result"`
	SelectedTask *domain.Task           `json:"selected_task,omitempty"`
	Run          *domain.Run            `json:"run,omitempty"`
}

func Run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	global := flag.NewFlagSet("redline", flag.ContinueOnError)
	global.SetOutput(stderr)
	configPath := global.String("config", "redline.yaml", "service configuration file")
	apiURL := global.String("api", "http://127.0.0.1:7436", "Redline service API URL")
	if err := global.Parse(args); err != nil {
		return 1
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(stderr, "usage: redline [--api URL] <serve|health|decision|status|calibration|capacity|token|usage|task|profile|scheduler|run|notification>")
		return 1
	}
	client := apiclient.Client{BaseURL: *apiURL}
	switch remaining[0] {
	case "serve":
		return runServe(remaining[1:], *configPath, stdout, stderr, now)
	case "health":
		return runHealth(client, remaining[1:], stdout, stderr)
	case "decision":
		return runDecision(client, remaining[1:], stdout, stderr)
	case "status":
		return runStatus(client, remaining[1:], stdout, stderr)
	case "calibration":
		return runCalibration(client, remaining[1:], stdout, stderr)
	case "capacity":
		return runCapacity(client, remaining[1:], stdout, stderr)
	case "token":
		return runToken(client, remaining[1:], stdout, stderr)
	case "usage":
		return runUsage(client, remaining[1:], stdout, stderr)
	case "task":
		return runResource(client, "tasks", remaining[1:], stdout, stderr)
	case "profile":
		return runResource(client, "profiles", remaining[1:], stdout, stderr)
	case "scheduler":
		return runScheduler(client, remaining[1:], stdout, stderr)
	case "run":
		return runRuns(client, remaining[1:], stdout, stderr)
	case "notification":
		return runNotifications(client, remaining[1:], stdout, stderr)
	case "pause", "resume":
		return runProviderControl(client, remaining[0], remaining[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", remaining[0])
		return 1
	}
}

func runCapacity(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	provider, _, ok := providerFlags("capacity", args, stderr)
	if !ok {
		return 1
	}
	var estimate capacity.EstimateResult
	path := "/v1/providers/" + url.PathEscape(provider) + "/capacity"
	if err := client.Do(context.Background(), http.MethodGet, path, nil, &estimate); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, estimate)
	return 0
}

func runToken(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "sync" {
		fmt.Fprintln(stderr, "usage: redline token sync --provider ID")
		return 1
	}
	provider, _, ok := providerFlags("token sync", args[1:], stderr)
	if !ok {
		return 1
	}
	var result map[string]any
	path := "/v1/providers/" + url.PathEscape(provider) + "/token-sync"
	if err := client.Do(context.Background(), http.MethodPost, path, map[string]any{}, &result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, result)
	return 0
}

func runCalibration(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	provider, _, ok := providerFlags("calibration", args, stderr)
	if !ok {
		return 1
	}
	var estimate calibration.Estimate
	path := "/v1/providers/" + url.PathEscape(provider) + "/calibration"
	if err := client.Do(context.Background(), http.MethodGet, path, nil, &estimate); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, estimate)
	return 0
}

func runHealth(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(stderr)
	window := flags.String("window", "24h", "summary duration")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	var health domain.OperationalHealth
	path := "/v1/health/details?window=" + url.QueryEscape(*window)
	if err := client.Do(context.Background(), http.MethodGet, path, nil, &health); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, health)
	return 0
}

func runNotifications(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: redline notification list")
		return 1
	}
	var deliveries []domain.NotificationDelivery
	if err := client.Do(context.Background(), http.MethodGet, "/v1/notifications", nil, &deliveries); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, deliveries)
	return 0
}

func runServe(args []string, configPath string, stdout, stderr io.Writer, now func() time.Time) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:7436", "listen address")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	database, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer database.Close()
	if err := database.RecoverInterruptedRuns(context.Background(), now()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := database.RecoverPendingNotificationDeliveries(context.Background(), now()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	apiServer := api.NewServer(cfg, database, now)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	apiServer.StartScheduler(ctx)
	server := &http.Server{
		Addr:              *listen,
		Handler:           apiServer,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(stdout, "Redline API listening on http://%s\n", *listen)
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServe() }()
	select {
	case err := <-errors:
		stop()
		apiServer.Wait()
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		apiServer.Wait()
	}
	return 0
}

func runDecision(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	provider, jsonOutput, ok := providerFlags("decision", args, stderr)
	if !ok {
		return 1
	}
	var response decisionResponse
	path := "/v1/providers/" + url.PathEscape(provider) + "/decision"
	if err := client.Do(context.Background(), http.MethodPost, path, map[string]any{}, &response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		writeJSON(stdout, response)
	} else {
		writeDecisionText(stdout, response)
	}
	if response.Result.Decision == decision.Unknown {
		return 2
	}
	return 0
}

func runStatus(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	provider, jsonOutput, ok := providerFlags("status", args, stderr)
	if !ok {
		return 1
	}
	var snapshot decision.UsageSnapshot
	path := "/v1/providers/" + url.PathEscape(provider) + "/status"
	if err := client.Do(context.Background(), http.MethodGet, path, nil, &snapshot); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		writeJSON(stdout, snapshot)
	} else {
		short := "unrestricted"
		if snapshot.Short != nil {
			short = percent(snapshot.Short.Remaining) + " remaining"
		}
		fmt.Fprintf(stdout, "%s: 5-hour %s, %s weekly remaining (observed %s)\n",
			snapshot.Provider, short, percent(snapshot.Weekly.Remaining), snapshot.ObservedAt.Format(time.RFC3339))
		for _, allowance := range snapshot.Allowances {
			if allowance.Key == "session" || allowance.Key == "weekly" {
				continue
			}
			fmt.Fprintf(stdout, "  %s: %s remaining (resets %s)\n", allowance.SourceLabel,
				percent(allowance.Remaining), allowance.ResetsAt.Format(time.RFC3339))
		}
	}
	return 0
}

func runUsage(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "refresh" {
		fmt.Fprintln(stderr, "usage: redline usage refresh --provider <name> [--json]")
		return 1
	}
	provider, jsonOutput, ok := providerFlags("usage refresh", args[1:], stderr)
	if !ok {
		return 1
	}
	var snapshot decision.UsageSnapshot
	path := "/v1/providers/" + url.PathEscape(provider) + "/refresh"
	if err := client.Do(context.Background(), http.MethodPost, path, map[string]any{}, &snapshot); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		writeJSON(stdout, snapshot)
	} else {
		fmt.Fprintf(stdout, "refreshed %s usage observed at %s\n", snapshot.Provider, snapshot.ObservedAt.Format(time.RFC3339))
	}
	return 0
}

func runResource(
	client apiclient.Client,
	resource string,
	args []string,
	stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "usage: redline %s <add|list>\n", strings.TrimSuffix(resource, "s"))
		return 1
	}
	switch args[0] {
	case "list":
		var output any
		if resource == "tasks" {
			output = &[]domain.Task{}
		} else {
			output = &[]domain.ExecutionProfile{}
		}
		if err := client.Do(context.Background(), http.MethodGet, "/v1/"+resource, nil, output); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, output)
		return 0
	case "add":
		flags := flag.NewFlagSet(resource+" add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		file := flags.String("file", "", "YAML definition")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil || *file == "" {
			if *file == "" {
				fmt.Fprintln(stderr, "--file is required")
			}
			return 1
		}
		request, err := readYAML(*file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var output any
		if resource == "tasks" {
			output = &domain.Task{}
		} else {
			output = &domain.ExecutionProfile{}
		}
		if err := client.Do(context.Background(), http.MethodPost, "/v1/"+resource, request, output); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, output)
		} else {
			fmt.Fprintf(stdout, "created %s\n", strings.TrimSuffix(resource, "s"))
		}
		return 0
	case "enable", "disable", "retry":
		if resource != "tasks" || len(args) < 2 {
			fmt.Fprintf(stderr, "usage: redline task %s <id> [--json]\n", args[0])
			return 1
		}
		flags := flag.NewFlagSet("task "+args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return 1
		}
		var task domain.Task
		path := "/v1/tasks/" + url.PathEscape(args[1]) + "/" + args[0]
		if err := client.Do(context.Background(), http.MethodPost, path, map[string]any{}, &task); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, task)
		} else {
			fmt.Fprintf(stdout, "%sd task %s\n", args[0], task.ID)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown %s command %q\n", strings.TrimSuffix(resource, "s"), args[0])
		return 1
	}
}

func runProviderControl(
	client apiclient.Client,
	control string,
	args []string,
	stdout, stderr io.Writer,
) int {
	provider, _, ok := providerFlags(control, args, stderr)
	if !ok {
		return 1
	}
	var response map[string]any
	path := "/v1/providers/" + url.PathEscape(provider) + "/" + control
	if err := client.Do(context.Background(), http.MethodPost, path, map[string]any{}, &response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	verb := "paused"
	if control == "resume" {
		verb = "resumed"
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, provider)
	return 0
}

func runScheduler(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redline scheduler <evaluate|execute|history|attempts|status>")
		return 1
	}
	if args[0] == "status" {
		var status autoscheduler.Status
		if err := client.Do(context.Background(), http.MethodGet, "/v1/scheduler/status", nil, &status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, status)
		return 0
	}
	if args[0] == "history" {
		provider, _, ok := providerFlags("scheduler history", args[1:], stderr)
		if !ok {
			return 1
		}
		var records []domain.SchedulerDecision
		path := "/v1/scheduler/decisions?provider=" + url.QueryEscape(provider)
		if err := client.Do(context.Background(), http.MethodGet, path, nil, &records); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, records)
		return 0
	}
	if args[0] == "attempts" {
		provider, _, ok := providerFlags("scheduler attempts", args[1:], stderr)
		if !ok {
			return 1
		}
		var attempts []domain.DispatchAttempt
		path := "/v1/scheduler/attempts?provider=" + url.QueryEscape(provider)
		if err := client.Do(context.Background(), http.MethodGet, path, nil, &attempts); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, attempts)
		return 0
	}
	if args[0] != "evaluate" && args[0] != "execute" {
		fmt.Fprintf(stderr, "unknown scheduler command %q\n", args[0])
		return 1
	}
	command := args[0]
	flags := flag.NewFlagSet("scheduler "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "configured provider name")
	revision := flags.String("revision", "", "current repository revision")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil || *provider == "" {
		if *provider == "" {
			fmt.Fprintln(stderr, "--provider is required")
		}
		return 1
	}
	var response schedulerResponse
	body := map[string]string{"provider_account_id": *provider, "current_revision": *revision}
	if err := client.Do(context.Background(), http.MethodPost, "/v1/scheduler/"+command, body, &response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		writeJSON(stdout, response)
	} else {
		writeDecisionText(stdout, decisionResponse{Snapshot: response.Snapshot, Result: response.Result})
		if response.SelectedTask == nil {
			fmt.Fprintln(stdout, "Selected task:                none")
		} else {
			fmt.Fprintf(stdout, "Selected task:                %s (%s)\n", response.SelectedTask.Name, response.SelectedTask.ID)
		}
	}
	return 0
}

func runRuns(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redline run <list|show|events|logs>")
		return 1
	}
	switch args[0] {
	case "list":
		var runs []domain.Run
		if err := client.Do(context.Background(), http.MethodGet, "/v1/runs", nil, &runs); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, runs)
		return 0
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: redline run show <id>")
			return 1
		}
		var run domain.Run
		if err := client.Do(context.Background(), http.MethodGet, "/v1/runs/"+url.PathEscape(args[1]), nil, &run); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, run)
		return 0
	case "events":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: redline run events <id> [--limit N]")
			return 1
		}
		flags := flag.NewFlagSet("run events", flag.ContinueOnError)
		flags.SetOutput(stderr)
		limit := flags.Int("limit", 100, "maximum number of events")
		if err := flags.Parse(args[2:]); err != nil || *limit <= 0 {
			if *limit <= 0 {
				fmt.Fprintln(stderr, "--limit must be positive")
			}
			return 1
		}
		path := "/v1/runs/" + url.PathEscape(args[1]) + "/events?limit=" + strconv.Itoa(*limit)
		var events []domain.RunEvent
		if err := client.Do(context.Background(), http.MethodGet, path, nil, &events); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, events)
		return 0
	case "logs":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: redline run logs <id> [--stream STREAM] [--tail-bytes N]")
			return 1
		}
		flags := flag.NewFlagSet("run logs", flag.ContinueOnError)
		flags.SetOutput(stderr)
		stream := flags.String("stream", "stdout", "stdout, stderr, prepare_stdout, prepare_stderr, finalize_stdout, or finalize_stderr")
		tailBytes := flags.Int64("tail-bytes", 32*1024, "maximum bytes from the end of the log")
		jsonOutput := flags.Bool("json", false, "emit JSON metadata and content")
		if err := flags.Parse(args[2:]); err != nil || !validRunLogStream(*stream) || *tailBytes <= 0 {
			if !validRunLogStream(*stream) {
				fmt.Fprintln(stderr, "--stream is not supported")
			}
			if *tailBytes <= 0 {
				fmt.Fprintln(stderr, "--tail-bytes must be positive")
			}
			return 1
		}
		path := "/v1/runs/" + url.PathEscape(args[1]) + "/logs?stream=" + url.QueryEscape(*stream) +
			"&tail_bytes=" + strconv.FormatInt(*tailBytes, 10)
		var tail artifacts.Tail
		if err := client.Do(context.Background(), http.MethodGet, path, nil, &tail); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, tail)
		} else {
			fmt.Fprint(stdout, tail.Content)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown run command %q\n", args[0])
		return 1
	}
}

func validRunLogStream(stream string) bool {
	switch stream {
	case "stdout", "stderr", "prepare_stdout", "prepare_stderr", "finalize_stdout", "finalize_stderr":
		return true
	default:
		return false
	}
}

func providerFlags(name string, args []string, stderr io.Writer) (string, bool, bool) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "configured provider name")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return "", false, false
	}
	if *provider == "" {
		fmt.Fprintln(stderr, "--provider is required")
		return "", false, false
	}
	return *provider, *jsonOutput, true
}

func readYAML(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read definition: %w", err)
	}
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode YAML definition: %w", err)
	}
	return value, nil
}

func writeJSON(writer io.Writer, value any) {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeDecisionText(w io.Writer, response decisionResponse) {
	s, r := response.Snapshot, response.Result
	rows := [][2]string{
		{"Provider", s.Provider}, {"Observed at", s.ObservedAt.Format(time.RFC3339)},
		{"Weekly remaining", percent(s.Weekly.Remaining)},
		{"Weekly reset", s.Weekly.ResetsAt.Format(time.RFC3339)},
		{"Decision mode", string(r.Mode)},
	}
	if s.Short == nil {
		rows = append(rows, [2]string{"5-hour window", "unrestricted"})
	} else {
		rows = append(rows,
			[2]string{"5-hour remaining", percent(s.Short.Remaining)},
			[2]string{"Next 5-hour reset", s.Short.ResetsAt.Format(time.RFC3339)},
		)
	}
	rows = append(rows,
		[2]string{"Slots", fmt.Sprintf("%d", len(r.Slots))},
		[2]string{"Maximum consumable", percent(r.MaximumConsumable)},
		[2]string{"Calculated overflow", percent(r.Overflow)},
		[2]string{"Decision", string(r.Decision)}, [2]string{"Reason", r.Reason},
	)
	for _, row := range rows {
		fmt.Fprintf(w, "%-29s %s\n", row[0]+":", row[1])
	}
}

func percent(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }
