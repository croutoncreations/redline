plugins {
    // 8.10+ is required to compile/target SDK 36; 8.10.1 is the newest patch
    // in the same minor line, and its minimum Gradle requirement (8.11.1) is
    // exactly the version already pinned in gradle-wrapper.properties, so no
    // Gradle bump is needed alongside this.
    id("com.android.application") version "8.10.1" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
    id("org.jetbrains.kotlin.plugin.serialization") version "1.9.24" apply false
}
