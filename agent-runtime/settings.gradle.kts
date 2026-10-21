pluginManagement {
    val quarkusPlatformVersion: String by settings
    repositories {
        mavenCentral()
        gradlePluginPortal()
    }
    plugins {
        id("io.quarkus") version quarkusPlatformVersion
    }
}
rootProject.name = "agent-runtime"

// The placement code (tickets, server selection, placement policies) lives in placement-core,
// shared with the placement service; agent-runtime and the service resolve the same sources.
includeBuild("../placement-core")
