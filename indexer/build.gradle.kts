plugins {
    java
    jacoco
    checkstyle
    id("org.springframework.boot") version "4.1.1"
    id("io.spring.dependency-management") version "1.1.7"
}

group = "ai.kubemoot"
version = "0.1.0-SNAPSHOT"
description = "Kubemoot Indexer - Spring AI based document and MCP catalog indexer"

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

dependencyManagement {
    imports {
        mavenBom("org.springframework.ai:spring-ai-bom:2.0.1")
    }
}

dependencies {
    // Spring Boot
    implementation("org.springframework.boot:spring-boot-starter")
    implementation("org.springframework.boot:spring-boot-starter-webflux")

    // Spring AI - Embeddings
    implementation("org.springframework.ai:spring-ai-starter-model-ollama")

    // Spring AI - Vector Stores
    implementation("org.springframework.ai:spring-ai-starter-vector-store-pgvector")

    // Spring AI - Document processing
    implementation("org.springframework.ai:spring-ai-tika-document-reader")

    // Git support
    implementation("org.eclipse.jgit:org.eclipse.jgit:7.8.0.202609011348-r")

    // AWS S3 support
    implementation("software.amazon.awssdk:s3:2.55.12")

    // HTTP client for URL sources and MCP registries
    implementation("org.springframework.boot:spring-boot-starter-webflux")

    // Kubernetes client for Job annotation (checksum reporting)
    implementation("io.fabric8:kubernetes-client:8.0.0")

    // NATS client for KV source
    implementation("io.nats:jnats:2.26.4")

    // JSON processing
    implementation("com.fasterxml.jackson.core:jackson-databind")

    // PostgreSQL driver (for PgVectorStore)
    runtimeOnly("org.postgresql:postgresql")

    // Testing
    testImplementation("org.springframework.boot:spring-boot-starter-test")
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

