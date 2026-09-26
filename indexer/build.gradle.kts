plugins {
    java
    jacoco
    id("org.springframework.boot") version "4.0.1"
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

repositories {
    mavenCentral()
    maven { url = uri("https://repo.spring.io/milestone") }
    maven { url = uri("https://repo.spring.io/snapshot") }
}

dependencyManagement {
    imports {
        mavenBom("org.springframework.ai:spring-ai-bom:2.0.0-M1")
    }
}

dependencies {
    // Spring Boot
    implementation("org.springframework.boot:spring-boot-starter")
    implementation("org.springframework.boot:spring-boot-starter-webflux")

    // Spring AI - Embeddings
    implementation("org.springframework.ai:spring-ai-starter-model-ollama")

    // Spring AI - Vector Stores (artifact name changed in 1.0.0-M7)
    implementation("org.springframework.ai:spring-ai-starter-vector-store-pgvector")

    // Spring AI - Document processing
    implementation("org.springframework.ai:spring-ai-tika-document-reader")

    // Git support
    implementation("org.eclipse.jgit:org.eclipse.jgit:7.0.0.202409031743-r")

    // AWS S3 support
    implementation("software.amazon.awssdk:s3:2.29.0")

    // HTTP client for URL sources and MCP registries
    implementation("org.springframework.boot:spring-boot-starter-webflux")

    // Kubernetes client for Job annotation (checksum reporting)
    implementation("io.fabric8:kubernetes-client:7.1.0")

    // NATS client for KV source
    implementation("io.nats:jnats:2.25.3")

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

// Container image configuration
tasks.named<org.springframework.boot.gradle.tasks.bundling.BootBuildImage>("bootBuildImage") {
    imageName.set("${findProperty("image.registry") ?: "ghcr.io/kubemoot"}/${project.name}:${project.version}")

    environment.set(mapOf(
        "BP_JVM_VERSION" to "25"
    ))

    docker {
        publishRegistry {
            url.set(findProperty("spring.boot.image.docker.publishRegistry.url")?.toString() ?: "ghcr.io")
            username.set(findProperty("spring.boot.image.docker.publishRegistry.username")?.toString() ?: "")
            password.set(findProperty("spring.boot.image.docker.publishRegistry.password")?.toString() ?: "")
        }
    }
}
