package ai.redline.app

import android.content.Context

/**
 * Where the app finds the Redline service.
 *
 * Phase 1 is direct-connection only, so this reads a base URL and bearer token
 * from shared preferences with defaults suited to a development emulator.
 * Phase 3 replaces this with QR pairing and keys held in the Android Keystore;
 * the token stored here is deliberately temporary.
 */
class RedlineSettings(context: Context) {

    private val preferences =
        context.getSharedPreferences("redline", Context.MODE_PRIVATE)

    val baseUrl: String
        get() = preferences.getString(KEY_BASE_URL, null) ?: DEFAULT_BASE_URL

    val token: String
        get() = preferences.getString(KEY_TOKEN, null) ?: ""

    fun update(baseUrl: String, token: String) {
        preferences.edit()
            .putString(KEY_BASE_URL, baseUrl)
            .putString(KEY_TOKEN, token)
            .apply()
    }

    private companion object {
        const val KEY_BASE_URL = "base_url"
        const val KEY_TOKEN = "token"

        // `adb reverse tcp:7436 tcp:7436` maps this to the developer machine's
        // Redline instance, so a debug build works without configuration.
        const val DEFAULT_BASE_URL = "http://127.0.0.1:7436"
    }
}
