package com.croutoncreations.redline

import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.Dp
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.rules.RuleChain
import org.junit.rules.TestRule
import org.junit.rules.TestWatcher
import org.junit.runner.Description
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The app's own chrome must not sit under the system bars.
 *
 * Android 15 draws every app targeting SDK 35+ edge to edge, whatever the
 * theme says. After the targetSdk 36 bump the header slid under the status
 * bar -- the clock covered the logo, the tray covered the connection status
 * -- and the tab bar under the gesture handle. Nothing but a real window has
 * system bars, so this runs the real activity on a device.
 */
@RunWith(AndroidJUnit4::class)
class SystemBarsOnDeviceTest {

    private val compose = createAndroidComposeRule<MainActivity>()

    // The pairing screen asks for the camera as soon as it appears, and the
    // permission dialog pauses the activity before Compose can be inspected.
    // Granted before the activity launches, so the dialog never opens.
    @get:Rule val rules: TestRule = RuleChain
        .outerRule(object : TestWatcher() {
            override fun starting(description: Description) {
                val instrumentation = InstrumentationRegistry.getInstrumentation()
                instrumentation.uiAutomation.executeShellCommand(
                    "pm grant ${instrumentation.targetContext.packageName} android.permission.CAMERA",
                ).close()
            }
        })
        .around(compose)

    private var statusBarPx = 0
    private var navigationBarPx = 0
    private var windowHeightPx = 0

    @Before
    fun readSystemBars() {
        compose.waitForIdle()
        compose.runOnUiThread {
            val decor = compose.activity.window.decorView
            val insets = ViewCompat.getRootWindowInsets(decor)!!
                .getInsets(WindowInsetsCompat.Type.systemBars())
            statusBarPx = insets.top
            navigationBarPx = insets.bottom
            windowHeightPx = decor.height
        }
        // Without bars there is nothing to overlap; the test would pass vacuously.
        assumeTrue("device reports no status bar inset", statusBarPx > 0)
    }

    private fun Dp.px(): Float = value * compose.activity.resources.displayMetrics.density

    @Test
    fun rootContentIsInsetFromBothSystemBars() {
        // Every screen, the header and tab bar included, is drawn inside this
        // root, so its bounds are what the system bars must not overlap.
        val bounds = compose.onNodeWithTag(ROOT_CONTENT_TAG).getUnclippedBoundsInRoot()
        assertTrue(
            "content top ${bounds.top.px()}px is under the ${statusBarPx}px status bar",
            bounds.top.px() >= statusBarPx,
        )
        assertTrue(
            "content bottom ${bounds.bottom.px()}px is under the navigation bar " +
                "(window ${windowHeightPx}px, bar ${navigationBarPx}px)",
            bounds.bottom.px() <= windowHeightPx - navigationBarPx,
        )
    }
}
