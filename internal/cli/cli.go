package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/apiauth"
	"github.com/jfox/redline/internal/apiclient"
	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/demo"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/launchmetrics"
	"github.com/jfox/redline/internal/mcpserver"
	"github.com/jfox/redline/internal/pairing"
	"github.com/jfox/redline/internal/relay"
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
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		writeHelp(stdout)
		return 0
	}
	global := flag.NewFlagSet("redline", flag.ContinueOnError)
	global.SetOutput(stderr)
	configPath := global.String("config", "redline.yaml", "service configuration file")
	apiURL := global.String("api", "http://127.0.0.1:7436", "Redline service API URL")
	if err := global.Parse(args); err != nil {
		return 1
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(stderr, "usage: redline [--api URL] <serve|demo|mcp|health|decision|status|calibration|capacity|metrics|token|usage|task|profile|scheduler|run|notification|candidates|pause|resume|pair|relay>")
		return 1
	}
	client := apiclient.Client{BaseURL: *apiURL, Token: clientToken(*configPath)}
	switch remaining[0] {
	case "serve":
		return runServe(remaining[1:], *configPath, stdout, stderr, now)
	case "demo":
		return runDemo(remaining[1:], stdout, stderr, now)
	case "mcp":
		return runMCP(client, remaining[1:], stderr)
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
	case "metrics":
		return runMetrics(client, remaining[1:], stdout, stderr)
	case "token":
		return runToken(client, remaining[1:], *configPath, stdout, stderr)
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
	case "candidates":
		return runCandidates(client, remaining[1:], stdout, stderr)
	case "pause", "resume":
		return runProviderControl(client, remaining[0], remaining[1:], stdout, stderr)
	case "pair":
		return runPair(client, remaining[1:], *configPath, stdout, stderr)
	case "relay":
		return runRelay(client, remaining[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", remaining[0])
		return 1
	}
}

func writeHelp(output io.Writer) {
	fmt.Fprintln(output, "Redline — budget-aware dispatch for deferred LLM work")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "usage: redline [--api URL] [--config FILE] <command>")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "commands: serve, demo, mcp, health, decision, status, calibration, capacity, metrics, token,")
	fmt.Fprintln(output, "          usage, task, profile, scheduler, run, notification, candidates, pause, resume, pair, relay")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "token rotate --yes   replace the API token and sign out every paired device")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "GitHub:  https://github.com/croutoncreations/redline")
	fmt.Fprintln(output, "Updates: https://buttondown.com/croutoncreations?utm_source=redline&utm_medium=cli&utm_campaign=redline")
}

func runDemo(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 1 && args[0] == "list" {
		fmt.Fprintln(stdout, "Available Redline demo scenarios:")
		for _, scenario := range demo.Scenarios() {
			fmt.Fprintf(stdout, "  %-10s %s\n", scenario.Name, scenario.Description)
		}
		return 0
	}
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, "usage: redline demo <list|serve> [--scenario NAME] [--provider claude-main|codex-main] [--listen 127.0.0.1:7446] [--state-dir DIR] [--keep] [--open]")
		return 1
	}
	flags := flag.NewFlagSet("demo serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	scenario := flags.String("scenario", "overview", "named fixture scenario")
	provider := flags.String("provider", "claude-main", "provider for decision scenarios")
	listen := flags.String("listen", "127.0.0.1:7446", "loopback listen address")
	stateDir := flags.String("state-dir", "", "new or empty isolated state directory")
	keep := flags.Bool("keep", false, "preserve temporary demo state after exit")
	openDashboard := flags.Bool("open", false, "open the demo dashboard in the default browser")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if err := validateDemoListen(*listen); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer listener.Close()
	root, temporary, err := prepareDemoState(*stateDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if temporary && !*keep {
		defer os.RemoveAll(root)
	}
	env, err := demo.CreateForProvider(context.Background(), *scenario, *provider, root, now())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer env.Close()
	apiServer := api.NewDemoServer(env.Config, env.Database, now, env.Snapshots,
		demo.Discoverer{Now: now}, demo.Executor{Store: env.Database, Root: root, Now: now})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: apiServer, ReadHeaderTimeout: 5 * time.Second}
	address := "http://" + listener.Addr().String()
	fmt.Fprintf(stdout, "Redline demo (%s) listening on %s\n", *scenario, address)
	fmt.Fprintf(stdout, "Synthetic state: %s\n", root)
	fmt.Fprintln(stdout, "This process does not read real Redline data or invoke provider harnesses.")
	if *openDashboard {
		if err := exec.Command("open", address).Start(); err != nil {
			fmt.Fprintf(stderr, "open dashboard: %v\n", err)
		}
	}
	errors := make(chan error, 1)
	go func() { errors <- server.Serve(listener) }()
	select {
	case err := <-errors:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	apiServer.Wait()
	return 0
}

func validateDemoListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid demo listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("demo mode only listens on an explicit loopback IP")
	}
	if port == "7436" {
		return fmt.Errorf("demo mode refuses Redline's production port 7436")
	}
	return nil
}

func prepareDemoState(requested string) (string, bool, error) {
	if requested == "" {
		root, err := os.MkdirTemp("", "redline-demo-")
		return root, true, err
	}
	root, err := filepath.Abs(requested)
	if err != nil {
		return "", false, err
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		production := filepath.Join(home, "Library", "Application Support", "Redline")
		if root == production {
			return "", false, fmt.Errorf("demo state cannot use Redline's production state directory")
		}
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", false, err
		}
		return root, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(entries) != 0 {
		return "", false, fmt.Errorf("demo state directory must be empty: %s", root)
	}
	return root, false, nil
}

func runMetrics(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "launch" {
		fmt.Fprintln(stderr, "usage: redline metrics launch [--days N] [--provider ID]")
		return 1
	}
	flags := flag.NewFlagSet("metrics launch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	days := flags.Int("days", 21, "report window in days")
	provider := flags.String("provider", "", "optional configured provider account")
	if err := flags.Parse(args[1:]); err != nil || *days < 1 || *days > 365 {
		if *days < 1 || *days > 365 {
			fmt.Fprintln(stderr, "--days must be between 1 and 365")
		}
		return 1
	}
	values := url.Values{"days": {strconv.Itoa(*days)}}
	if *provider != "" {
		values.Set("provider", *provider)
	}
	var report launchmetrics.Report
	if err := client.Do(context.Background(), http.MethodGet, "/v1/metrics/launch?"+values.Encode(), nil, &report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, report)
	return 0
}

func runMCP(client apiclient.Client, args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: redline [--api URL] mcp")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.RunStdio(ctx, client); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "run Redline MCP server: %v\n", err)
		return 1
	}
	return 0
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

func runToken(client apiclient.Client, args []string, configPath string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "rotate" {
		return runTokenRotate(args[1:], configPath, stdout, stderr)
	}
	if len(args) == 0 || args[0] != "sync" {
		fmt.Fprintln(stderr, "usage: redline token <sync --provider ID|rotate>")
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

func runTokenRotate(args []string, configPath string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("token rotate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	confirmed := flags.Bool("yes", false, "rotate without the interactive confirmation prompt")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	resolvedPath := resolveTokenConfigPath(configPath)
	tokenPath := apiauth.TokenPath(resolvedPath)
	if !*confirmed {
		fmt.Fprintf(stderr, "Rotating %s signs out every paired browser and invalidates saved API tokens.\n", tokenPath)
		fmt.Fprintln(stderr, "Re-run with --yes to confirm.")
		return 1
	}
	// The new token is intentionally not printed: it stays in the protected
	// file, and `pair --qr` is the supported way to hand it to a device.
	if _, err := apiauth.RotateToken(resolvedPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Rotated the Redline API token at %s\n", tokenPath)
	fmt.Fprintln(stdout, "Restart Redline to load it, then run `redline pair --qr` to pair devices again.")
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
	cfg, err := config.LoadForService(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Claim the service boundary before RelayResolver can initialize or update
	// managed state. A losing second service therefore cannot mutate state.
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer listener.Close()
	// From this boundary onward service components receive one deep owner, not
	// separate state, Keychain, issuer, controller, or coordinator capabilities.
	management, err := config.NewServiceRelayManager(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "relay:", err)
		return 1
	}
	var connectionGeneration uint64
	relayManager := newRelaySupervisor(management, func(snapshot config.ResolvedRelay, tokenSource func() string) (relayDialerRun, error) {
		connectionGeneration++
		identity := config.RelayConnectionIdentity{
			URL: snapshot.URL, SessionID: snapshot.SessionID, ReconnectGeneration: snapshot.ReconnectGeneration,
			ConnectionGeneration: connectionGeneration,
		}
		management.SetConnection(identity, false)
		dialer, err := newRelayDialer(cfg, snapshot, tokenSource, listener.Addr().String(), management.TriggerRelayEntitlement, func(connected bool) {
			management.SetConnection(identity, connected)
		}, func(format string, args ...any) {
			fmt.Fprintf(stderr, format+"\n", args...)
		})
		if err != nil {
			return nil, err
		}
		return dialer.Run, nil
	})
	defer relayManager.Close()
	initialRelay := relayManager.Initial()
	switch initialRelay.Readiness {
	case config.RelayReadinessNeedsLicense:
		fmt.Fprintln(stderr, "relay: needs_license")
	case config.RelayReadinessUnavailable:
		fmt.Fprintln(stderr, "relay: unavailable (secure store unavailable)")
	}
	cfg.APIToken, err = apiauth.EnsureToken(configPath)
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
	apiServer := api.NewServerWithRelayManager(cfg, database, now, management)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// HTTP admission and handler draining precede controller/dialer cancellation.
	// Keeping relay authority services alive during the drain lets every admitted
	// management transaction finish its prepare/commit/abort protocol.
	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	defer stopRuntime()
	apiServer.StartScheduler(ctx)
	server := &http.Server{
		Addr:              *listen,
		Handler:           apiServer,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(stdout, "Redline API listening on http://%s\n", listener.Addr())

	// Remote access is opt-in. The supervisor dials out and follows the same
	// coordinator observed by API pairing instead of retaining startup state.
	if initialRelay.CanDial() {
		fmt.Fprintf(stdout, "Relay enabled via %s\n", initialRelay.URL)
	}
	relayDone := make(chan error, 1)
	go func() { relayDone <- relayManager.Run(runtimeCtx) }()
	controllerDone := make(chan struct{})
	go func() {
		management.Run(runtimeCtx)
		close(controllerDone)
	}()

	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	relayFinished := false
	serverFinished := false
	exitCode := 0
	select {
	case err := <-serverDone:
		serverFinished = true
		stop()
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, err)
			exitCode = 1
		}
	case err := <-relayDone:
		relayFinished = true
		stop()
		if err != nil {
			fmt.Fprintln(stderr, "relay:", err)
			exitCode = 1
		}
	case <-ctx.Done():
	}
	// The drain budget must exceed the longest legitimate admitted operation
	// (Configure's up-to-16s hosted-attempt wait, itself bounding a 15s issuer
	// HTTP call) plus margin, or shutdown can interrupt an already-persisted
	// mutation and leave its caller without an acknowledgement.
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := management.CloseAdmission(shutdown); err != nil {
		fmt.Fprintln(stderr, "relay management shutdown:", err)
		exitCode = 1
	}
	if !serverFinished {
		if err := server.Shutdown(shutdown); err != nil {
			fmt.Fprintln(stderr, err)
			exitCode = 1
		}
		<-serverDone
	}
	apiServer.Wait()
	cancel()
	stopRuntime()
	if !relayFinished {
		if err := <-relayDone; err != nil {
			fmt.Fprintln(stderr, "relay:", err)
			exitCode = 1
		}
	}
	<-controllerDone
	return exitCode
}

// newRelayDialer builds the outbound relay leg for a service that has remote
// access enabled.
//
// The session id has already been resolved from atomically managed state rather
// than minted per start, so a phone remains paired across service restarts.
func newRelayDialer(cfg config.Config, snapshot config.ResolvedRelay, tokenSource func() string, localAddr string, signal func(relay.EntitlementSignal), connectionState func(bool), logf func(string, ...any)) (*relay.Dialer, error) {
	keypair, err := relay.LoadOrCreateKeypair(
		relay.DefaultKeypairPath(cfg.Relay.KeypairPath, cfg.Database),
	)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(snapshot.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("resolved relay session_id is required when dial is enabled")
	}
	return relay.NewDialer(relay.DialerOptions{
		RelayURL:               snapshot.URL,
		SessionID:              sessionID,
		Keypair:                keypair,
		EntitlementTokenSource: tokenSource,
		EntitlementSignal:      signal,
		ConnectionState:        connectionState,
		Logf:                   logf,
		// Requests are replayed against this service's own listener, so the
		// phone reaches exactly the API a local browser would.
		Forwarder: relay.NewForwarder("http://"+localAddr, &http.Client{Timeout: 30 * time.Second}),
	}), nil
}

// resolveTokenConfigPath returns the config path whose API token the running
// service actually uses. When the caller did not override --config, an
// installed app keeps its credential beside the standard Application Support
// configuration rather than the working directory. Both credential readers and
// `token rotate` share this so rotation can never target a different file than
// the one the service reads.
func resolveTokenConfigPath(configPath string) string {
	if _, err := apiauth.ReadToken(configPath); err == nil {
		return configPath
	}
	if configPath != "redline.yaml" {
		return configPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return configPath
	}
	standard := filepath.Join(home, "Library", "Application Support", "Redline", "redline.yaml")
	if _, err := apiauth.ReadToken(standard); err != nil {
		return configPath
	}
	return standard
}

func clientToken(configPath string) string {
	if token := strings.TrimSpace(os.Getenv("REDLINE_API_TOKEN")); token != "" {
		return token
	}
	if token, err := apiauth.ReadToken(resolveTokenConfigPath(configPath)); err == nil {
		return token
	}
	return ""
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
		commands := "add|list"
		if resource == "tasks" {
			commands = "add|list|enable|disable|retry|dispatch"
		}
		fmt.Fprintf(stderr, "usage: redline %s <%s>\n", strings.TrimSuffix(resource, "s"), commands)
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
	case "enable", "disable", "retry", "dispatch":
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
		var body any = map[string]any{}
		var output any = &task
		if args[0] == "dispatch" {
			body = nil
			output = &map[string]any{}
		}
		if err := client.Do(context.Background(), http.MethodPost, path, body, output); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput || args[0] == "dispatch" {
			writeJSON(stdout, output)
		} else {
			fmt.Fprintf(stdout, "%sd task %s\n", args[0], task.ID)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown %s command %q\n", strings.TrimSuffix(resource, "s"), args[0])
		return 1
	}
}

func runCandidates(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("candidates", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "provider account")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if strings.TrimSpace(*provider) == "" {
		fmt.Fprintln(stderr, "--provider is required")
		return 1
	}
	var response map[string]any
	path := "/v1/providers/" + url.PathEscape(*provider) + "/candidates"
	if err := client.Do(context.Background(), http.MethodGet, path, nil, &response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, response)
	return 0
}

func runRelay(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redline relay <status|activate|devices|device deactivate|setup|off|deactivate>")
		return 1
	}
	requestStatus := func(method, path string, body any) int {
		var status config.RelayStatus
		if err := client.Do(context.Background(), method, path, body, &status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, status)
		return 0
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: redline relay status")
			return 1
		}
		return requestStatus(http.MethodGet, "/v1/relay/status", nil)
	case "activate":
		if len(args) < 2 || args[1] == "" {
			fmt.Fprintln(stderr, "usage: redline relay activate <key> [--label LABEL]")
			return 1
		}
		flags := flag.NewFlagSet("relay activate", flag.ContinueOnError)
		flags.SetOutput(stderr)
		label := flags.String("label", "", "optional device label")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return 1
		}
		licenseKey := args[1]
		var status config.RelayStatus
		if err := client.Do(context.Background(), http.MethodPost, "/v1/relay/configure", config.RelayConfigureRequest{
			Mode: config.RelayModeHosted, LicenseKey: licenseKey, Label: *label,
		}, &status); err != nil {
			fmt.Fprintln(stderr, strings.ReplaceAll(err.Error(), licenseKey, "[REDACTED]"))
			return 1
		}
		var redacted bytes.Buffer
		writeJSON(&redacted, status)
		_, _ = fmt.Fprint(stdout, strings.ReplaceAll(redacted.String(), licenseKey, "[REDACTED]"))
		return 0
	case "devices":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: redline relay devices")
			return 1
		}
		var response struct {
			Devices []relay.Activation `json:"devices"`
		}
		if err := client.Do(context.Background(), http.MethodGet, "/v1/relay/devices", nil, &response); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, response.Devices)
		return 0
	case "device":
		if len(args) != 3 || args[1] != "deactivate" || !validCLIActivationID(args[2]) {
			fmt.Fprintln(stderr, "usage: redline relay device deactivate <id>")
			return 1
		}
		path := "/v1/relay/devices/" + url.PathEscape(args[2])
		if err := client.Do(context.Background(), http.MethodDelete, path, nil, nil); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "relay device deactivated")
		return 0
	case "setup":
		flags := flag.NewFlagSet("relay setup", flag.ContinueOnError)
		flags.SetOutput(stderr)
		relayURL := flags.String("url", "", "self-hosted relay URL")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *relayURL == "" {
			if *relayURL == "" {
				fmt.Fprintln(stderr, "--url is required")
			}
			return 1
		}
		return requestStatus(http.MethodPost, "/v1/relay/configure", config.RelayConfigureRequest{Mode: config.RelayModeSelfHosted, URL: *relayURL})
	case "off":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: redline relay off")
			return 1
		}
		return requestStatus(http.MethodPost, "/v1/relay/configure", config.RelayConfigureRequest{Mode: config.RelayModeOff})
	case "deactivate":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: redline relay deactivate")
			return 1
		}
		return requestStatus(http.MethodPost, "/v1/relay/deactivate", map[string]any{})
	default:
		fmt.Fprintf(stderr, "unknown relay command %q\n", args[0])
		return 1
	}
}

func validCLIActivationID(id string) bool {
	if id == "" || len(id) > 512 {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func runPair(client apiclient.Client, args []string, configPath string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	flags.SetOutput(stderr)
	qrOutput := flags.Bool("qr", false, "print a terminal pairing QR code")
	host := flags.String("host", "", "Tailscale MagicDNS hostname")
	port := flags.Int("port", 443, "Tailscale Serve HTTPS port")
	relayOnly := flags.Bool("relay-only", false, "emit a relay-only code even when a tailnet host is available")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if !*qrOutput {
		fmt.Fprintln(stderr, "--qr is required")
		return 1
	}
	if *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, "--port must be between 1 and 65535")
		return 1
	}
	_ = configPath // Pairing state belongs to the authenticated running service.
	var code struct {
		Token        string                     `json:"pairing_token"`
		ExpiresAt    time.Time                  `json:"expires_at"`
		PairingURL   string                     `json:"pairing_url"`
		Routes       []pairing.Route            `json:"routes"`
		Endpoint     string                     `json:"endpoint"`
		RelayStatus  string                     `json:"relay_status"`
		RelayRefusal pairing.RelayRefusalReason `json:"relay_refusal"`
	}
	request := map[string]any{"host": *host, "port": *port, "relay_only": *relayOnly}
	if err := client.Do(context.Background(), http.MethodPost, "/v1/pairing", request, &code); err != nil {
		fmt.Fprintln(stderr, "create pairing token:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Relay: %s\n", strings.ReplaceAll(code.RelayStatus, "_", " "))
	if code.RelayRefusal != "" {
		fmt.Fprintf(stdout, "Relay route unavailable: %s\n", strings.ReplaceAll(string(code.RelayRefusal), "_", " "))
	}
	if code.Token == "" || code.PairingURL == "" || !code.ExpiresAt.After(time.Now()) {
		fmt.Fprintln(stderr, "create pairing token: service returned an invalid pairing credential or no usable route")
		return 1
	}
	qr, err := qrcode.New(code.PairingURL, qrcode.Medium)
	if err != nil {
		fmt.Fprintln(stderr, "create pairing QR:", err)
		return 1
	}
	// Says exactly which routes the code offers, because "from a device on
	// your tailnet" was wrong for two of the three.
	hasDirect, hasRelay := false, false
	for _, route := range code.Routes {
		hasDirect = hasDirect || route == pairing.RouteDirect
		hasRelay = hasRelay || route == pairing.RouteRelay
	}
	switch {
	case hasDirect && hasRelay:
		fmt.Fprintf(stdout, "Scan this QR code to pair — pairs over your tailnet (%s) and falls back to the relay:\n", code.Endpoint)
	case hasDirect:
		fmt.Fprintf(stdout, "Scan this QR code to pair — pairs over your tailnet (%s):\n", code.Endpoint)
	default:
		fmt.Fprintln(stdout, "Scan this QR code to pair — pairs over the relay only:")
	}
	renderTerminalQR(stdout, qr.Bitmap())
	fmt.Fprintln(stdout, "WARNING: This QR contains a pairing credential that grants full API access. Keep it private and rotate the API token if exposed.")
	return 0
}

func renderTerminalQR(output io.Writer, bitmap [][]bool) {
	const margin = 2
	width := 0
	if len(bitmap) > 0 {
		width = len(bitmap[0])
	}
	blank := strings.Repeat(" ", width+2*margin)
	for range margin / 2 {
		fmt.Fprintln(output, blank)
	}
	for y := -margin; y < len(bitmap)+margin; y += 2 {
		for x := -margin; x < width+margin; x++ {
			top := y >= 0 && y < len(bitmap) && x >= 0 && x < len(bitmap[y]) && !bitmap[y][x]
			bottom := y+1 >= 0 && y+1 < len(bitmap) && x >= 0 && x < len(bitmap[y+1]) && !bitmap[y+1][x]
			switch {
			case top && bottom:
				fmt.Fprint(output, "█")
			case top:
				fmt.Fprint(output, "▀")
			case bottom:
				fmt.Fprint(output, "▄")
			default:
				fmt.Fprint(output, " ")
			}
		}
		fmt.Fprintln(output)
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
	if r.TaskSelectionReason != "" {
		rows = append(rows, [2]string{"Task selection", r.TaskSelectionReason})
	}
	for _, rejection := range r.CandidateRejections {
		rows = append(rows, [2]string{"Rejected " + rejection.TaskID, rejection.Reason})
	}
	for _, row := range rows {
		fmt.Fprintf(w, "%-29s %s\n", row[0]+":", row[1])
	}
}

func percent(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }
