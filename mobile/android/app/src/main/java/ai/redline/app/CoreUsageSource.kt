package ai.redline.app

import core.Client
import core.Core

/**
 * [UsageSource] backed by the gomobile-bound Go core.
 *
 * This is the only file that touches the binding, so everything above it stays
 * testable on the JVM without the native library.
 */
class CoreUsageSource(baseUrl: String, token: String) : UsageSource {

    private val client: Client = Core.newClient(baseUrl, token)

    override fun fetchUsageJson(): String = client.fetchUsage()

    override fun isUnauthorized(error: Throwable): Boolean {
        // gomobile surfaces Go errors as Exception; anything else came from the
        // Kotlin side and cannot be an auth failure.
        val exception = error as? Exception ?: return false
        return runCatching { Core.isUnauthorized(exception) }.getOrDefault(false)
    }
}
