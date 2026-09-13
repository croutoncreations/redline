package ai.redline.app

/** [QueueSource] backed by the gomobile-bound Go core. */
class CoreQueueSource(private val client: CoreClientHolder) : QueueSource {

    override fun fetchQueueJson(providerAccountId: String): String =
        client.client().fetchQueue(providerAccountId)

    override fun controlProvider(providerAccountId: String, control2: String) {
        client.client().controlProvider(providerAccountId, control2)
    }

    override fun isUnauthorized(error: Throwable): Boolean = client.isUnauthorized(error)
}
