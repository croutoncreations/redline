package greenlane

// Claude's GitHub App posts these COMMENTED reviews when it did not review the
// PR at all. They contain no findings, so they are not feedback to address.
// They are ignored only when the body matches byte for byte and the author is
// the immutable claude[bot] identity. Any other text from Claude, including a
// reworded notice, still blocks the live lane. These notices never count as a
// completed review either.
const claudeBotUserID int64 = 209825114

const claudeNoticeNoUsage = "⚠️ **Code review skipped** — your organization has no extra usage available to pay for this review.\n\nIf your organization's extra usage balance is empty, an organization admin can add extra usage credits at [claude.ai/admin-settings/usage](https://claude.ai/admin-settings/usage). If its monthly spend limit was reached, an admin can raise it on the same page. If neither applies, contact Anthropic support.\n\nOnce extra usage is available, someone with write access to this repository can comment `@claude review` on this pull request to trigger a review."

const claudeNoticeBotRequester = "## Claude Code Review\n\nNo review was started: this request came from a bot account. Manual reviews can only be requested by someone with write access to this repository. Ask a maintainer to comment `@claude review`, or have your automation post the comment from a user account with write access.\n\n<sub>Tip: disable this comment in your organization's [Code Review settings](https://claude.ai/admin-settings/claude-code#code-review).</sub>"

var claudeNoReviewNotices = map[string]bool{
	claudeNoticeNoUsage:      true,
	claudeNoticeBotRequester: true,
}

func isClaudeNoReviewNotice(r Review) bool {
	return r.UserID == claudeBotUserID && r.UserType == "Bot" && r.User == "claude[bot]" &&
		r.State == "COMMENTED" && claudeNoReviewNotices[r.Body]
}
