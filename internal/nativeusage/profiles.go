package nativeusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ClaudeProfile is what Redline learns about a Claude Code profile directory
// without touching its login: the directory, an opaque fingerprint of the
// account it belongs to, its plan words, and whether a login is stored.
// Names, email addresses and organization names in the profile's metadata
// are never read into it.
type ClaudeProfile struct {
	Dir string `json:"dir"`
	// Fingerprint is a short SHA-256 of the account's opaque UUID, so two
	// directories signed in to the same account can be recognised without
	// carrying the UUID itself. "" when the profile names no account.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Plan is the subscription in words ("Team", "Max 5x"), from the
	// profile's own metadata; "" when it does not say.
	Plan string `json:"plan,omitempty"`
	// Role is the person's role on a Team plan (owner, admin, member).
	Role string `json:"role,omitempty"`
	// LoginStored says Claude Code keeps a login for this profile. Only the
	// item's presence is checked, never its contents.
	LoginStored bool `json:"login_stored"`
}

// profileMetadata is the subset of a profile's .claude.json Redline reads.
// The JSON decoder ignores every other field, including the person's name
// and email address, which are deliberately absent here.
type profileMetadata struct {
	OAuthAccount *struct {
		AccountUUID               string `json:"accountUuid"`
		OrganizationType          string `json:"organizationType"`
		OrganizationRole          string `json:"organizationRole"`
		OrganizationRateLimitTier string `json:"organizationRateLimitTier"`
		UserRateLimitTier         string `json:"userRateLimitTier"`
	} `json:"oauthAccount"`
}

// InspectClaudeProfile reads a profile directory's non-secret metadata. A
// directory without Claude Code metadata is an error: it is not a profile.
func InspectClaudeProfile(ctx context.Context, dir string) (ClaudeProfile, error) {
	profile := ClaudeProfile{Dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if errors.Is(err, os.ErrNotExist) {
		return profile, fmt.Errorf("%s is not a Claude Code profile (no .claude.json)", displayDir(dir))
	}
	if err != nil {
		return profile, fmt.Errorf("read Claude Code profile metadata: %w", err)
	}
	var metadata profileMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return profile, fmt.Errorf("Claude Code profile metadata in %s is not valid JSON", displayDir(dir))
	}
	if account := metadata.OAuthAccount; account != nil {
		if uuid := strings.TrimSpace(account.AccountUUID); uuid != "" {
			sum := sha256.Sum256([]byte("redline-claude-account:" + uuid))
			profile.Fingerprint = hex.EncodeToString(sum[:])[:12]
		}
		profile.Plan = planWords(account.OrganizationType, account.UserRateLimitTier, account.OrganizationRateLimitTier)
		profile.Role = roleWords(account.OrganizationRole)
	}
	profile.LoginStored, err = ClaudeProfileLoginPresent(ctx, dir)
	if err != nil {
		return profile, err
	}
	return profile, nil
}

// FindClaudeProfiles lists the Claude Code profile directories directly in
// home whose names start with prefix (for example ".claude-cc-"), sorted.
func FindClaudeProfiles(ctx context.Context, home, prefix string) ([]ClaudeProfile, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	var profiles []ClaudeProfile
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || entry.Name() == prefix {
			continue
		}
		profile, err := InspectClaudeProfile(ctx, filepath.Join(home, entry.Name()))
		if err != nil {
			continue
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Dir < profiles[j].Dir })
	return profiles, nil
}

// DefaultClaudeFingerprint fingerprints the default login's account (from
// ~/.claude.json), so a profile signed in to the same account as the default
// login can be recognised as a duplicate. "" when unknown.
func DefaultClaudeFingerprint(home string) string {
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return ""
	}
	var metadata profileMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.OAuthAccount == nil {
		return ""
	}
	uuid := strings.TrimSpace(metadata.OAuthAccount.AccountUUID)
	if uuid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("redline-claude-account:" + uuid))
	return hex.EncodeToString(sum[:])[:12]
}

func planWords(organizationType, userTier, organizationTier string) string {
	tier := userTier
	if tier == "" {
		tier = organizationTier
	}
	size := ""
	switch {
	case strings.Contains(tier, "max_20x"):
		size = "Max 20x"
	case strings.Contains(tier, "max_5x"):
		size = "Max 5x"
	}
	switch organizationType {
	case "claude_team":
		if size != "" {
			return "Team · " + size
		}
		return "Team"
	case "claude_enterprise":
		return "Enterprise"
	case "claude_max":
		if size != "" {
			return size
		}
		return "Max"
	case "claude_pro":
		return "Pro"
	}
	return size
}

func roleWords(role string) string {
	switch role {
	case "primary_owner", "owner":
		return "owner"
	case "admin":
		return "admin"
	case "user", "member":
		return "member"
	}
	return ""
}
