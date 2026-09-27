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
