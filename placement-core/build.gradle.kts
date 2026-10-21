plugins {
    `java-library`
    `java-test-fixtures`
    jacoco
    checkstyle
}

group = "ai.kubemoot"
version = "0.1.0-SNAPSHOT"
description = "Kubemoot placement core - tickets, server selection and placement policies shared by agent-runtime and the placement service"

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(25)
    }
}

// The repository's shared complexity ruleset runs over main, test and fixture sources as part
// of `check`; any finding fails the build.
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
    // The Quarkus platform pins every version below, so the library and the two applications
    // that use it resolve the same ones.
    api(platform("${quarkusPlatformGroupId}:${quarkusPlatformArtifactId}:${quarkusPlatformVersion}"))
    testFixturesApi(platform("${quarkusPlatformGroupId}:${quarkusPlatformArtifactId}:${quarkusPlatformVersion}"))

    api("com.fasterxml.jackson.core:jackson-databind")
    api("io.nats:jnats:2.26.3")
    api("io.micrometer:micrometer-core")
    api("org.slf4j:slf4j-api")

    // Annotations only: the applications provide the container and the configuration.
    compileOnly("jakarta.enterprise:jakarta.enterprise.cdi-api")
    compileOnly("jakarta.inject:jakarta.inject-api")
    compileOnly("org.eclipse.microprofile.config:microprofile-config-api")

    testFixturesApi("io.nats:jnats:2.26.3")
    testFixturesApi("com.fasterxml.jackson.core:jackson-databind")

    testImplementation("org.junit.jupiter:junit-jupiter")
    testImplementation("org.mockito:mockito-core")
    testImplementation("jakarta.enterprise:jakarta.enterprise.cdi-api")
    testImplementation("jakarta.inject:jakarta.inject-api")
    testImplementation("org.eclipse.microprofile.config:microprofile-config-api")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.withType<Test> {
    useJUnitPlatform()
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
