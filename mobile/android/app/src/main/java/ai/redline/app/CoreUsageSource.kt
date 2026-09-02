package ai.redline.app

import core.Client
import core.Core

/**
 * [UsageSource] backed by the gomobile-bound Go core.
 *
 * This is the only file that touches the binding, so everything above it stays
 * testable on the JVM without the native library.
 *
 * Networking note: the core uses Go's own net/http, which opens sockets
 * directly and is therefore not subject to Android's NetworkSecurityPolicy.
 * That is why plain-HTTP requests to a local Redline succeed even though the
 * app targets SDK 34, where cleartext is otherwise blocked by default. It also
 * means adding a network security config would not constrain this path: if a
 * Kotlin-side HTTP client is ever introduced, it will need its own allowance.
 * Phase 3 moves this traffic inside an encrypted session, which removes the
 * question.
 */
class CoreUsageSource(private val settings: RedlineSettings) : UsageSource {

    private var client: Client? = null
    private var credentials: Pair<String, String>? = null

    /**
     * Rebuilds the client when the stored credentials change.
     *
     * Building it once at construction would pin the app to whatever token was
     * present at launch, so re-pairing would not take effect until the process
     * died. That matters more once phase 3 makes re-pairing routine.
     */
    @Synchronized
    private fun client(): Client {
        val current = settings.baseUrl to settings.token
        val existing = client
        if (existing != null && credentials == current) {
            return existing
        }
        val created = Core.newClient(current.first, current.second)
        client = created
        credentials = current
        return created
    }

    override fun fetchUsageJson(): String = client().fetchUsage()

    override fun isUnauthorized(error: Throwable): Boolean {
        // gomobile surfaces Go errors as Exception; anything else came from the
        // Kotlin side and cannot be an auth failure.
        val exception = error as? Exception ?: return false
        return runCatching { Core.isUnauthorized(exception) }.getOrDefault(false)
    }
}
