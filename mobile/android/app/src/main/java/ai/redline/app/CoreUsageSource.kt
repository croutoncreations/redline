package ai.redline.app

/**
 * [UsageSource] backed by the gomobile-bound Go core.
 *
 * Holds no client of its own: [CoreClientHolder] owns the single shared one so
 * every screen authenticates the same way.
 */
class CoreUsageSource(private val client: CoreClientHolder) : UsageSource {

    override fun fetchUsageJson(): String = client.client().fetchUsage()

    override fun controlProvider(providerAccountId: String, control: String) {
        client.client().controlProvider(providerAccountId, control)
    }

    override fun isUnauthorized(error: Throwable): Boolean = client.isUnauthorized(error)
}
