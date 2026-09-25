package openusage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/openusage"
)

func codexWithResets(line string) []byte {
	return []byte(fmt.Sprintf(`{"providerId":"codex","fetchedAt":"2026-09-24T14:17:15Z","lines":[
		{"type":"progress","label":"Weekly","used":36,"limit":100,"resetsAt":"2026-09-26T15:32:04Z"}%s,
		{"type":"text","label":"Credits","value":"$0.00 · 0 credits"}]}`, line))
}

// Shape taken from OpenUsage 0.7.5's live Codex response.
func TestParseReadsCodexBankedResetsAndExpiry(t *testing.T) {
	got, err := openusage.Parse(codexWithResets(`,{"type":"text","label":"Rate Limit Resets","value":"2 available","resetsAt":"2026-10-05T04:22:40.312Z"}`), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets == nil || *got.BankedResets != 2 {
		t.Fatalf("resets = %v, want 2", got.BankedResets)
	}
	want := time.Date(2026, 10, 5, 4, 22, 40, 312000000, time.UTC)
	if got.BankedResetsExpireAt == nil || !got.BankedResetsExpireAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got.BankedResetsExpireAt, want)
	}
}

func TestParseBankedResetsZeroAbsentAndUnreadable(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *int
	}{
		{"absent", ``, nil},
		{"zero", `,{"type":"text","label":"Rate Limit Resets","value":"0 available"}`, ptr(0)},
		{"none", `,{"type":"text","label":"Rate Limit Resets","value":"None available"}`, ptr(0)},
		{"prose zero", `,{"type":"text","label":"Rate Limit Resets","value":"You have no rate limit resets"}`, ptr(0)},
		{"unreadable", `,{"type":"text","label":"Rate Limit Resets","value":"several"}`, nil},
		{"bad expiry keeps count", `,{"type":"text","label":"Rate Limit Resets","value":"1 available","resetsAt":"soon"}`, ptr(1)},
		{"lapsed expiry dropped", `,{"type":"text","label":"Rate Limit Resets","value":"1 available","resetsAt":"2026-09-01T00:00:00Z"}`, ptr(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := openusage.Parse(codexWithResets(tc.line), "codex")
			if err != nil {
				t.Fatal(err)
			}
			if (got.BankedResets == nil) != (tc.want == nil) || (tc.want != nil && *got.BankedResets != *tc.want) {
				t.Fatalf("resets = %v, want %v", got.BankedResets, tc.want)
			}
			if got.BankedResetsExpireAt != nil {
				t.Fatalf("expiry = %v, want none", got.BankedResetsExpireAt)
			}
		})
	}
}

func ptr(v int) *int { return &v }
