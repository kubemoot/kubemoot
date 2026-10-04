plugins {
    java
    jacoco
    id("org.springframework.boot") version "4.1.1"
    id("io.spring.dependency-management") version "1.1.7"
}

group = "kubemoot.ai"
version = "0.1.0-SNAPSHOT"
description = "Kubemoot MCP Gateway - Spring AI based MCP aggregation gateway"

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
        mavenBom("org.springframework.ai:spring-ai-bom:2.1.0-SNAPSHOT")
    }
}

dependencies {
    // Spring Boot
    implementation("org.springframework.boot:spring-boot-starter-web")
    implementation("org.springframework.boot:spring-boot-starter-webflux")
    implementation("org.springframework.boot:spring-boot-starter-actuator")

    // Jackson for JSON serialization
    implementation("com.fasterxml.jackson.core:jackson-databind")
    implementation("com.fasterxml.jackson.datatype:jackson-datatype-jsr310")

    // Spring AI MCP support
    implementation("org.springframework.ai:spring-ai-mcp")

    // Testing
    testImplementation("org.springframework.boot:spring-boot-starter-test")
    testImplementation("io.projectreactor:reactor-test")
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

    // Docker registry configuration for publishing
    docker {
        publishRegistry {
            url.set(findProperty("spring.boot.image.docker.publishRegistry.url")?.toString() ?: "ghcr.io")
            username.set(findProperty("spring.boot.image.docker.publishRegistry.username")?.toString() ?: "")
            password.set(findProperty("spring.boot.image.docker.publishRegistry.password")?.toString() ?: "")
        }
    }
}
