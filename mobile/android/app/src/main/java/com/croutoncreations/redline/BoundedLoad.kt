package com.croutoncreations.redline

import java.io.IOException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.withTimeout

/**
 * The longest one core call may take before the screen says so.
 *
 * Longer than the slowest healthy path for a single call: the core's 20s
 * direct attempt against a blackholed tailnet address, a relay dial that
 * needs one retry (two 3s handshake waits, 0.4s apart), then a relayed request
 * bounded by the core at 30s. A slow but working fallback is never cut off.
 * Bound each core call separately; a screen that makes two calls must not
 * share one budget between them.
 */
internal const val LOAD_TIMEOUT_MILLIS = 60_000L

/** A core call overran [LOAD_TIMEOUT_MILLIS]; reported as unreachable. */
internal class LoadTimedOut : IOException("load timed out")

/**
 * Runs a blocking core call on [dispatcher], giving up after [timeoutMillis].
 *
 * The core's calls block in Go and cannot be cancelled from Kotlin. Inside
 * withContext the caller waits for the block regardless of any timeout, since
 * withContext joins it -- that is how a stuck relay became an endless spinner
 * on a blank screen. Running the call in its own scope and awaiting with a
 * deadline lets the screen move on; a call that finishes late is dropped.
 *
 * The detached scope is deliberate and bounded: every core path now has its
 * own timeout, so the abandoned call ends on its own; nothing reads its
 * result once the deadline has passed.
 */
internal suspend fun <T> boundedLoad(
    dispatcher: CoroutineDispatcher,
    timeoutMillis: Long = LOAD_TIMEOUT_MILLIS,
    block: () -> T,
): T {
    val pending = CoroutineScope(SupervisorJob() + dispatcher).async { block() }
    return try {
        withTimeout(timeoutMillis) { pending.await() }
    } catch (timedOut: TimeoutCancellationException) {
        pending.cancel()
        throw LoadTimedOut()
    }
}
