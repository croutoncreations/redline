#!/usr/bin/env bash

# Helpers for driving hdiutil reliably in CI.
#
# hdiutil detach returns once the unmount has been *requested*; macOS tears the
# backing device down asynchronously. A subsequent hdiutil create can therefore
# fail with "Resource busy" while the previous image is still releasing, which
# shows up as an unrelated red build on whichever PR happened to be running.
# These wrappers wait for the teardown to finish and retry the transient
# failures instead.

# is_mounted reports whether a path currently appears in the mount table.
#
# The path is resolved first: macOS reports the physical mountpoint, so an
# image attached at /tmp/x is listed as /private/tmp/x because /tmp is a
# symlink. Comparing the unresolved path would never match, which would skip
# the teardown wait entirely and silently reintroduce the race this guards.
#
# grep -F keeps the path a literal, since a path containing regex
# metacharacters would otherwise match the wrong line. macOS prints
# "/dev/diskNsM on /path (apfs, ...)", so the surrounding spaces anchor the
# match and stop /tmp/foo matching /tmp/foobar.
is_mounted() {
  local mount_path="$1"
  local resolved
  # cd -P resolves symlinks without requiring realpath, which is not present
  # on stock macOS.
  resolved="$(cd -P "${mount_path}" 2>/dev/null && pwd -P)" || resolved=""
  if [[ -n "${resolved}" ]] && mount | grep -qF " on ${resolved} "; then
    return 0
  fi
  mount | grep -qF " on ${mount_path} "
}

# detach_dmg unmounts a mountpoint and waits for the device to be released.
# Paths that are not mounted are treated as success so it stays safe to call
# from a cleanup trap, including on the common path where the script already
# detached cleanly.
detach_dmg() {
  local mount_path="$1"
  local attempt

  # Check the mount table rather than the directory: the mountpoint directory
  # outlives the mount, so testing for the directory would send an
  # already-unmounted path through the whole retry loop on every clean run.
  if ! is_mounted "${mount_path}"; then
    return 0
  fi

  for attempt in 1 2 3 4 5; do
    if hdiutil detach "${mount_path}" -quiet 2>/dev/null; then
      break
    fi
    # Still busy: a process may hold a file open. Escalate after a short wait.
    sleep "${attempt}"
    if hdiutil detach "${mount_path}" -force -quiet 2>/dev/null; then
      break
    fi
  done

  # detach can return before the mountpoint is gone; wait for it to disappear
  # so a following create does not race the teardown.
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    if ! is_mounted "${mount_path}"; then
      return 0
    fi
    sleep 1
  done

  printf 'hdiutil detach did not release %s\n' "${mount_path}" >&2
  return 1
}

# create_dmg wraps hdiutil create, retrying the transient busy-resource failure
# that occurs when a previous image is still detaching.
#
# hdiutil returns a generic non-zero exit code for every failure, so the message
# is the only signal that separates "retry" from "give up". The match is
# deliberately narrow: a real failure such as a full disk must surface
# immediately rather than after three attempts.
#
# It matches "busy" only on hdiutil's own "create failed" diagnostic, and
# case-insensitively, so a reworded or lowercased message ("Resource busy",
# "device busy") still retries while the word appearing anywhere else in the
# output does not. If the wording changes completely this degrades to failing
# fast, which is loud rather than silent.
create_dmg() {
  local dmg_path="$1"
  local staging_path="$2"
  local attempt
  local output

  for attempt in 1 2 3; do
    if output="$(hdiutil create -volname Redline -srcfolder "${staging_path}" \
      -ov -format UDZO "${dmg_path}" 2>&1)"; then
      printf '%s\n' "${output}"
      return 0
    fi
    if ! printf '%s\n' "${output}" | grep -qi 'create failed.*busy'; then
      printf '%s\n' "${output}" >&2
      return 1
    fi
    printf 'hdiutil create hit a busy resource; retrying (attempt %d)\n' "${attempt}" >&2
    sleep $((attempt * 3))
  done

  printf 'hdiutil create still reported a busy resource after retries\n' >&2
  printf '%s\n' "${output}" >&2
  return 1
}
