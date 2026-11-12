# Keeping Claude Code profile logins fresh

Status: plan (2026-11-11). Waiting on the overnight test in step 0.

## Problem

Since 0.1.13, team seats are Claude Code profile folders (`~/.claude-cc-<name>`).
Redline reads each profile's login (keychain `Claude Code-credentials-<sha8>`)
and never writes it (`nativeusage/credentials.go`, `AccessClaudeProfile`). The
access token lasts 8 hours. Only Claude Code renews it, and only when it runs
under that profile. An idle seat therefore goes stale 8 hours after it was last
used:

- `AccessClaudeProfile` returns `needs_session` within 5 minutes of expiry.
- The snapshot ages out, and the account shows "Out of date, dispatch held" on
  the Mac and the phone.
- It stays that way until someone runs Claude Code under the profile by hand.

OpenUsage never had this problem because it renews the login itself: it calls
`platform.claude.com/v1/oauth/token` and writes the result to the keychain.
Redline deliberately stopped writing other apps' credentials in 0.1.6, and a
renewal racing a live Claude Code session can invalidate the refresh token and
sign the seat out.

## Approach

Redline has Claude Code renew its own login. Shortly before a profile's access
token expires, Redline runs Claude Code under that profile in the background.
Claude Code renews the token and saves it as it normally does. Redline still
never writes a credential, and Claude Code's own locking handles any concurrent
session.

### 0. Which command renews (overnight test, 2026-11-12 ~03:44)

`~/.cache/rlproof/keepalive-test.py` reads each seat's expiry (read-only) and:

- for `frank`, runs `claude auth status` 4 minutes before expiry (inside
  Claude Code's usual refresh window);
- for the other four, runs it 2 minutes after expiry;
- records the expiry before and after each run;
- for any profile still lapsed, falls back to a one-word haiku prompt, then
  has Redline refresh each account.

Outcome decides the command:

| `auth status` renews | Keep-alive command |
|---|---|
| before and after expiry | `claude auth status` (no quota) |
| only after expiry | `auth status` just after expiry. Collection briefly sees `needs_session` (one cycle), so the snapshot is not marked stale for it. |
| never | `claude -p ok --model haiku` with tools, MCP and session persistence off. A few hundred tokens per seat every ~8h, counted against that seat. |

Don't use `--bare`: its help says it changes how authentication works.

### 1. `internal/profilelogin` (new)

- `Keeper.Check(ctx, account)`:
  - read the expiry via a new read-only `nativeusage.ProfileLoginState(dir)`,
    which returns expiry, has-refresh-token and signed-out;
  - if the token expires within `lead` (default 15m), or has already expired
    and a refresh token exists, run the renew command.
- Run the command with `CLAUDE_CONFIG_DIR=<dir>`, with the same env scrubbing
  as `harness.buildCommand` (drop inherited `CLAUDE_CONFIG_DIR`, tokens and
  routing variables). Use `/tmp` as the working directory and a 90s timeout.
  Find `claude` with `exec.LookPath` under the service PATH; the macOS app
  already adds `~/.gatepost/bin` and `~/.local/bin`.
- Re-read the expiry afterwards. Success means the expiry moved forward.
- Only one renewal runs at a time, across all profiles. If a renewal fails,
  back off for 30 minutes before retrying that profile.
- Skip a profile while a Redline task is running under it: that session renews
  the login itself.
- Never touch the default login (OpenUsage handles that) and never touch
  Codex.
- Only accounts that Redline manages and that are enabled are kept alive.

### 2. Hook into the usage monitor

Call `Keeper.Check` in `monitorProvider` (`api/server.go`) before
`fetchAndStore`, for profile accounts only. The monitor runs every 5 minutes,
so a 15-minute lead gives about three tries before expiry. A `needs_session`
result from `fetchAndStore` also triggers an immediate renewal and one retry
of the fetch, so an account that has already lapsed recovers in the same cycle
rather than waiting 5 minutes.

### 3. When renewal can't work

If the refresh token is gone, Claude rejects it, or Claude Code reports logged
out:

- New account status `needs_login` (separate from `stale`), with the reason
  "Claude Code profile ~/.claude-cc-jon is signed out. Run
  `CLAUDE_CONFIG_DIR=~/.claude-cc-jon claude` and sign in (`/login`)."
- New notification event `account.needs_login`, sent once per profile per
  sign-out. Dedupe it with a small store table `profile_login_notices
  (account, signed_out_at, notified_at)`, cleared when the login recovers. It
  goes to Mac notifications and paired phones, and its title and message go in
  `notification/messages.go`.
- Add `needs_login` to the dashboard, `/m` and Android, showing the command to
  run rather than "Out of date".

### 4. Setting

In `redline.yaml`:

```yaml
usage_monitor:
  keep_profile_logins_fresh: true   # default true; profile accounts only
```

Document it in `config.example.yaml` and `docs/multi-account.md`. When it's
off, today's behaviour is kept, plus the step 3 notification.

### 5. Visibility

- Log each renewal under the `profile_login` log tag, with the account, old
  and new expiry, and how long it took.
- Add `GET /v1/accounts` fields `login_expires_at` and `login_renewed_at`.
- Add `redline account status` lines such as "login renews automatically
  (next ~03:30)".

## Tests

- Unit tests with a fake clock and a fake runner:
  - renews inside the lead window, but not outside it;
  - success means the expiry moved forward;
  - backs off after a failure;
  - skips while a task is running;
  - ignores the default login and Codex;
  - only one renewal at a time.
- `needs_session` from fetch causes a renewal and one retry.
- Signed out sends one notification, then no repeats; recovery clears it and a
  later sign-out notifies again.
- Env scrubbing matches the harness (a shared helper, tested once).
- Live check on this Mac: leave the five seats idle past an 8-hour expiry and
  confirm none goes stale, with renewals in the log.

## Out of scope

- Writing credentials (option 2).
- Keeping the default login fresh (OpenUsage, or a running Claude Code, does
  that).
- Codex profiles.
