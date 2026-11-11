package com.croutoncreations.redline

/** Last attempted registration, not merely the user's opt-in preference. */
data class PushRegistrationResult(val attemptedAt: Long, val error: String?)

internal fun pushSettingsStatus(pushEnabled: Boolean, enabled: Boolean, permission: Boolean, result: PushRegistrationResult?): String = when {
    !pushEnabled -> "Unavailable in this build"
    !enabled -> "Off"
    !permission -> "Permission needed: tap to allow"
    result == null -> "Couldn't register: registration pending"
    result.error != null -> "Couldn't register: ${result.error.lineSequence().joinToString(" ").take(90)}"
    else -> "On · this phone is registered with your Mac"
}
