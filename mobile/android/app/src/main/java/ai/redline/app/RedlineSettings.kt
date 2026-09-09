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
 * If encrypted storage cannot open, credential storage fails closed. A bearer
 * token with full API access must never silently move to plaintext because of
 * a transient Keystore fault.
 */
/**
 * The part of settings that pairing needs.
 *
 * Narrowed to one method so the pairing flow can be tested on the JVM: the real
 * implementation needs a Context and the Android Keystore, neither of which
 * exists in a unit test.
 */
data class PairingConfiguration(
    val baseUrl: String,
    val token: String,
    val relayUrl: String,
    val desktopKey: String,
    val relaySession: String,
    val entitlementToken: String,
)

interface RedlineSettingsWriter {
    fun update(baseUrl: String, token: String)

    /** Stores credential and routes as one pairing transaction. */
    fun updatePairing(configuration: PairingConfiguration) {
        update(configuration.baseUrl, configuration.token)
        updateRelay(
            configuration.relayUrl,
            configuration.desktopKey,
            configuration.relaySession,
            configuration.entitlementToken,
        )
    }

    /**
     * Forgets everything about the paired desktop.
     *
     * Part of the interface so unpairing can be tested without a device. The
     * risk being guarded against is a partial clear, which leaves a phone that
     * looks unpaired but is still carrying one desktop's relay details.
     */
    fun clear()

    /**
     * Records how to reach this desktop when it is not directly reachable.
     *
     * Kept separate from update() because an older desktop supplies neither,
     * and pairing must still work without them: no relay simply means direct
     * only, which is what every existing paired phone already does.
     */
    fun updateRelay(
        relayUrl: String,
        desktopKey: String,
        relaySession: String,
        entitlementToken: String,
    )
}

/**
 * Alias used by tests that only care about storing and clearing a pairing.
 *
 * Named for the role rather than the implementation, so a test reads as being
 * about the pairing store rather than about Android preferences.
 */
typealias PairingStore = RedlineSettingsWriter

class RedlineSettings(private val context: Context) : RedlineSettingsWriter {

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
        ).also {
            // Versions before secure storage became fail-closed could leave a
            // bearer token in this file after a transient Keystore failure.
            context.deleteSharedPreferences(PLAIN_FILE)
        }
    } catch (error: Exception) {
        context.deleteSharedPreferences(PLAIN_FILE)
        throw IllegalStateException("secure credential storage is unavailable", error)
    }

    /** Credential storage is always Keystore-backed or construction fails. */
    val usingEncryptedStorage: Boolean get() = true

    val baseUrl: String
        get() = resolvedBaseUrl(
            hasStoredValue = preferences.contains(KEY_BASE_URL),
            storedValue = preferences.getString(KEY_BASE_URL, null),
        )

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
        get() = relayConfigurationComplete(relayUrl, desktopKey, relaySession)

    override fun updateRelay(
        relayUrl: String,
        desktopKey: String,
        relaySession: String,
        entitlementToken: String,
    ) {
        preferences.edit()
            .putString(KEY_RELAY_URL, relayUrl)
            .putString(KEY_ENTITLEMENT, entitlementToken)
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

    override fun updatePairing(configuration: PairingConfiguration) {
        preferences.edit()
            .putString(KEY_BASE_URL, configuration.baseUrl)
            .putString(KEY_TOKEN, configuration.token)
            .putString(KEY_RELAY_URL, configuration.relayUrl)
            .putString(KEY_ENTITLEMENT, configuration.entitlementToken)
            .putString(KEY_DESKTOP_KEY, configuration.desktopKey)
            .putString(KEY_RELAY_SESSION, configuration.relaySession)
            .apply()
    }

    /**
     * Forgets the credential.
     *
     * Used when the desktop rejects it: keeping a credential the service has
     * already refused only produces the same failure on every launch, and
     * clearing it returns the app to the pairing screen where the fix is.
     */
    override fun clear() {
        preferences.edit()
            .remove(KEY_TOKEN)
            .remove(KEY_BASE_URL)
            .remove(KEY_RELAY_URL)
            .remove(KEY_DESKTOP_KEY)
            .remove(KEY_RELAY_SESSION)
            .remove(KEY_ENTITLEMENT)
            .apply()
        // Also remove credentials written by pre-fail-closed versions.
        context.deleteSharedPreferences(PLAIN_FILE)
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
    }
}

// Pairing supplies the real address. Localhost is only the unconfigured debug
// default; an explicitly stored empty string means relay-only and stays empty.
internal fun resolvedBaseUrl(hasStoredValue: Boolean, storedValue: String?): String =
    if (hasStoredValue) storedValue.orEmpty() else "http://127.0.0.1:7436"

internal fun relayConfigurationComplete(relayUrl: String, desktopKey: String, relaySession: String): Boolean =
    relayUrl.isNotBlank() && desktopKey.isNotBlank() && relaySession.isNotBlank()
