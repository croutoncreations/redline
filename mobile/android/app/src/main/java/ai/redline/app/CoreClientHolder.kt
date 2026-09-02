package ai.redline.app

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

    private var cached: Client? = null
    private var credentials: Pair<String, String>? = null

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
        cached = created
        credentials = current
        return created
    }

    /** Reports whether an error means the credential was rejected. */
    fun isUnauthorized(error: Throwable): Boolean {
        // gomobile surfaces Go errors as Exception; anything else came from the
        // Kotlin side and cannot be an auth failure.
        val exception = error as? Exception ?: return false
        return runCatching { Core.isUnauthorized(exception) }.getOrDefault(false)
    }
}
