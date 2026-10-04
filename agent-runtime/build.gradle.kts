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

    // LangChain4j via Quarkus extension
    implementation("io.quarkiverse.langchain4j:quarkus-langchain4j-ollama")
    // JDK HTTP client for LangChain4j models built MANUALLY (ChatModelPool's
    // per-endpoint JIT models). quarkus-langchain4j-ollama EXCLUDES
    // langchain4j-http-client-jdk and wires its own JAX-RS client for the
    // INJECTED model only — so a manually-built OllamaChatModel finds no HTTP
    // client via ServiceLoader and fails in native with "No HTTP client has
    // been found in the classpath". Adding it back lets ChatModelPool set an
    // explicit JdkHttpClientBuilder (java.net.http — native-safe, the same
    // client the triage path already uses). Keeps LangChain4j as the
    // provider abstraction so other backends (vLLM, …) stay possible.
    implementation("dev.langchain4j:langchain4j-http-client-jdk")

    // MCP protocol client (framework-agnostic, used for direct MCP server connections).
    // mcp-core with the Jackson 2 binding instead of the `mcp` bundle, which brings
    // Jackson 3 and a YAML stack: the agent keeps the one Jackson that Quarkus manages
    // and the native image carries no second JSON library.
    implementation("io.modelcontextprotocol.sdk:mcp-core:2.0.1")
    implementation("io.modelcontextprotocol.sdk:mcp-json-jackson2:2.0.1")

    // NATS - publish chat events to NATS JetStream
    implementation("io.nats:jnats:2.26.3")

    // Observability
    implementation("io.quarkus:quarkus-micrometer-registry-prometheus")
    implementation("io.quarkus:quarkus-smallrye-health")

    // REST client for RAG and Gateway
    implementation("io.quarkus:quarkus-rest-client-jackson")

    // SSE support
    implementation("io.quarkus:quarkus-rest-qute")

    // Container image build (Kaniko in CI, no Docker daemon required)
    // No container-image extension: images are built by Paketo buildpacks with the
    // pack CLI (GitHub-hosted runners, laptops; see .github/workflows/quickstart.yaml)
    // or, on the in-cluster ARC runners that have no Docker daemon, by Kaniko around
    // the native binary (src/main/docker/Dockerfile.native).

    // Testing
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
