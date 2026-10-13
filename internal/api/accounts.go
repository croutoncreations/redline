package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"sort"
	"strings"
	"sync"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/openusage"
)

// Account management. Accounts are written to the managed accounts file
// beside the config (see config.ManagedAccount) and take effect when the
// service next starts: the scheduler and usage monitor run a fixed set of
// accounts, and swapping that set under a running dispatch is not worth the
// risk for a change made a few times a year. Every response says whether a
// restart is still needed.
//
// Nothing here signs in, stores a credential, or reads one. Linking an account
// to an OpenUsage card only records the card's opaque ID.

var accountsMu sync.Mutex

type accountView struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label,omitempty"`
	UsageSource   string `json:"usage_source"`
	OpenUsageURL  string `json:"openusage_url,omitempty"`
	OpenUsageCard string `json:"openusage_card,omitempty"`
	// ClaudeConfigDir is the account's Claude Code profile directory, with
	// the home folder shown as "~".
	ClaudeConfigDir string `json:"claude_config_dir,omitempty"`
	// Origin is "config" for accounts in the main config file (edit that
	// file to change them) and "managed" for accounts added from Redline.
	Origin   string `json:"origin"`
	Disabled bool   `json:"disabled"`
	// Running says the service is collecting usage for the account now.
	Running bool `json:"running"`
	// Pending describes a saved change the service has not applied yet:
	// "add", "change", "disable", "enable" or "remove".
	Pending string `json:"pending,omitempty"`
	// Status is the running account's collection state (see accountStatus);
	// empty when the account is not running.
	Status    string `json:"status,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// SignIn tells the owner how to connect the account, in words. Redline
	// never performs or proxies a sign-in.
	SignIn string `json:"sign_in"`
}

type accountsResponse struct {
	Accounts        []accountView `json:"accounts"`
	RestartRequired bool          `json:"restart_required"`
	AccountsFile    string        `json:"accounts_file,omitempty"`
}

type accountRequest struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Label         *string `json:"label,omitempty"`
	UsageSource   *string `json:"usage_source,omitempty"`
	OpenUsageURL  *string `json:"openusage_url,omitempty"`
	OpenUsageCard *string `json:"openusage_card,omitempty"`
	// ClaudeConfigDir is refused over HTTP. A profile directory decides
	// which local login the service reads and which login dispatched runs
	// use, and phones reach this API through Tailscale Serve and the relay,
	// which both arrive on loopback. Profiles are bound only by
	// `redline account add-profile` on the Mac, which edits the accounts
	// file directly.
	ClaudeConfigDir *string `json:"claude_config_dir,omitempty"`
	Disabled        *bool   `json:"disabled,omitempty"`
}

// errProfileOverHTTP is the refusal for a claude_config_dir sent to the API.
var errProfileOverHTTP = "claude_config_dir can only be set on the Mac running Redline, with `redline account add-profile`"

// signInGuidance is the owner-facing setup text for a provider account.
func signInGuidance(provider, card string, profileDir ...string) string {
	switch strings.ToLower(provider) {
	case "claude":
		if len(profileDir) > 0 && strings.TrimSpace(profileDir[0]) != "" {
			dir := config.DisplayClaudeConfigDir(profileDir[0])
			return "Reads the Claude Code profile in " + dir + " on the Mac running Redline, read-only. " +
				"To connect it, sign in there yourself with `CLAUDE_CONFIG_DIR=" + dir + " claude auth login`; " +
				"Redline never asks for a password or token, and never refreshes or copies that profile's login."
		}
		if card == "" || strings.EqualFold(card, "claude") {
			return "Uses the Claude account signed in to Claude Code (or OpenUsage's default Claude card) on the Mac running Redline. " +
				"To connect it, sign in there yourself with `claude auth login`; Redline never asks for a password or token."
		}
		return "Reads OpenUsage card " + card + ". Sign in to this Claude account in OpenUsage on the Mac running Redline " +
			"(OpenUsage › Settings › Accounts), then run `redline account discover claude` to confirm the card reports usage. " +
			"Redline never asks for a password or token."
	case "codex":
		return "Uses the Codex login on the Mac running Redline. Sign in there yourself with `codex login`."
	default:
		return "Sign in with the provider's own app or CLI on the Mac running Redline."
	}
}

func (s *Server) accountsPath() (string, error) {
	path := s.config.AccountsFilePath()
	if path == "" {
		return "", fmt.Errorf("account management needs the service to be started with a config file")
	}
	return path, nil
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	response, err := s.accountsSnapshot()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// accountsSnapshot merges the running configuration with the accounts file so
// a saved-but-unapplied change is visible as pending. Callers hold accountsMu.
func (s *Server) accountsSnapshot() (accountsResponse, error) {
	response := accountsResponse{Accounts: []accountView{}}
	path := s.config.AccountsFilePath()
	response.AccountsFile = path
	saved, err := config.ReadManagedAccounts(path)
	if err != nil {
		return accountsResponse{}, err
	}
	savedByID := make(map[string]config.ManagedAccount, len(saved))
	for _, account := range saved {
		savedByID[account.ID] = account
	}
	seen := map[string]bool{}
	for id, provider := range s.config.Providers {
		seen[id] = true
		view := accountView{ID: id, Provider: provider.Provider, Label: provider.Label, UsageSource: provider.EffectiveUsageSource(),
			OpenUsageURL: provider.OpenUsageURL, OpenUsageCard: provider.OpenUsageCard, Origin: "config", Running: true,
			ClaudeConfigDir: displayProfileDir(provider.ClaudeConfigDir),
			SignIn:          signInGuidance(provider.Provider, provider.OpenUsageCard, provider.ClaudeConfigDir)}
		if s.config.ManagedAccounts[id] {
			view.Origin = "managed"
			account, stillSaved := savedByID[id]
			switch {
			case !stillSaved:
				view.Pending = "remove"
			case account.Disabled:
				view.Pending = "disable"
			case !sameManagedFields(account.ProviderConfig(), provider):
				view.Pending = "change"
			}
		}
		status := s.usageSources.Status(id)
		view.LastError = openusage.RedactEmails(status.LastError)
		view.Status = s.liveAccountStatus(id, provider, status.Reason, status.LastError)
		response.Accounts = append(response.Accounts, view)
	}
	for _, account := range saved {
		if seen[account.ID] {
			continue
		}
		provider := account.ProviderConfig()
		view := accountView{ID: account.ID, Provider: provider.Provider, Label: account.Label, UsageSource: provider.EffectiveUsageSource(),
			OpenUsageURL: account.OpenUsageURL, OpenUsageCard: account.OpenUsageCard, Origin: "managed", Disabled: account.Disabled,
			ClaudeConfigDir: displayProfileDir(account.ClaudeConfigDir),
			SignIn:          signInGuidance(provider.Provider, account.OpenUsageCard, account.ClaudeConfigDir)}
		_, wasDisabledAtStart := s.config.DisabledAccounts[account.ID]
		switch {
		case !account.Disabled && wasDisabledAtStart:
			view.Pending = "enable"
		case !account.Disabled:
			view.Pending = "add"
		}
		response.Accounts = append(response.Accounts, view)
	}
	for _, view := range response.Accounts {
		if view.Pending != "" {
			response.RestartRequired = true
		}
	}
	// An account disabled at start and since removed from the file.
	for id := range s.config.DisabledAccounts {
		if _, stillSaved := savedByID[id]; !stillSaved {
			response.RestartRequired = true
		}
	}
	sort.Slice(response.Accounts, func(i, j int) bool { return response.Accounts[i].ID < response.Accounts[j].ID })
	return response, nil
}

// sameManagedFields compares the fields a managed account can set, so a
// running provider matches the file entry it was built from.
func sameManagedFields(saved, running config.Provider) bool {
	return saved.Provider == running.Provider && saved.Label == running.Label && saved.UsageSource == running.UsageSource &&
		saved.OpenUsageURL == running.OpenUsageURL && saved.OpenUsageCard == running.OpenUsageCard &&
		config.NormalizeClaudeConfigDir(saved.ClaudeConfigDir) == config.NormalizeClaudeConfigDir(running.ClaudeConfigDir) &&
		saved.WindowWeeklyCost == running.WindowWeeklyCost && saved.Policy == running.Policy
}

func (s *Server) liveAccountStatus(id string, provider config.Provider, reason, lastError string) string {
	snapshot, _, err := s.store.LatestSnapshot(contextBackground(), id, provider.Provider)
	item := dashboardProvider{UsageSource: s.usageSources.Status(id)}
	item.UsageSource.Reason, item.UsageSource.LastError = reason, lastError
	if err == nil {
		item.Snapshot = &snapshot
		maxAge, _ := s.config.SnapshotAge()
		age := s.now().Sub(snapshot.ObservedAt)
		item.SnapshotStale = age > maxAge || age < 0 || snapshot.SourceStale
	}
	return accountStatus(item)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var request accountRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	account := config.ManagedAccount{ID: strings.TrimSpace(request.ID), Provider: strings.ToLower(strings.TrimSpace(request.Provider))}
	if account.Provider == "" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider is required"})
		return
	}
	if account.Provider != "claude" && account.Provider != "codex" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider must be claude or codex; other providers are configured in the config file"})
		return
	}
	if err := config.ValidateAccountID(account.ID); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if !validAccountURL(request) {
		writeJSON(w, http.StatusBadRequest, problem{Code: "invalid_account", Error: "openusage_url must be a loopback http URL"})
		return
	}
	if request.ClaudeConfigDir != nil {
		writeJSON(w, http.StatusBadRequest, problem{Code: "invalid_account", Error: errProfileOverHTTP})
		return
	}
	applyAccountRequest(&account, request)
	s.mutateAccounts(w, http.StatusCreated, func(saved []config.ManagedAccount) ([]config.ManagedAccount, error) {
		if s.config.ConfigFileAccount(account.ID) {
			return nil, accountConflict("account %q is defined in the config file", account.ID)
		}
		for _, existing := range saved {
			if existing.ID == account.ID {
				return nil, accountConflict("account %q already exists", account.ID)
			}
		}
		// A disabled account kept on file still has an ID; adding a new
		// one with the same ID would make the old one unreachable.
		return append(saved, account), nil
	})
}

func applyAccountRequest(account *config.ManagedAccount, request accountRequest) {
	if request.Label != nil {
		account.Label = strings.TrimSpace(*request.Label)
	}
	if request.UsageSource != nil {
		account.UsageSource = strings.ToLower(strings.TrimSpace(*request.UsageSource))
	}
	if request.OpenUsageURL != nil {
		account.OpenUsageURL = strings.TrimSpace(*request.OpenUsageURL)
	}
	if request.OpenUsageCard != nil {
		account.OpenUsageCard = strings.ToLower(strings.TrimSpace(*request.OpenUsageCard))
	}
	if request.Disabled != nil {
		account.Disabled = *request.Disabled
	}
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("account")
	var request accountRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if (request.ID != "" && request.ID != id) || request.Provider != "" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "an account's id and provider cannot be changed; add a new account instead"})
		return
	}
	if !validAccountURL(request) {
		writeJSON(w, http.StatusBadRequest, problem{Code: "invalid_account", Error: "openusage_url must be a loopback http URL"})
		return
	}
	if request.ClaudeConfigDir != nil {
		writeJSON(w, http.StatusBadRequest, problem{Code: "invalid_account", Error: errProfileOverHTTP})
		return
	}
	s.mutateAccounts(w, http.StatusOK, func(saved []config.ManagedAccount) ([]config.ManagedAccount, error) {
		if s.config.ConfigFileAccount(id) {
			return nil, accountConflict("account %q is defined in the config file; edit that file to change it", id)
		}
		for index := range saved {
			if saved[index].ID == id {
				applyAccountRequest(&saved[index], request)
				return saved, nil
			}
		}
		return nil, errAccountNotFound
	})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("account")
	s.mutateAccounts(w, http.StatusOK, func(saved []config.ManagedAccount) ([]config.ManagedAccount, error) {
		if s.config.ConfigFileAccount(id) {
			return nil, accountConflict("account %q is defined in the config file; edit that file to remove it", id)
		}
		// Read under the accounts lock, so a profile created meanwhile
		// through the API cannot slip in between the check and the write.
		profiles, err := s.store.ListProfiles(r.Context())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errStore, err)
		}
		var referencing []string
		for _, profile := range profiles {
			if profile.ProviderAccountID == id {
				referencing = append(referencing, profile.ID)
			}
		}
		if len(referencing) > 0 {
			sort.Strings(referencing)
			return nil, accountConflict("account %q is used by execution profiles %s; disable it instead, or move those profiles first",
				id, strings.Join(referencing, ", "))
		}
		for index := range saved {
			if saved[index].ID == id {
				return append(saved[:index], saved[index+1:]...), nil
			}
		}
		return nil, errAccountNotFound
	})
}

var errAccountNotFound = errors.New("account is not managed by Redline")

func accountConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", config.ErrAccountConflict, fmt.Sprintf(format, args...))
}

// mutateAccounts applies change to the saved accounts, validates the whole
// resulting configuration exactly as the next start would load it, and only
// then writes the file. A change the next start would refuse is never saved.
func (s *Server) mutateAccounts(w http.ResponseWriter, status int,
	change func([]config.ManagedAccount) ([]config.ManagedAccount, error),
) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	path, err := s.accountsPath()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, problem{Error: err.Error()})
		return
	}
	saved, err := config.ReadManagedAccounts(path)
	if err != nil {
		writeError(w, err)
		return
	}
	next, err := change(append([]config.ManagedAccount(nil), saved...))
	if err == nil {
		_, err = s.config.WithManagedAccounts(next)
	}
	if err == nil {
		err = disabledDuplicates(s.config, next)
	}
	switch {
	case errors.Is(err, errAccountNotFound):
		writeJSON(w, http.StatusNotFound, problem{Code: "account_not_found", Error: err.Error()})
		return
	case errors.Is(err, config.ErrAccountConflict):
		writeJSON(w, http.StatusConflict, problem{Code: "account_conflict", Error: err.Error()})
		return
	case errors.Is(err, errStore):
		writeError(w, err)
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, problem{Code: "invalid_account", Error: err.Error()})
		return
	}
	if err := config.WriteManagedAccounts(path, next); err != nil {
		writeError(w, err)
		return
	}
	response, err := s.accountsSnapshot()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, response)
}

type discoverResponse struct {
	Provider string           `json:"provider"`
	Source   string           `json:"source"`
	Cards    []discoveredCard `json:"cards"`
	Error    string           `json:"error,omitempty"`
	Hint     string           `json:"hint"`
}

type discoveredCard struct {
	openusage.CardSummary
	// LinkedAccount is the account already reading this card, running or
	// saved, so the setup flow can refuse a duplicate before submitting.
	LinkedAccount string `json:"linked_account,omitempty"`
}

// discoverAccounts lists the OpenUsage cards available for a provider. It
// only reads what OpenUsage already has; it never signs in or changes it.
func (s *Server) discoverAccounts(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if provider != "claude" && provider != "codex" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider must be claude or codex"})
		return
	}
	url := strings.TrimSpace(r.URL.Query().Get("openusage_url"))
	if url == "" {
		// The default local OpenUsage, always: picking from the configured
		// accounts would depend on map order.
		url = config.DefaultOpenUsageURL
	} else if !loopbackURL(url) {
		writeJSON(w, http.StatusBadRequest, problem{Error: "openusage_url must be a loopback http URL"})
		return
	}
	response := discoverResponse{Provider: provider, Source: url, Cards: []discoveredCard{},
		Hint: "Sign in to more " + provider + " accounts in OpenUsage on this Mac to see them here. Redline never signs in for you."}
	cards, err := (openusage.Client{BaseURL: url}).ListCards(r.Context(), provider)
	if err != nil {
		response.Error = "OpenUsage is not reachable: " + openusage.RedactEmails(err.Error())
		writeJSON(w, http.StatusOK, response)
		return
	}
	accountsMu.Lock()
	linked := map[string]string{}
	for id, configured := range s.config.Providers {
		if strings.EqualFold(configured.Provider, provider) {
			linked[config.MeteringIdentity(configured)] = id
		}
	}
	if saved, readErr := config.ReadManagedAccounts(s.config.AccountsFilePath()); readErr == nil {
		for _, account := range saved {
			if strings.EqualFold(account.Provider, provider) {
				identity := config.MeteringIdentity(account.ProviderConfig())
				if _, taken := linked[identity]; !taken {
					linked[identity] = account.ID
				}
			}
		}
	}
	accountsMu.Unlock()
	for _, card := range cards {
		identity := config.MeteringIdentity(config.Provider{Provider: provider, OpenUsageURL: url, OpenUsageCard: card.ID})
		response.Cards = append(response.Cards, discoveredCard{CardSummary: card, LinkedAccount: linked[identity]})
	}
	writeJSON(w, http.StatusOK, response)
}

// loopbackURL accepts only http URLs on this machine, so the discover call
// cannot be pointed at another host from a paired device.
func loopbackURL(raw string) bool {
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	return loopbackHost(parsed.Host)
}

func contextBackground() context.Context { return context.Background() }

func displayProfileDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return config.DisplayClaudeConfigDir(dir)
}

// errStore marks a database failure inside an account change, which is a
// server error rather than a refused request.
var errStore = errors.New("account store")

// validAccountURL refuses a non-loopback OpenUsage URL from the API: a
// paired device must not be able to point the service at another host.
// Hand-written config files may still use any URL.
func validAccountURL(request accountRequest) bool {
	if request.OpenUsageURL == nil || strings.TrimSpace(*request.OpenUsageURL) == "" {
		return true
	}
	return loopbackURL(strings.TrimSpace(*request.OpenUsageURL))
}

// disabledDuplicates refuses saving a managed account that would read the
// same usage as any other account once both are switched on, so a disabled
// account can always be turned back on. Pairs written entirely in the config
// file are that file's business (they load with a warning).
func disabledDuplicates(cfg config.Config, accounts []config.ManagedAccount) error {
	owners := map[string]string{}
	for id, provider := range cfg.Providers {
		if !cfg.ManagedAccounts[id] && !strings.EqualFold(provider.Provider, "cursor") {
			owners[config.MeteringIdentity(provider)] = id
		}
	}
	for _, account := range accounts {
		identity := config.MeteringIdentity(account.ProviderConfig())
		if other, taken := owners[identity]; taken {
			return accountConflict("accounts %q and %q would read the same usage when both are on", other, account.ID)
		}
		owners[identity] = account.ID
	}
	return nil
}

// accountLeaving explains why a running managed account must not gain a new
// profile: it has been removed or switched off in the accounts file and goes
// away at the next restart. Callers hold accountsMu. "" means it may.
func (s *Server) accountLeaving(id string) string {
	if !s.config.ManagedAccounts[id] {
		return ""
	}
	saved, err := config.ReadManagedAccounts(s.config.AccountsFilePath())
	if err != nil {
		return "the accounts file cannot be read: " + err.Error()
	}
	for _, account := range saved {
		if account.ID == id {
			if account.Disabled {
				return fmt.Sprintf("account %q has been switched off; turn it back on before giving it tasks", id)
			}
			return ""
		}
	}
	return fmt.Sprintf("account %q has been removed and goes away when Redline restarts", id)
}
