package ai.redline.app

/**
 * [UsageSource] backed by the gomobile-bound Go core.
 *
 * Holds no client of its own: [CoreClientHolder] owns the single shared one so
 * every screen authenticates the same way.
 */
class CoreUsageSource(private val client: CoreClientHolder) : UsageSource {

    // Relay fallback lives inside the core's client, below every one of these
    // methods, so nothing here has to know which route carried the request.
    override fun fetchUsageJson(): String = client.client().fetchUsage()

    override fun controlProvider(providerAccountId: String, control: String) {
        client.client().controlProvider(providerAccountId, control)
    }

    override fun stream(
        onUsage: (String) -> Unit,
        onState: (String) -> Unit,
    ): AutoCloseable {
        val stream = client.client().streamUsage(object : core.UsageStreamSink {
            override fun onUsage(payload: String) = onUsage(payload)
            override fun onState(state: String) = onState(state)
        })
        return AutoCloseable { stream.stop() }
    }

    override fun isUnauthorized(error: Throwable): Boolean = client.isUnauthorized(error)
}
