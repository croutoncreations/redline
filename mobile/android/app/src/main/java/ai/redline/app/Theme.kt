package ai.redline.app

import androidx.compose.ui.graphics.Color

/**
 * The palette, shared by every screen.
 *
 * Colours live here rather than per-file so the runs screen and the usage
 * screen cannot drift into two slightly different dark themes.
 */
// Taken from the web dashboard's dark theme (internal/api/dashboard/mobile.css)
// so the two surfaces look like the same product rather than two apps that
// happen to share a name.
internal val Background = Color(0xFF0D0F12)
internal val Panel = Color(0xFF15191D)
internal val PanelRaised = Color(0xFF1A1F24)
internal val Line = Color(0xFF2A3036)
internal val TextPrimary = Color(0xFFF2F0ED)
internal val TextMuted = Color(0xFF91989E)
internal val Accent = Color(0xFFF0524D)
internal val AccentSoft = Color(0xFF3B2021)
internal val Good = Color(0xFF72D9A4)
internal val Warn = Color(0xFFEFBD62)
internal val Danger = Color(0xFFF0524D)

/** Matches the web dashboard's thresholds so both surfaces agree. */
internal fun toneFor(percent: Int): Color = when {
    percent < 15 -> Danger
    percent < 35 -> Warn
    else -> Good
}
