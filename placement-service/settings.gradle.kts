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
rootProject.name = "placement-service"

// The placement code (tickets, server selection, placement policies) lives in placement-core,
// shared with agent-runtime; the service and agent-runtime resolve the same sources.
includeBuild("../placement-core")
