package ai.redline.app

import androidx.compose.ui.graphics.Color

/**
 * The palette, shared by every screen.
 *
 * Colours live here rather than per-file so the runs screen and the usage
 * screen cannot drift into two slightly different dark themes.
 */
internal val Background = Color(0xFF0D0F12)
internal val Panel = Color(0xFF15181D)
internal val Line = Color(0xFF272C34)
internal val TextPrimary = Color(0xFFE6E9EE)
internal val TextMuted = Color(0xFF91989E)
internal val Accent = Color(0xFFFF5A52)
internal val Good = Color(0xFF72D9A4)
internal val Warn = Color(0xFFEFBD62)
internal val Danger = Color(0xFFFF7B72)

/** Matches the web dashboard's thresholds so both surfaces agree. */
internal fun toneFor(percent: Int): Color = when {
    percent < 15 -> Danger
    percent < 35 -> Warn
    else -> Good
}
