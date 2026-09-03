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
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test:runner:1.6.1")

    debugImplementation("androidx.compose.ui:ui-tooling")
}
