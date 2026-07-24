package hermes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jfox/redline/internal/domain"
	_ "modernc.org/sqlite"
)

const (
	defaultDesktopConfig  = "Library/Application Support/Hermes/connection.json"
	defaultDesktopCookies = "Library/Application Support/Hermes/Partitions/hermes-remote-oauth/Cookies"
)

type Profile struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	IsDefault   bool   `json:"is_default,omitempty"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Description string `json:"description,omitempty"`
}

type Project struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	PrimaryPath string   `json:"primary_path,omitempty"`
	Folders     []string `json:"folders,omitempty"`
}

type ModelProvider struct {
	Slug          string         `json:"slug"`
	Name          string         `json:"name"`
	IsCurrent     bool           `json:"is_current"`
	Models        []string       `json:"models"`
	Authenticated bool           `json:"authenticated"`
	Capabilities  map[string]any `json:"capabilities,omitempty"`
}

type ProfileOptions struct {
	Profile   Profile         `json:"profile"`
	Projects  []Project       `json:"projects"`
	Providers []ModelProvider `json:"providers"`
	Model     string          `json:"model,omitempty"`
	Provider  string          `json:"provider,omitempty"`
}

type Discovery struct {
	Version        string           `json:"version"`
	ReleaseDate    string           `json:"release_date,omitempty"`
	AuthRequired   bool             `json:"auth_required"`
	AuthProviders  []string         `json:"auth_providers,omitempty"`
	Profiles       []Profile        `json:"profiles"`
	ProfileOptions []ProfileOptions `json:"profile_options"`
}

type RunRequest struct {
	RunID             string
	Prompt            string
	Connection        domain.RuntimeConnection
	Context           domain.AgentContext
	Model             string
	Provider          string
	OnExternalStarted func(domain.ExternalRun) error
}

type RunResult struct {
	SessionID string         `json:"session_id"`
	Output    string         `json:"output"`
	Usage     map[string]any `json:"usage,omitempty"`
	Model     string         `json:"model,omitempty"`
	Provider  string         `json:"provider,omitempty"`
}

type HTTPClientFactory func(context.Context, domain.RuntimeConnection) (*http.Client, string, error)

type Client struct {
	HTTPClient HTTPClientFactory
}

func (c Client) Discover(ctx context.Context, connection domain.RuntimeConnection) (Discovery, error) {
	httpClient, baseURL, err := c.httpClient(ctx, connection)
	if err != nil {
		return Discovery{}, err
	}
	var status struct {
		Version       string   `json:"version"`
		ReleaseDate   string   `json:"release_date"`
		AuthRequired  bool     `json:"auth_required"`
		AuthProviders []string `json:"auth_providers"`
	}
	if err := getJSON(ctx, httpClient, baseURL+"/api/status", &status); err != nil {
		return Discovery{}, fmt.Errorf("read Hermes status: %w", err)
	}
	var profiles struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := getJSON(ctx, httpClient, baseURL+"/api/profiles", &profiles); err != nil {
		return Discovery{}, fmt.Errorf("list Hermes profiles: %w", err)
	}
	result := Discovery{
		Version: status.Version, ReleaseDate: status.ReleaseDate, AuthRequired: status.AuthRequired,
		AuthProviders: status.AuthProviders, Profiles: profiles.Profiles,
	}
	for _, profile := range profiles.Profiles {
		gateway, err := dialGateway(ctx, httpClient, baseURL, profile.Name, connection)
		if err != nil {
			return Discovery{}, fmt.Errorf("connect Hermes profile %q: %w", profile.Name, err)
		}
		var projectsPayload struct {
			Projects []Project `json:"projects"`
		}
		projectsErr := gateway.call(ctx, "projects.list", map[string]any{}, &projectsPayload)
		var models struct {
			Providers []ModelProvider `json:"providers"`
			Model     string          `json:"model"`
			Provider  string          `json:"provider"`
		}
		modelsErr := gateway.call(ctx, "model.options", map[string]any{}, &models)
		gateway.close()
		if projectsErr != nil {
			return Discovery{}, fmt.Errorf("list Hermes projects for %q: %w", profile.Name, projectsErr)
		}
		if modelsErr != nil {
			return Discovery{}, fmt.Errorf("list Hermes models for %q: %w", profile.Name, modelsErr)
		}
		result.ProfileOptions = append(result.ProfileOptions, ProfileOptions{
			Profile: profile, Projects: projectsPayload.Projects, Providers: models.Providers,
			Model: models.Model, Provider: models.Provider,
		})
	}
	return result, nil
}

func (c Client) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return RunResult{}, fmt.Errorf("Hermes prompt is required")
	}
	httpClient, baseURL, err := c.httpClient(ctx, request.Connection)
	if err != nil {
		return RunResult{}, err
	}
	gateway, err := dialGateway(ctx, httpClient, baseURL, request.Context.Profile, request.Connection)
	if err != nil {
		return RunResult{}, err
	}
	defer gateway.close()
	create := map[string]any{
		"profile": request.Context.Profile, "cwd": request.Context.WorkingDirectory,
		"source": "redline", "title": "Redline " + request.RunID,
		"close_on_disconnect": false,
	}
	if request.Model != "" && request.Model != "default" {
		create["model"] = request.Model
	}
	if request.Provider != "" {
		create["provider"] = request.Provider
	}
	var session struct {
		SessionID       string `json:"session_id"`
		StoredSessionID string `json:"stored_session_id"`
		Info            struct {
			Model    string `json:"model"`
			Provider string `json:"provider"`
		} `json:"info"`
	}
	if err := gateway.call(ctx, "session.create", create, &session); err != nil {
		return RunResult{}, fmt.Errorf("create Hermes session: %w", err)
	}
	if session.SessionID == "" {
		return RunResult{}, fmt.Errorf("Hermes returned an empty session id")
	}
	if request.OnExternalStarted != nil {
		if err := request.OnExternalStarted(domain.ExternalRun{
			RuntimeConnectionID: request.Connection.ID,
			RunID:               request.RunID, SessionID: session.StoredSessionID,
		}); err != nil {
			return RunResult{}, fmt.Errorf("persist Hermes session identity: %w", err)
		}
	}
	if err := gateway.call(ctx, "prompt.submit", map[string]any{
		"session_id": session.SessionID, "text": request.Prompt,
	}, nil); err != nil {
		return RunResult{}, fmt.Errorf("submit Hermes prompt: %w", err)
	}
	output, err := gateway.waitForCompletion(ctx, session.SessionID)
	if err != nil {
		return RunResult{}, err
	}
	var usage map[string]any
	if err := gateway.call(ctx, "session.usage", map[string]any{"session_id": session.SessionID}, &usage); err != nil {
		return RunResult{}, fmt.Errorf("read Hermes session usage: %w", err)
	}
	info := struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Info     struct {
			Model    string `json:"model"`
			Provider string `json:"provider"`
		} `json:"info"`
	}{
		Model: session.Info.Model, Provider: session.Info.Provider,
	}
	_ = gateway.call(ctx, "session.status", map[string]any{"session_id": session.SessionID}, &info)
	if info.Info.Model != "" {
		info.Model = info.Info.Model
	}
	if info.Info.Provider != "" {
		info.Provider = info.Info.Provider
	}
	return RunResult{
		SessionID: session.StoredSessionID, Output: output, Usage: usage,
		Model: info.Model, Provider: info.Provider,
	}, nil
}

func (c Client) httpClient(ctx context.Context, connection domain.RuntimeConnection) (*http.Client, string, error) {
	factory := c.HTTPClient
	if factory == nil {
		factory = DesktopHTTPClient
	}
	return factory(ctx, connection)
}

type desktopConnectionFile struct {
	Mode   string `json:"mode"`
	Remote struct {
		URL      string `json:"url"`
		AuthMode string `json:"authMode"`
	} `json:"remote"`
}

type credentialDocument struct {
	SessionToken string `json:"session_token"`
	Provider     string `json:"provider"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

func loadCredential(connection domain.RuntimeConnection) (credentialDocument, error) {
	var data []byte
	switch connection.CredentialSource {
	case "", "hermes_desktop":
		return credentialDocument{}, nil
	case "environment":
		value, ok := os.LookupEnv(connection.CredentialRef)
		if !ok || strings.TrimSpace(value) == "" {
			return credentialDocument{}, fmt.Errorf("Hermes credential environment variable %q is empty", connection.CredentialRef)
		}
		data = []byte(value)
	case "file":
		var err error
		info, statErr := os.Stat(connection.CredentialRef)
		if statErr != nil {
			return credentialDocument{}, fmt.Errorf("read Hermes credential file: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return credentialDocument{}, fmt.Errorf("Hermes credential file permissions must not allow group or other access")
		}
		if info.Size() > 64*1024 {
			return credentialDocument{}, fmt.Errorf("Hermes credential file exceeds 64 KiB")
		}
		data, err = os.ReadFile(connection.CredentialRef)
		if err != nil {
			return credentialDocument{}, fmt.Errorf("read Hermes credential file: %w", err)
		}
	default:
		return credentialDocument{}, fmt.Errorf("unsupported Hermes credential source %q", connection.CredentialSource)
	}
	var credential credentialDocument
	if err := json.Unmarshal(data, &credential); err != nil {
		return credentialDocument{}, fmt.Errorf("decode Hermes credential: %w", err)
	}
	hasToken := strings.TrimSpace(credential.SessionToken) != ""
	hasPassword := strings.TrimSpace(credential.Username) != "" && credential.Password != ""
	if hasToken == hasPassword {
		return credentialDocument{}, fmt.Errorf("Hermes credential must contain either session_token or username and password")
	}
	if hasPassword && credential.Provider == "" {
		credential.Provider = "basic"
	}
	return credential, nil
}

func DiscoverDesktopConnection() (domain.RuntimeConnection, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return domain.RuntimeConnection{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return LoadDesktopConnection(filepath.Join(home, defaultDesktopConfig))
}

func LoadDesktopConnection(path string) (domain.RuntimeConnection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.RuntimeConnection{}, fmt.Errorf("read Hermes Desktop connection: %w", err)
	}
	var configured desktopConnectionFile
	if err := json.Unmarshal(data, &configured); err != nil {
		return domain.RuntimeConnection{}, fmt.Errorf("decode Hermes Desktop connection: %w", err)
	}
	if configured.Mode != "remote" || strings.TrimSpace(configured.Remote.URL) == "" {
		return domain.RuntimeConnection{}, fmt.Errorf("Hermes Desktop does not have a remote Gateway configured")
	}
	if _, err := normalizeBaseURL(configured.Remote.URL); err != nil {
		return domain.RuntimeConnection{}, err
	}
	return domain.RuntimeConnection{
		ID: "hermes-desktop", Runtime: "hermes", Transport: "gateway",
		URL: configured.Remote.URL, CredentialSource: "hermes_desktop",
		DesktopConfigPath: path, MaxConcurrentRuns: 1,
	}, nil
}

func DesktopHTTPClient(ctx context.Context, connection domain.RuntimeConnection) (*http.Client, string, error) {
	baseURL := connection.URL
	configPath := connection.DesktopConfigPath
	if connection.CredentialSource == "hermes_desktop" {
		if configPath == "" {
			discovered, err := DiscoverDesktopConnection()
			if err != nil {
				return nil, "", err
			}
			configPath, baseURL = discovered.DesktopConfigPath, discovered.URL
		}
		if strings.TrimSpace(baseURL) == "" {
			discovered, err := LoadDesktopConnection(configPath)
			if err != nil {
				return nil, "", err
			}
			baseURL = discovered.URL
		}
	}
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, "", err
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	if connection.CredentialSource == "hermes_desktop" {
		home, _ := os.UserHomeDir()
		cookiesPath := filepath.Join(home, defaultDesktopCookies)
		if configPath != "" {
			cookiesPath = filepath.Join(filepath.Dir(configPath), "Partitions", "hermes-remote-oauth", "Cookies")
		}
		if err := seedDesktopCookies(cookiesPath, normalized, jar); err != nil {
			return nil, "", err
		}
	}
	credential, err := loadCredential(connection)
	if err != nil {
		return nil, "", err
	}
	if credential.SessionToken != "" {
		client.Transport = sessionTokenTransport{token: credential.SessionToken, base: http.DefaultTransport}
	}
	if credential.Username != "" {
		payload, _ := json.Marshal(map[string]string{
			"provider": credential.Provider, "username": credential.Username,
			"password": credential.Password,
		})
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, normalized+"/auth/password-login", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		response, loginErr := client.Do(request)
		if loginErr != nil {
			return nil, "", fmt.Errorf("authenticate to Hermes Gateway: %w", loginErr)
		}
		defer response.Body.Close()
		if response.StatusCode/100 != 2 {
			return nil, "", fmt.Errorf("authenticate to Hermes Gateway: HTTP %d", response.StatusCode)
		}
	}
	return client, normalized, nil
}

type sessionTokenTransport struct {
	token string
	base  http.RoundTripper
}

func (t sessionTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("X-Hermes-Session-Token", t.token)
	return t.base.RoundTrip(cloned)
}

func seedDesktopCookies(path, baseURL string, jar http.CookieJar) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open Hermes Desktop session: %w", err)
	}
	defer db.Close()
	parsed, _ := url.Parse(baseURL)
	rows, err := db.Query(`SELECT name, value, encrypted_value FROM cookies
WHERE host_key = ? AND name IN ('hermes_session_at', 'hermes_session_rt')`, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("read Hermes Desktop session: %w", err)
	}
	defer rows.Close()
	var cookies []*http.Cookie
	for rows.Next() {
		var name, value string
		var encrypted []byte
		if err := rows.Scan(&name, &value, &encrypted); err != nil {
			return fmt.Errorf("scan Hermes Desktop session: %w", err)
		}
		if value == "" && len(encrypted) > 0 {
			return fmt.Errorf("Hermes Desktop session is encrypted and cannot be imported; configure a separate Redline credential")
		}
		if value != "" {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
			cookies = append(cookies, &http.Cookie{Name: name, Value: value, Path: "/"})
		}
	}
	if len(cookies) == 0 {
		return fmt.Errorf("Hermes Desktop has no authenticated session for %s", parsed.Hostname())
	}
	jar.SetCookies(parsed, cookies)
	return rows.Err()
}

func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid Hermes Gateway URL")
	}
	parsed.RawQuery, parsed.Fragment = "", ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func getJSON(ctx context.Context, client *http.Client, target string, result any) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(result)
}

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type gatewayClient struct {
	ctx     context.Context
	cancel  context.CancelFunc
	socket  *websocket.Conn
	mu      sync.Mutex
	next    int
	pending map[string]chan rpcFrame
	events  chan rpcFrame
	errs    chan error
}

func dialGateway(
	ctx context.Context,
	client *http.Client,
	baseURL, profile string,
	connection domain.RuntimeConnection,
) (*gatewayClient, error) {
	queryKey, queryValue := "ticket", ""
	credential, err := loadCredential(connection)
	if err != nil {
		return nil, err
	}
	if credential.SessionToken != "" {
		queryKey, queryValue = "token", credential.SessionToken
	} else {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/ws-ticket", nil)
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return nil, fmt.Errorf("mint Hermes WebSocket ticket: %w", requestErr)
		}
		defer response.Body.Close()
		if response.StatusCode/100 != 2 {
			return nil, fmt.Errorf("mint Hermes WebSocket ticket: HTTP %d", response.StatusCode)
		}
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&ticket); decodeErr != nil || ticket.Ticket == "" {
			return nil, fmt.Errorf("decode Hermes WebSocket ticket")
		}
		queryValue = ticket.Ticket
	}
	parsed, _ := url.Parse(baseURL)
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/ws"
	query := parsed.Query()
	query.Set(queryKey, queryValue)
	if profile != "" {
		query.Set("profile", profile)
	}
	parsed.RawQuery = query.Encode()
	socket, _, err := websocket.Dial(ctx, parsed.String(), nil)
	if err != nil {
		message := strings.ReplaceAll(err.Error(), queryValue, "[REDACTED]")
		return nil, fmt.Errorf("open Hermes WebSocket: %s", message)
	}
	socket.SetReadLimit(4 << 20)
	readCtx, cancel := context.WithCancel(context.Background())
	gateway := &gatewayClient{
		ctx: readCtx, cancel: cancel, socket: socket, pending: make(map[string]chan rpcFrame),
		events: make(chan rpcFrame, 64), errs: make(chan error, 1),
	}
	go gateway.readLoop()
	return gateway, nil
}

func (g *gatewayClient) call(ctx context.Context, method string, params any, result any) error {
	g.mu.Lock()
	g.next++
	id := fmt.Sprintf("redline-%d", g.next)
	response := make(chan rpcFrame, 1)
	g.pending[id] = response
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.pending, id)
		g.mu.Unlock()
	}()
	if err := wsjson.Write(ctx, g.socket, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return err
	}
	select {
	case frame := <-response:
		if frame.Error != nil {
			return fmt.Errorf("Hermes RPC %s: %s", method, frame.Error.Message)
		}
		if result != nil && len(frame.Result) > 0 {
			if err := json.Unmarshal(frame.Result, result); err != nil {
				return fmt.Errorf("decode Hermes RPC %s: %w", method, err)
			}
		}
		return nil
	case err := <-g.errs:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gatewayClient) waitForCompletion(ctx context.Context, sessionID string) (string, error) {
	for {
		select {
		case frame := <-g.events:
			var event struct {
				Type      string `json:"type"`
				SessionID string `json:"session_id"`
				Payload   struct {
					Text    string `json:"text"`
					Message string `json:"message"`
				} `json:"payload"`
			}
			if json.Unmarshal(frame.Params, &event) != nil || event.SessionID != sessionID {
				continue
			}
			switch event.Type {
			case "message.complete":
				return event.Payload.Text, nil
			case "error":
				return "", fmt.Errorf("Hermes run failed: %s", event.Payload.Message)
			}
		case err := <-g.errs:
			return "", err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (g *gatewayClient) readLoop() {
	for {
		var frame rpcFrame
		if err := wsjson.Read(g.ctx, g.socket, &frame); err != nil {
			select {
			case g.errs <- err:
			default:
			}
			return
		}
		if frame.ID != "" {
			g.mu.Lock()
			target := g.pending[frame.ID]
			g.mu.Unlock()
			if target != nil {
				target <- frame
			}
		} else if frame.Method == "event" {
			g.events <- frame
		}
	}
}

func (g *gatewayClient) close() {
	g.cancel()
	_ = g.socket.Close(websocket.StatusNormalClosure, "redline completed")
}
