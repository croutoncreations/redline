package com.croutoncreations.redline

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeoutOrNull
import org.junit.Assert.*
import org.junit.Test

class PushClientTest {
    @Test fun lateNativeResultCannotPostOrAdvanceAfterCancellation() = runBlocking {
        val started = CompletableDeferred<Unit>()
        val release = CountDownLatch(1)
        val finished = CountDownLatch(1)
        val cursor = PushCursor(10)
        var posted = false
        val wake = async {
            corePushCall {
                started.complete(Unit)
                try {
                    check(release.await(5, TimeUnit.SECONDS))
                    11L
                } finally {
                    finished.countDown()
                }
            }.also {
                posted = true
                cursor.advance(it)
            }
        }
        try {
            withTimeoutOrNull(2_000) { started.await() } ?: error("native fake did not start")
            wake.cancel()
            // Joining must not wait for the blocking native operation.
            assertNotNull(withTimeoutOrNull(1_000) { wake.join(); true })
        } finally {
            release.countDown()
        }
        assertTrue(finished.await(2, TimeUnit.SECONDS))
        assertFalse(posted)
        assertEquals(10L, cursor.lastSeenId)
    }

    @Test fun nativeErrorsReachTheCaller() = runBlocking {
        try {
            corePushCall<Unit> { error("desktop refused") }
            fail("native error was swallowed")
        } catch (error: IllegalStateException) {
            assertEquals("desktop refused", error.message)
        }
    }
}
