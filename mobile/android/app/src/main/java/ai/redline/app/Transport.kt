package ai.redline.app

/**
 * How the phone is currently reaching the desktop.
 *
 * The distinction is not cosmetic. A direct connection over the user's own
 * tailnet is free, private, and fast. A relayed one crosses a third party we
 * pay for, so it is the fallback rather than the default, and it carries
 * guardrails a direct connection does not need.
 */
enum class Transport {
    Direct,
    Relay,

    /** Nothing is reachable: no direct route and no relay to fall back to. */
    None,
}

/**
 * Picks a transport.
 *
 * Direct wins whenever it is available. Falling back to a relay that is not
 * configured would report a relay problem to someone who never opted into one,
 * sending them to look for the wrong fault.
 */
fun chooseTransport(directReachable: Boolean, relayConfigured: Boolean): Transport = when {
    directReachable -> Transport.Direct
    relayConfigured -> Transport.Relay
    else -> Transport.None
}

/**
 * How often to ask for a live update.
 *
 * Five seconds is right on loopback, where a frame costs nothing. Over a relay
 * each frame is a billed request and a slice of a daily duration allowance, and
 * the difference between five and twenty seconds is roughly four times the cost
 * for numbers almost nobody watches that closely.
 */
fun liveCadenceSeconds(transport: Transport): Int = when (transport) {
    Transport.Direct -> 5
    Transport.Relay -> 20
    Transport.None -> 20
}

/**
 * Whether an idle session should be dropped.
 *
 * An app left open on a desk is the expensive case: a permanently connected
 * relayed session is most of the free tier's daily allowance on its own. A
 * direct session costs nothing to hold, so dropping it would be a regression
 * for someone sitting at home.
 */
fun shouldDropWhenIdle(transport: Transport): Boolean = transport == Transport.Relay

/**
 * Tracks whether a relayed session has gone quiet.
 *
 * Times are passed in rather than read from a clock so the behaviour can be
 * tested without waiting five real minutes.
 */
class RelayIdleTracker(private val idleMillis: Long) {
    private var lastTraffic: Long = 0

    fun sawTraffic(at: Long) {
        lastTraffic = at
    }

    fun expired(now: Long): Boolean = now - lastTraffic > idleMillis
}

/** What the relay said about this session. */
enum class RelayStatus {
    Connected,

    /** The relay is up but will not carry this session without an upgrade. */
    NeedsUpgrade,

    /** The credential is wrong or has been revoked. */
    Unauthorized,

    /** Another device already holds this session. */
    Busy,

    /** The relay is down, or has hit a limit. Nothing the user did. */
    Unavailable,
}

/**
 * Maps the relay's HTTP response to a status.
 *
 * These are kept apart on purpose. Free-tier limits are hard stops rather than
 * overage billing, so a relay can simply refuse; telling someone to re-pair
 * because of that would send them to redo the one thing that was working.
 * Equally, treating a rejected credential as an outage would leave them waiting
 * for a service that is already fine.
 */
fun relayStatusFor(httpStatus: Int): RelayStatus = when (httpStatus) {
    101 -> RelayStatus.Connected
    401, 403 -> RelayStatus.Unauthorized
    402 -> RelayStatus.NeedsUpgrade
    409 -> RelayStatus.Busy
    else -> RelayStatus.Unavailable
}

/** Wording a person can act on, for each status. */
fun relayStatusMessage(status: RelayStatus): String = when (status) {
    RelayStatus.Connected -> "Connected through the relay"
    RelayStatus.NeedsUpgrade -> "Remote access needs an upgrade on this account"
    RelayStatus.Unauthorized -> "This device is no longer authorised. Pair it again."
    RelayStatus.Busy -> "Another device is already connected to this desktop"
    RelayStatus.Unavailable -> "The relay is unreachable. Your desktop is fine; try again shortly."
}
