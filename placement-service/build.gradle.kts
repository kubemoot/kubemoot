plugins {
    java
    jacoco
    checkstyle
    id("io.quarkus")
}

group = "ai.kubemoot"
version = "0.1.0-SNAPSHOT"
description = "Kubemoot placement service - the single writer of model-server tickets and the place where calls are placed"

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(25)
    }
}

// The repository's shared complexity ruleset (config/checkstyle/checkstyle.xml) runs
// over main and test sources as part of `check`; any finding fails the build.
checkstyle {
    toolVersion = "14.3.0"
    configFile = rootDir.resolve("../config/checkstyle/checkstyle.xml")
    maxWarnings = 0
    isIgnoreFailures = false
}

repositories {
    mavenCentral()
}

val quarkusPlatformGroupId = "io.quarkus.platform"
val quarkusPlatformArtifactId = "quarkus-bom"
val quarkusPlatformVersion: String by project

dependencies {
    implementation(enforcedPlatform("${quarkusPlatformGroupId}:${quarkusPlatformArtifactId}:${quarkusPlatformVersion}"))

    // Quarkus core, REST and JSON (the container supplies the ObjectMapper the placement code uses)
    implementation("io.quarkus:quarkus-arc")
    implementation("io.quarkus:quarkus-rest")
    implementation("io.quarkus:quarkus-rest-jackson")

    // Placement: tickets, server selection and placement policies, shared with agent-runtime.
    // Its CDI beans are indexed through quarkus.index-dependency in application.properties.
    implementation("ai.kubemoot:placement-core")
    implementation("io.nats:jnats:2.26.4")

    // Observability
    implementation("io.quarkus:quarkus-micrometer-registry-prometheus")
    implementation("io.quarkus:quarkus-smallrye-health")

    testImplementation(testFixtures("ai.kubemoot:placement-core"))
    testImplementation("io.quarkus:quarkus-junit5")
    testImplementation("io.rest-assured:rest-assured")
    testImplementation("org.mockito:mockito-core")
}

tasks.withType<Test> {
    useJUnitPlatform()
    systemProperty("java.util.logging.manager", "org.jboss.logmanager.LogManager")
}

tasks.jacocoTestReport {
    reports {
        xml.required.set(true)
    }
}

tasks.test {
    finalizedBy(tasks.jacocoTestReport)
}

tasks.withType<JavaCompile> {
    options.encoding = "UTF-8"
    options.compilerArgs.add("-parameters")
}
