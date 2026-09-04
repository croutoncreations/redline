plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.serialization")
}

android {
    namespace = "ai.redline.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "ai.redline.app"
        // Matches the -androidapi passed to gomobile in
        // scripts/build-mobile-core.sh.
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.14"
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
        jniLibs {
            // Current devices such as the Pixel 9 use 16KB memory pages, and
            // Android warns on every launch when a bundled library is packed
            // for 4KB. Storing the libraries uncompressed and page-aligned is
            // what makes the alignment survive packaging; linking them for
            // 16KB is necessary but not sufficient on its own.
            useLegacyPackaging = false
        }
    }
}

dependencies {
    // The shared Go core, built by scripts/build-mobile-core.sh.
    implementation(files("libs/redlinecore.aar"))

    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.4")
    implementation("androidx.activity:activity-compose:1.9.1")
    implementation(platform("androidx.compose:compose-bom:2024.09.02"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")

    // Keystore-backed storage for the API token, which grants full access.
    implementation("androidx.security:security-crypto:1.1.0-alpha06")

    // Camera and barcode scanning for QR pairing.
    //
    // CameraX 1.4+ is required for 16KB memory pages: 1.3.x shipped a
    // libimage_processing_util_jni.so linked for 4KB, which current devices
    // such as the Pixel 9 reject as incompatible.
    implementation("androidx.camera:camera-camera2:1.4.2")
    implementation("androidx.camera:camera-lifecycle:1.4.2")
    implementation("androidx.camera:camera-view:1.4.2")

    // The Play Services variant rather than the bundled one. Both recognise
    // barcodes entirely on the device -- a pairing credential is never sent to
    // a cloud recogniser either way -- but this variant keeps the model in
    // Play Services instead of bundling libbarhopper_v3.so, which is not 16KB
    // aligned and is not something this project can rebuild. It also drops
    // several megabytes from the APK.
    implementation("com.google.android.gms:play-services-mlkit-barcode-scanning:18.3.1")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.8.1")

    // On-device tests: the QR decoder and the Keystore both need a real device.
    androidTestImplementation(platform("androidx.compose:compose-bom:2024.09.02"))
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    // Renders composables on device so a screen state can be asserted and
    // photographed without needing a paired desktop behind it.
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    // Provides the empty activity the Compose test rule launches into.
    debugImplementation("androidx.compose.ui:ui-test-manifest")
    androidTestImplementation("androidx.test:runner:1.6.1")

    debugImplementation("androidx.compose.ui:ui-tooling")
}

/**
 * Fails the build when the bundled Go core is older than its source.
 *
 * The app links a compiled AAR, not the Go source, so editing mobile/core and
 * forgetting to rebuild produces an app that reads fields the native library
 * never emits. That has happened: `banked_resets` was added, every test on both
 * sides passed, and the row was missing on a real phone until the built .so was
 * grepped and found not to contain the string at all.
 *
 * Nothing else catches it. The AAR is gitignored, so a fresh checkout has none
 * and CI never builds this module, which leaves the mistake purely local and
 * silent.
 */
val checkCoreFreshness by tasks.registering {
    group = "verification"
    description = "Fails if mobile/core is newer than the bundled redlinecore.aar."

    val aar = layout.projectDirectory.file("libs/redlinecore.aar").asFile
    val coreDir = rootProject.layout.projectDirectory.dir("../../mobile/core").asFile
    val buildScript = "scripts/build-mobile-core.sh"

    // Declared so Gradle can skip the task when neither side has changed.
    //
    // The AAR is registered as an optional file collection rather than
    // inputs.file: a missing AAR is the case this task exists to explain, and
    // inputs.file rejects it during configuration with a plumbing error before
    // doLast can say anything useful.
    inputs.dir(coreDir).withPropertyName("goCore").withPathSensitivity(PathSensitivity.RELATIVE)
    inputs.files(project.files(aar)).withPropertyName("coreAar")
        .withPathSensitivity(PathSensitivity.RELATIVE)
        .optional()

    doLast {
        if (!aar.exists()) {
            throw GradleException(
                "The Go core has not been built.\n" +
                    "  Run: ./$buildScript android && cp mobile/build/redlinecore.aar mobile/android/app/libs/"
            )
        }

        // Only .go files matter: a change to a test fixture or a README beside
        // the core does not alter the compiled library.
        val newest = coreDir.walkTopDown()
            .filter { it.isFile && it.extension == "go" && !it.name.endsWith("_test.go") }
            .maxByOrNull { it.lastModified() }
            ?: return@doLast

        if (newest.lastModified() > aar.lastModified()) {
            throw GradleException(
                "The bundled Go core is stale: ${newest.name} is newer than redlinecore.aar.\n" +
                    "  The app would run against the previous native library, so a field added to\n" +
                    "  mobile/core would be missing at runtime with every test still passing.\n" +
                    "  Run: ./$buildScript android && cp mobile/build/redlinecore.aar mobile/android/app/libs/"
            )
        }
    }
}

// Runs before compilation, so the failure arrives before a stale build is
// installed rather than after someone notices a missing value on a phone.
tasks.named("preBuild") { dependsOn(checkCoreFreshness) }
