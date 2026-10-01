package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/primer"
)

const primerUsage = `usage: redline primer <status|set|enable|disable|run|history> --provider ID [flags]

  status   show settings, the next ping, and today's forecast windows
  set      --mode schedule|reset  --at 06:00,11:00  --days weekdays|mon,wed|daily
           --tz America/Chicago  --prompt TEXT  --model haiku  --catch-up 45m
           --enable / --disable
  enable   turn the primer on
  disable  turn the primer off
  run      ping now; skips if a window is already open unless --force
  history  list recent attempts (--limit N)`

func runPrimer(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stderr, primerUsage)
		return 1
	}
	command, rest := args[0], args[1:]
	flags := flag.NewFlagSet("primer "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "configured provider name")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	var mode, at, days, timezone, prompt, model, catchUp *string
	var enable, disable, force *bool
	var limit *int
	switch command {
	case "set":
		mode = flags.String("mode", "", "schedule (fixed times) or reset (after every reset)")
		at = flags.String("at", "", "comma-separated HH:MM times for schedule mode")
		days = flags.String("days", "", "daily, weekdays, weekends, or day names such as mon,wed,fri")
		timezone = flags.String("tz", "", "IANA time zone; \"local\" uses the service's zone")
		prompt = flags.String("prompt", "", "message to send")
		model = flags.String("model", "", "model alias or name")
		catchUp = flags.String("catch-up", "", "how late a scheduled ping may still fire after sleep, e.g. 45m")
		enable = flags.Bool("enable", false, "turn the primer on")
		disable = flags.Bool("disable", false, "turn the primer off")
	case "run":
		force = flags.Bool("force", false, "ping even if a window is already open")
	case "history":
		limit = flags.Int("limit", 20, "number of attempts")
	case "status", "enable", "disable":
	default:
		fmt.Fprintf(stderr, "unknown primer command %q\n%s\n", command, primerUsage)
		return 1
	}
	if err := flags.Parse(rest); err != nil {
		return 1
	}
	if *provider == "" {
		fmt.Fprintln(stderr, "--provider is required")
		return 1
	}
	base := "/v1/providers/" + url.PathEscape(*provider) + "/primer"
	ctx := context.Background()
	switch command {
	case "status":
		var status primer.Status
		if err := client.Do(ctx, http.MethodGet, base, nil, &status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writePrimerStatus(stdout, status, *jsonOutput)
	case "enable", "disable":
		return patchPrimer(ctx, client, base, map[string]any{"enabled": command == "enable"}, stdout, stderr, *jsonOutput)
	case "set":
		body := map[string]any{}
		set := map[string]bool{}
		flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if set["mode"] {
			switch value := strings.ToLower(*mode); value {
			case "schedule", "reset":
				body["mode"] = value
			default:
				fmt.Fprintln(stderr, "--mode must be schedule or reset")
				return 1
			}
		}
		if set["at"] {
			body["times"] = splitList(*at)
		}
		if set["days"] {
			body["days"] = splitList(*days)
		}
		if set["tz"] {
			if strings.EqualFold(*timezone, "local") {
				body["timezone"] = ""
			} else {
				body["timezone"] = *timezone
			}
		}
		if set["prompt"] {
			body["prompt"] = *prompt
		}
		if set["model"] {
			body["model"] = *model
		}
		if set["catch-up"] {
			duration, err := time.ParseDuration(*catchUp)
			if err != nil || duration < 0 {
				fmt.Fprintln(stderr, "--catch-up must be a duration such as 45m")
				return 1
			}
			body["catch_up_seconds"] = int64(duration / time.Second)
		}
		if *enable && *disable {
			fmt.Fprintln(stderr, "choose only one of --enable and --disable")
			return 1
		}
		if *enable || *disable {
			body["enabled"] = *enable
		}
		if len(body) == 0 {
			fmt.Fprintln(stderr, "nothing to change; pass at least one setting flag")
			return 1
		}
		return patchPrimer(ctx, client, base, body, stdout, stderr, *jsonOutput)
	case "run":
		path := base + "/run"
		if *force {
			path += "?force=true"
		}
		var attempt domain.PrimerAttempt
		// A ping waits for Claude to answer, which can outlast the default
		// 30-second client timeout; the service bounds it at two minutes.
		client.HTTPClient = &http.Client{Timeout: 3 * time.Minute}
		if err := client.Do(ctx, http.MethodPost, path, map[string]any{}, &attempt); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, attempt)
		} else {
			fmt.Fprintf(stdout, "%s: %s\n", attempt.Outcome, attempt.Reason)
		}
		if attempt.Outcome == domain.PrimerFailed {
			return 1
		}
		return 0
	case "history":
		var attempts []domain.PrimerAttempt
		if err := client.Do(ctx, http.MethodGet, fmt.Sprintf("%s/history?limit=%d", base, *limit), nil, &attempts); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, attempts)
			return 0
		}
		if len(attempts) == 0 {
			fmt.Fprintln(stdout, "No primer attempts yet.")
			return 0
		}
		// Times are shown in the primer's zone, like status.
		location := time.Local
		var status primer.Status
		if err := client.Do(ctx, http.MethodGet, base, nil, &status); err == nil {
			if loaded, err := time.LoadLocation(status.Timezone); err == nil {
				location = loaded
			}
		}
		fmt.Fprintf(stdout, "Times in %s\n", location)
		for _, attempt := range attempts {
			fmt.Fprintf(stdout, "%s  %-8s %-8s %s\n", attempt.StartedAt.In(location).Format("2006-01-02 15:04"),
				attempt.Trigger, primerOutcomeLabel(attempt), attempt.Reason)
		}
		return 0
	}
	return 1
}

func patchPrimer(ctx context.Context, client apiclient.Client, path string, body map[string]any, stdout, stderr io.Writer, jsonOutput bool) int {
	var status primer.Status
	if err := client.Do(ctx, http.MethodPatch, path, body, &status); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writePrimerStatus(stdout, status, jsonOutput)
}

func writePrimerStatus(stdout io.Writer, status primer.Status, jsonOutput bool) int {
	if jsonOutput {
		writeJSON(stdout, status)
		return 0
	}
	settings := status.Settings
	location, err := time.LoadLocation(status.Timezone)
	if err != nil {
		location = time.Local
	}
	stamp := func(value time.Time) string { return value.In(location).Format("Mon 15:04") }
	state := "off"
	if settings.Enabled {
		state = "on"
	}
	if !status.Supported {
		state = "unsupported: " + status.UnsupportedReason
	}
	fmt.Fprintf(stdout, "Window primer: %s\n", state)
	if settings.Mode == domain.PrimerReset {
		fmt.Fprintln(stdout, "Mode:          after every reset (keeps a window open 24/7)")
	} else {
		days := "every day"
		if len(settings.Days) > 0 {
			days = strings.Join(settings.Days, ",")
		}
		fmt.Fprintf(stdout, "Mode:          at scheduled times %s · %s\n", strings.Join(settings.Times, ", "), days)
		fmt.Fprintf(stdout, "Catch-up:      %s after sleep\n", time.Duration(settings.CatchUpSeconds)*time.Second)
	}
	fmt.Fprintf(stdout, "Time zone:     %s\n", status.Timezone)
	fmt.Fprintf(stdout, "Ping:          %q with %s\n", settings.Prompt, settings.Model)
	if status.WindowOpenUntil != nil {
		fmt.Fprintf(stdout, "Current window: open until %s\n", stamp(*status.WindowOpenUntil))
	} else {
		fmt.Fprintln(stdout, "Current window: none open")
	}
	if status.NextPingAt != nil {
		fmt.Fprintf(stdout, "Next ping:     %s\n", stamp(*status.NextPingAt))
	}
	if len(status.Forecast) > 0 {
		windows := make([]string, 0, len(status.Forecast))
		for _, window := range status.Forecast {
			windows = append(windows, window.Start.In(location).Format("15:04")+"–"+window.End.In(location).Format("15:04"))
		}
		fmt.Fprintf(stdout, "Forecast:      %s\n", strings.Join(windows, ", "))
	}
	if status.LastAttempt != nil {
		fmt.Fprintf(stdout, "Last attempt:  %s %s · %s\n", stamp(status.LastAttempt.StartedAt),
			primerOutcomeLabel(*status.LastAttempt), status.LastAttempt.Reason)
	}
	return 0
}

func primerOutcomeLabel(attempt domain.PrimerAttempt) string {
	if attempt.Outcome == domain.PrimerFired && attempt.Verification != domain.PrimerVerifyNone {
		return string(attempt.Verification)
	}
	return string(attempt.Outcome)
}

func splitList(value string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
