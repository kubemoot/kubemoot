plugins {
    java
    jacoco
    checkstyle
    id("io.quarkus")
}

group = "ai.kubemoot"
version = "0.1.0-SNAPSHOT"
description = "Kubemoot Agent Runtime - LangChain4j based agent execution environment"

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
    // Quarkus BOM
    implementation(enforcedPlatform("${quarkusPlatformGroupId}:${quarkusPlatformArtifactId}:${quarkusPlatformVersion}"))
    // quarkus-langchain4j BOM from the same Quarkus platform release, so the
    // extension and the langchain4j core it resolves match the Quarkus version
    implementation(enforcedPlatform("${quarkusPlatformGroupId}:quarkus-langchain4j-bom:${quarkusPlatformVersion}"))

    // Quarkus core
    implementation("io.quarkus:quarkus-arc")
    implementation("io.quarkus:quarkus-rest")
    implementation("io.quarkus:quarkus-rest-jackson")

    // Jackson for JSON serialization
    implementation("com.fasterxml.jackson.core:jackson-databind")
    implementation("com.fasterxml.jackson.datatype:jackson-datatype-jsr310")

    // LangChain4j via the Quarkus Ollama extension: still the injected static-fallback
    // chat model and the quarkus.langchain4j.ollama.* settings ModelWarmupService and
    // DiscussionSubscriber read. Scheduled calls go through langchain4j-open-ai below.
    implementation("io.quarkiverse.langchain4j:quarkus-langchain4j-ollama")
    // The JDK HTTP client for the chat models ChatModelPool builds per endpoint. The
    // Quarkus extension wires its own client only into the model it injects, so a model
    // built by hand finds no HTTP client through ServiceLoader in the native image;
    // ChatModelPool sets this one explicitly (java.net.http, native-safe).
    implementation("dev.langchain4j:langchain4j-http-client-jdk")
    // The OpenAI-compatible chat client every agent call goes through, whatever the
    // engine (Ollama, vLLM, SGLang, a hosted API): the plain library, not the Quarkus
    // extension, because ChatModelPool builds one model per endpoint itself.
    implementation("dev.langchain4j:langchain4j-open-ai")

    // MCP protocol client (framework-agnostic, used for direct MCP server connections).
    // mcp-core with the Jackson 2 binding instead of the `mcp` bundle, which brings
    // Jackson 3 and a YAML stack: the agent keeps the one Jackson that Quarkus manages
    // and the native image carries no second JSON library.
    implementation("io.modelcontextprotocol.sdk:mcp-core:2.0.1")
    implementation("io.modelcontextprotocol.sdk:mcp-json-jackson2:2.0.1")

    // Placement: tickets, server selection and placement policies, shared with the placement
    // service. Its CDI beans are indexed through quarkus.index-dependency in application.properties.
    implementation("ai.kubemoot:placement-core")

    // NATS - publish chat events to NATS JetStream
    implementation("io.nats:jnats:2.26.3")

    // Observability
    implementation("io.quarkus:quarkus-micrometer-registry-prometheus")
    implementation("io.quarkus:quarkus-smallrye-health")

    // REST client for RAG and Gateway
    implementation("io.quarkus:quarkus-rest-client-jackson")

    // SSE support
    implementation("io.quarkus:quarkus-rest-qute")

    // No container-image extension. CI compiles the native binary on the runner, then
    // Paketo buildpacks package it unchanged: the Procfile buildpack runs it as the web
    // process (src/main/buildpacks/Procfile) on the pinned ubuntu-noble-run-tiny run image,
    // in an in-cluster build pod (.github/workflows/ci-agent-runtime.yaml).

    // Testing
    testImplementation(testFixtures("ai.kubemoot:placement-core"))
    testImplementation("io.quarkus:quarkus-junit5")
    testImplementation("io.rest-assured:rest-assured")
    testImplementation("org.mockito:mockito-core")
    // Validates what the runtime writes on the bus against operator/pkg/bus/schema.
    testImplementation("com.networknt:json-schema-validator:2.0.7")
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
