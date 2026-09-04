package ai.redline.app

/**
 * How the phone is currently reaching the desktop.
 *
 * The distinction is not cosmetic. A direct connection over the user's own
 * tailnet is free, private, and fast. A relayed one crosses a third party we
 * pay for, so it is the fallback rather than the default, and the UI says which
 * one is in use.
 *
 * The choosing happens in the core's client, below every request, because that
 * is the one place all ~65 endpoints already pass through. This file once also
 * held a decision function, a cadence policy and an idle tracker, each written
 * and tested while nothing called any of them -- which is how the fallback came
 * to be missing entirely while its tests passed. They were deleted rather than
 * kept for a caller that might arrive: an unused branch that looks tested is
 * worse than an absent one, because it reads as covered.
 */
enum class Transport {
    Direct,
    Relay,

    /** Nothing is reachable: no direct route and no relay to fall back to. */
    None,
}
