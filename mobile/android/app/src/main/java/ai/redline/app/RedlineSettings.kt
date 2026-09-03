package ai.redline.app

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

/**
 * Where the app finds the Redline service, and the credential it uses.
 *
 * The credential is a bearer token with full API access, so it is held in
 * [EncryptedSharedPreferences], keyed from the Android Keystore. The Keystore
 * key is hardware-backed where the device offers it and never leaves the
 * secure element, so the stored file is useless on its own — which matters
 * because a plain preferences file is readable on a rooted or backed-up device.
 *
 * Storage falls back to plain preferences only if the encrypted store cannot be
 * opened at all. That is rare, and being unable to run beats being unable to
 * run *securely* only when the alternative is losing the app entirely; the
 * fallback is recorded in [usingEncryptedStorage] so the UI can say so rather
 * than quietly downgrading.
 */
/**
 * The part of settings that pairing needs.
 *
 * Narrowed to one method so the pairing flow can be tested on the JVM: the real
 * implementation needs a Context and the Android Keystore, neither of which
 * exists in a unit test.
 */
interface RedlineSettingsWriter {
    fun update(baseUrl: String, token: String)

    /**
     * Records how to reach this desktop when it is not directly reachable.
     *
     * Kept separate from update() because an older desktop supplies neither,
     * and pairing must still work without them: no relay simply means direct
     * only, which is what every existing paired phone already does.
     */
    fun updateRelay(relayUrl: String, desktopKey: String, relaySession: String)
}

class RedlineSettings(context: Context) : RedlineSettingsWriter {

    private var encrypted = true

    private val preferences: SharedPreferences = try {
        val key = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        EncryptedSharedPreferences.create(
            context,
            ENCRYPTED_FILE,
            key,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    } catch (error: Exception) {
        // A corrupt keystore entry or an unsupported device should not brick
        // the app, but it must not pretend the credential is protected either.
        encrypted = false
        context.getSharedPreferences(PLAIN_FILE, Context.MODE_PRIVATE)
    }

    /** Whether the credential is actually held in Keystore-backed storage. */
    val usingEncryptedStorage: Boolean get() = encrypted

    val baseUrl: String
        get() = preferences.getString(KEY_BASE_URL, null)?.takeIf { it.isNotBlank() }
            ?: DEFAULT_BASE_URL

    val token: String
        get() = preferences.getString(KEY_TOKEN, null) ?: ""

    /** Where to reach the relay, or "" when this desktop published none. */
    val relayUrl: String
        get() = preferences.getString(KEY_RELAY_URL, null) ?: ""

    /**
     * The desktop's Noise static public key.
     *
     * Without it there is nothing to authenticate the far end of a relayed
     * session against, so a relay connection must not be attempted.
     */
    val desktopKey: String
        get() = preferences.getString(KEY_DESKTOP_KEY, null) ?: ""

    /** This desktop's session on the relay, or "" when it published none. */
    val relaySession: String
        get() = preferences.getString(KEY_RELAY_SESSION, null) ?: ""

    /**
     * Authorises use of the relay. Empty while the relay is free to use; the
     * relay says nothing about who the token belongs to, only that it is
     * signed and unexpired.
     */
    val entitlementToken: String
        get() = preferences.getString(KEY_ENTITLEMENT, null) ?: ""

    /** Whether a relayed fallback is possible at all. */
    val relayConfigured: Boolean
        get() = relayUrl.isNotBlank() && desktopKey.isNotBlank() && relaySession.isNotBlank()

    override fun updateRelay(relayUrl: String, desktopKey: String, relaySession: String) {
        preferences.edit()
            .putString(KEY_RELAY_URL, relayUrl)
            .putString(KEY_DESKTOP_KEY, desktopKey)
            .putString(KEY_RELAY_SESSION, relaySession)
            .apply()
    }

    /** Whether this device has been paired with a Redline desktop. */
    val isPaired: Boolean get() = token.isNotBlank()

    override fun update(baseUrl: String, token: String) {
        preferences.edit()
            .putString(KEY_BASE_URL, baseUrl)
            .putString(KEY_TOKEN, token)
            .apply()
    }

    /**
     * Forgets the credential.
     *
     * Used when the desktop rejects it: keeping a credential the service has
     * already refused only produces the same failure on every launch, and
     * clearing it returns the app to the pairing screen where the fix is.
     */
    fun clear() {
        preferences.edit()
            .remove(KEY_TOKEN)
            .remove(KEY_BASE_URL)
            .remove(KEY_RELAY_URL)
            .remove(KEY_DESKTOP_KEY)
            .remove(KEY_RELAY_SESSION)
            .remove(KEY_ENTITLEMENT)
            .apply()
    }

    private companion object {
        const val KEY_BASE_URL = "base_url"
        const val KEY_TOKEN = "token"
        const val KEY_RELAY_URL = "relay_url"
        const val KEY_DESKTOP_KEY = "desktop_key"
        const val KEY_RELAY_SESSION = "relay_session"
        const val KEY_ENTITLEMENT = "entitlement_token"

        const val ENCRYPTED_FILE = "redline.secure"
        const val PLAIN_FILE = "redline"

        // Pairing supplies the real address. This default only matters for a
        // debug build reached through `adb reverse tcp:7436 tcp:7436`.
        const val DEFAULT_BASE_URL = "http://127.0.0.1:7436"
    }
}
