package ai.redline.app

import android.util.Log
import core.Client
import core.Core

/**
 * Owns the one gomobile client the app talks through.
 *
 * Every screen shares this rather than constructing its own, so re-pairing
 * takes effect everywhere at once instead of leaving one screen authenticated
 * and another not.
 *
 * This and the two source classes are the only files that touch the binding,
 * which keeps everything above them testable on the JVM: the native library
 * needs a real device to load.
 *
 * Networking note: the core uses Go's own net/http, which opens sockets
 * directly and is therefore not subject to Android's NetworkSecurityPolicy.
 * That is why plain-HTTP requests to a local Redline succeed even though the
 * app targets SDK 34, where cleartext is otherwise blocked by default. It also
 * means a network security config would not constrain this path: a Kotlin-side
 * HTTP client would need its own allowance. Phase 3 moves this traffic inside
 * an encrypted session, which removes the question.
 */
class CoreClientHolder(private val settings: RedlineSettings) {

    private companion object {
        const val TAG = "RedlineRelay"
    }

    private var cached: Client? = null
    private var credentials: Pair<String, String>? = null
    private var relay: core.RelayClient? = null

    /**
     * Returns a client for the current credentials, rebuilding it if they have
     * changed.
     *
     * Building it once at construction would pin the app to whatever token was
     * present at launch, so re-pairing would not take effect until the process
     * died. That matters more once phase 3 makes re-pairing routine.
     */
    @Synchronized
    fun client(): Client {
        val current = settings.baseUrl to settings.token
        val existing = cached
        if (existing != null && credentials == current) {
            return existing
        }
        val created = Core.newClient(current.first, current.second)
        // Installed on every client, so fallback cannot be present on one
        // screen and missing on another. The core calls this only after a
        // direct transport failure; an HTTP error means the desktop answered
        // and the relay would only reach the same desktop more slowly.
        created.setRelayFallback(RelayFallback())
        cached = created
        credentials = current
        return created
    }

    /**
     * Carries a request over the relay on the core's behalf.
     *
     * Returning an error rather than null when nothing is paired keeps the
     * core's direct failure as the one the user sees: someone who never set up
     * a relay should not be sent looking for a relay fault.
     */
    private inner class RelayFallback : core.RelayFallback {
        override fun do_(method: String, path: String, body: String): String {
            val relay = relayClient()
            if (relay == null) {
                Log.w(TAG, "relay fallback for $method $path: nothing to dial (configured=${settings.relayConfigured})")
                throw IllegalStateException("no relay is paired")
            }
            return try {
                // answer(), not request(): a 409 or a 401 is a real reply from
                // the desktop rather than a relay failure, and the status has
                // to survive or a refused dispatch reads as an accepted one.
                //
                // The status is encoded into the returned string by the core.
                // Holding it in a field here and reading it back separately is
                // what raced: one holder serves three view models, each
                // refreshing on its own thread.
                relay.answer(method, path, body).also {
                    Log.i(TAG, "relay carried $method $path: ${it.take(3)} ${it.length} bytes")
                }
            } catch (error: Exception) {
                Log.w(TAG, "relay request $method $path failed: ${error.message}")
                // Only a genuine transport or crypto failure reaches here.
                // Noise sessions do not resume: once a frame fails, every later
                // frame on that session fails too, so discard it and let the
                // next attempt dial afresh.
                dropRelay()
                throw error
            }
        }
    }

    /**
     * Reaches the desktop through the relay after a direct attempt failed.
     *
     * Returns null when there is nothing to fall back to, which the caller must
     * report as plain unreachability rather than a relay fault: someone who
     * never set up a relay should not be sent looking for one.
     *
     * A relayed session is single-use by the core's contract, so a failure here
     * discards it rather than retrying on the same session; the next attempt
     * dials afresh.
     */
    @Synchronized
    fun relayClient(): core.RelayClient? {
        if (!settings.relayConfigured) return null

        // A cached session is only worth reusing while it can still carry a
        // request. Noise sessions do not resume, so a spent one fails every
        // time -- which on a real phone read as "relayed", then "offline" on
        // the next refresh, with re-pairing unable to help because the cache
        // outlived the session.
        relay?.let { existing ->
            if (!existing.isSpent) return existing
            runCatching { existing.close() }
            relay = null
        }
        return try {
            Core.dialRelay(
                settings.relayUrl,
                settings.relaySession,
                settings.desktopKey,
                settings.entitlementToken,
            ).also {
                it.setAuthToken(settings.token)
                relay = it
                Log.i(TAG, "relay session established via ${settings.relayUrl}")
            }
        } catch (error: Exception) {
            // The dial reason must reach both the log and the caller. Swallowing
            // it here made an expired entitlement indistinguishable from an
            // unconfigured relay, so the renewal UI could never be reached.
            Log.w(TAG, "relay dial to ${settings.relayUrl} failed: ${error.message}")
            throw error
        }
    }

    /**
     * Discards a relayed session after a failure.
     *
     * Noise sessions do not resume: once a frame fails to decrypt, every later
     * frame on that session fails too, so keeping it would turn one bad frame
     * into a permanently broken app.
     */
    @Synchronized
    fun dropRelay() {
        runCatching { relay?.close() }
        relay = null
    }

    /**
     * Reports whether the relay declined for lack of a current subscription.
     *
     * Distinct from unreachability: the desktop may be healthy and the network
     * fine, and the remedy is to renew rather than to check the network.
     */
    fun isEntitlementRefused(error: Throwable): Boolean {
        val exception = error as? Exception ?: return false
        return runCatching { Core.isEntitlementRefused(exception) }.getOrDefault(false)
    }

    /** Reports whether an error means the credential was rejected. */
    fun isUnauthorized(error: Throwable): Boolean {
        // gomobile surfaces Go errors as Exception; anything else came from the
        // Kotlin side and cannot be an auth failure.
        val exception = error as? Exception ?: return false
        return runCatching { Core.isUnauthorized(exception) }.getOrDefault(false)
    }
}
