pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
        // The gomobile-built core is a local artifact, not published.
        flatDir { dirs("app/libs") }
    }
}

rootProject.name = "Redline"
include(":app")
