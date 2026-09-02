#!/usr/bin/env bash

# Helpers for driving hdiutil reliably in CI.
#
# hdiutil detach returns once the unmount has been *requested*; macOS tears the
# backing device down asynchronously. A subsequent hdiutil create can therefore
# fail with "Resource busy" while the previous image is still releasing, which
# shows up as an unrelated red build on whichever PR happened to be running.
# These wrappers wait for the teardown to finish and retry the transient
# failures instead.

# detach_dmg unmounts a mountpoint and waits for the device to be released.
# Missing or already-detached mountpoints are treated as success so it stays
# safe to call from a cleanup trap.
detach_dmg() {
  local mount_path="$1"
  local attempt

  if [[ ! -d "${mount_path}" ]]; then
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
    if ! mount | grep -q " on ${mount_path} "; then
      return 0
    fi
    sleep 1
  done

  printf 'hdiutil detach did not release %s\n' "${mount_path}" >&2
  return 1
}

# create_dmg wraps hdiutil create, retrying the transient "Resource busy"
# failure that occurs when a previous image is still detaching.
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
    if [[ "${output}" != *"Resource busy"* ]]; then
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
