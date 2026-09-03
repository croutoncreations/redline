package ai.redline.app

/*
 * Transport policy for reaching the desktop.
 *
 * STATUS: the decisions in this file are settled and tested, but the phone
 * cannot yet dial the relay -- mobile/core has the Noise session and the
 * pairing parser, and no WebSocket client. Until that exists, chooseTransport
 * is only ever called with directReachable = true, and the "relayed" marker in
 * UsageScreen cannot appear.
 *
 * This is deliberate rather than forgotten. The desktop leg, the relay, and the
 * pairing hand-off are each proven end to end; the phone's dialer is the one
 * remaining piece, and the policy it will need is easier to get right in
 * isolation than tangled into a ViewModel.
 */

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

/**
 * The chosen transport and the rules that follow from it.
 *
 * Bundled together because they are one decision: choosing the relay also means
 * accepting a slower cadence and an idle timeout, and letting those drift apart
 * is how a relayed session quietly ends up polling at the direct rate.
 */
data class TransportPlan(
    val transport: Transport,
    val liveCadenceSeconds: Int,
    val dropWhenIdle: Boolean,
) {
    /** Whether the caller should attempt a relay connection. */
    val shouldTryRelay: Boolean get() = transport == Transport.Relay
}

/**
 * Decides how to reach the desktop.
 *
 * [previous] is accepted but deliberately not used to make the choice: direct
 * is retried every time, so walking back onto the home network stops costing
 * money without needing the app restarted. It is part of the signature because
 * callers naturally have it and would otherwise be tempted to add their own
 * stickiness.
 */
fun planTransport(
    directReachable: Boolean,
    relayConfigured: Boolean,
    previous: Transport = Transport.Direct,
): TransportPlan {
    val transport = chooseTransport(directReachable, relayConfigured)
    return TransportPlan(
        transport = transport,
        liveCadenceSeconds = liveCadenceSeconds(transport),
        dropWhenIdle = shouldDropWhenIdle(transport),
    )
}
