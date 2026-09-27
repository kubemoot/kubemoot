package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Optional;

/**
 * The namespace and crew that scope every name this agent puts on NATS: subjects,
 * durable consumer names, KV keys, and object keys. Namespaces are Kubemoot's
 * isolation boundary, so every format carries the namespace FIRST, then the crew
 * or agent. This record is the single place those formats are built and parsed;
 * no other class concatenates them.
 *
 * <p>A Kubernetes namespace is a DNS label (no dots), so it is always exactly one
 * NATS token. The crew is optional: a crewless agent is scoped by namespace alone,
 * and the subject simply omits the crew token.
 *
 * <p>Formats (ns = namespace):
 * <ul>
 *   <li>requests {@code kubemoot.request.<ns>.<crew>}, durable {@code request-<ns>-<crew>}</li>
 *   <li>discussion {@code kubemoot.discuss.<ns>.<crew>.<channel>.<thread>}</li>
 *   <li>lifecycle {@code kubemoot.lifecycle.<ns>.<crew>.<signal>.<agent>}</li>
 *   <li>artifacts {@code kubemoot.artifacts.<ns>.<crew>.<thread>}, objects {@code <ns>/<crew>/<thread>/...}</li>
 *   <li>chat {@code kubemoot.chat.<ns>.<agent>}</li>
 *   <li>KV: resumes {@code <ns>.<crew>}, memory {@code <ns>.<crew>.<topic>.<key>},
 *       agent state {@code <ns>.<agent>}, latency {@code latency.<ns>.<agent>.<provider>}</li>
 * </ul>
 */
public record CrewScope(String namespace, String crew) {

    /** Mounted into every pod with a service account; holds the pod's namespace. */
    public static final String SERVICE_ACCOUNT_NAMESPACE_FILE =
            "/var/run/secrets/kubernetes.io/serviceaccount/namespace";

    // Crewless placeholders. They differ because each keeps the token its own store
    // has always used: artifact keys and subjects say "nocrew", crew-memory keys say
    // "default".
    /** Crew segment of artifact subjects and object keys for a crewless agent. */
    static final String ARTIFACT_NO_CREW = "nocrew";
    /** Crew segment of crew-memory keys for a crewless agent. */
    static final String MEMORY_NO_CREW = "default";

    private static final String DISCUSS = "kubemoot.discuss.";
    private static final String REQUEST = "kubemoot.request.";
    private static final String LIFECYCLE = "kubemoot.lifecycle.";
    private static final String ARTIFACTS = "kubemoot.artifacts.";
    private static final String CHAT = "kubemoot.chat.";
    private static final String LATENCY = "latency.";
    private static final String BROADCAST = "broadcast";
    private static final String DOT = ".";

    /** A parsed crew-scoped discussion subject. */
    public record DiscussSubject(String namespace, String crew, String channel, String threadId) {}

    /** A parsed crew-scoped lifecycle subject. */
    public record LifecycleSubject(String namespace, String crew, String signal, String agent) {}

    public CrewScope {
        requireToken(namespace, "namespace");
        crew = (crew == null || crew.isBlank()) ? null : crew.strip();
        if (crew != null) {
            requireToken(crew, "crew");
        }
    }

    /** Scope for {@code namespace} and {@code crew}; a null or blank crew means crewless. */
    public static CrewScope of(String namespace, String crew) {
        return new CrewScope(namespace, crew);
    }

    /**
     * Scope from the agent's configuration: namespace from {@code KUBEMOOT_NAMESPACE}
     * (falling back to the service-account namespace file), crew from {@code KUBEMOOT_CREW}.
     *
     * @throws IllegalStateException when neither source yields a namespace
     */
    public static CrewScope fromProperties(AgentProperties properties) {
        String ns = resolveNamespace(properties.namespace().orElse(null),
                Path.of(SERVICE_ACCOUNT_NAMESPACE_FILE));
        return of(ns, properties.crew().orElse(null));
    }

    /**
     * The configured namespace when non-blank, else the content of {@code serviceAccountFile}.
     *
     * @throws IllegalStateException when both are absent or blank, so an agent never
     *                               silently falls back to an unscoped name
     */
    public static String resolveNamespace(String configured, Path serviceAccountFile) {
        if (configured != null && !configured.isBlank()) {
            return configured.strip();
        }
        return readNamespaceFile(serviceAccountFile).orElseThrow(() -> new IllegalStateException(
                "Kubernetes namespace unknown: set KUBEMOOT_NAMESPACE or mount "
                        + serviceAccountFile + "; refusing to use unscoped NATS subjects and keys"));
    }

    private static Optional<String> readNamespaceFile(Path file) {
        if (file == null || !Files.isReadable(file)) {
            return Optional.empty();
        }
        try {
            String ns = Files.readString(file, StandardCharsets.UTF_8).strip();
            return ns.isEmpty() ? Optional.empty() : Optional.of(ns);
        } catch (IOException e) {
            return Optional.empty();
        }
    }

    private static void requireToken(String value, String what) {
        if (value == null || value.isBlank()) {
            throw new IllegalArgumentException(what + " must not be blank");
        }
        if (!value.matches("[A-Za-z0-9_-]+")) {
            throw new IllegalArgumentException(what + " '" + value
                    + "' is not a single NATS token (letters, digits, '-', '_')");
        }
    }

    /** The same namespace with another crew (null or blank means crewless). */
    public CrewScope forCrew(String otherCrew) {
        return of(namespace, otherCrew);
    }

    public boolean hasCrew() {
        return crew != null;
    }

    // --- discussion subjects ---

    /** {@code kubemoot.discuss.<ns>.<crew>.} (or {@code kubemoot.discuss.<ns>.} when crewless). */
    public String discussPrefix() {
        return DISCUSS + scopeTokens() + DOT;
    }

    /** Every discussion subject in this scope: {@code kubemoot.discuss.<ns>.<crew>.>}. */
    public String discussWildcard() {
        return discussPrefix() + ">";
    }

    /** Every thread on one channel: {@code kubemoot.discuss.<ns>.<crew>.<channel>.>}. */
    public String channelWildcard(String channel) {
        return discussPrefix() + channel.strip() + ".>";
    }

    /** {@code kubemoot.discuss.<ns>.<crew>.<channel>.<thread>}. */
    public String discussSubject(String channel, String threadId) {
        return discussPrefix() + channel + DOT + threadId;
    }

    /** {@code kubemoot.discuss.<ns>.<crew>.broadcast.<thread>}. */
    public String broadcastSubject(String threadId) {
        return discussSubject(BROADCAST, threadId);
    }

    /**
     * The channel token of a discussion subject in THIS scope (the token after the
     * namespace and crew), or {@code fallback} when the subject is not in this scope
     * or carries no channel and thread.
     */
    public String channelOf(String subject, String fallback) {
        String prefix = discussPrefix();
        if (subject == null || !subject.startsWith(prefix)) {
            return fallback;
        }
        String rest = subject.substring(prefix.length());
        int dot = rest.indexOf('.');
        return dot > 0 ? rest.substring(0, dot) : fallback;
    }

    /**
     * Parse a crew-scoped discussion subject
     * {@code kubemoot.discuss.<ns>.<crew>.<channel>.<thread>}.
     */
    public static Optional<DiscussSubject> parseDiscuss(String subject) {
        String[] t = tokensAfter(subject, DISCUSS, 4);
        return t.length == 0 ? Optional.empty() : Optional.of(new DiscussSubject(t[0], t[1], t[2], t[3]));
    }

    // --- request queue ---

    /** {@code kubemoot.request.<ns>.<crew>}. */
    public String requestSubject() {
        return REQUEST + namespace + DOT + requireCrew();
    }

    /** Durable consumer on the request stream: {@code request-<ns>-<crew>}. */
    public String requestConsumer() {
        return "request-" + namespace + "-" + requireCrew();
    }

    // --- lifecycle ---

    /** {@code kubemoot.lifecycle.<ns>.<crew>.<signal>.<agent>}. */
    public String lifecycleSubject(String signal, String agent) {
        return LIFECYCLE + scopeTokens() + DOT + signal + DOT + agent;
    }

    /** Every lifecycle signal in this scope: {@code kubemoot.lifecycle.<ns>.<crew>.>}. */
    public String lifecycleWildcard() {
        return LIFECYCLE + scopeTokens() + ".>";
    }

    /** Parse a crew-scoped lifecycle subject {@code kubemoot.lifecycle.<ns>.<crew>.<signal>.<agent>}. */
    public static Optional<LifecycleSubject> parseLifecycle(String subject) {
        String[] t = tokensAfter(subject, LIFECYCLE, 4);
        return t.length == 0 ? Optional.empty() : Optional.of(new LifecycleSubject(t[0], t[1], t[2], t[3]));
    }

    // --- artifacts ---

    /** Artifact reference subject: {@code kubemoot.artifacts.<ns>.<crew>.<thread>}. */
    public String artifactSubject(String threadId) {
        return ARTIFACTS + namespace + DOT + artifactCrew() + DOT + threadId;
    }

    /** Object-store key prefix holding every artifact of this scope: {@code <ns>/<crew>/}. */
    public String artifactKeyPrefix() {
        return namespace + "/" + artifactCrew() + "/";
    }

    private String artifactCrew() {
        return hasCrew() ? crew : ARTIFACT_NO_CREW;
    }

    // --- chat ---

    /** Chat event subject: {@code kubemoot.chat.<ns>.<agent>} (agent hyphens become underscores). */
    public String chatSubject(String agent) {
        return CHAT + namespace + DOT + agent.replace("-", "_");
    }

    // --- KV keys ---

    /** Key in {@code kubemoot_crew_resumes}: {@code <ns>.<crew>}. */
    public String resumesKey() {
        return namespace + DOT + requireCrew();
    }

    /** Prefix of every crew-memory key in this scope: {@code <ns>.<crew>.}. */
    public String memoryPrefix() {
        return kvToken(namespace) + DOT + kvToken(hasCrew() ? crew : MEMORY_NO_CREW) + DOT;
    }

    /** Key in {@code kubemoot_crew_memory}: {@code <ns>.<crew>.<topic>.<key>}. */
    public String memoryKey(String topic, String key) {
        return memoryPrefix() + kvToken(topic) + DOT + kvToken(key);
    }

    /** Key in {@code kubemoot_agent_state}: {@code <ns>.<agent>}. */
    public String agentStateKey(String agent) {
        return namespace + DOT + agent;
    }

    /** Key in {@code kubemoot_latency}: {@code latency.<ns>.<agent>.<provider>}. */
    public String latencyKey(String agent, String provider) {
        return LATENCY + namespace + DOT + agent + DOT + latencyToken(provider);
    }

    /** One KV key token: NATS KV keys allow [A-Za-z0-9-_/=.], dots split tokens. */
    public static String kvToken(String s) {
        if (s == null || s.isEmpty()) return "_";
        return s.strip().replaceAll("[^A-Za-z0-9_=-]", "_");
    }

    /** A provider label as a latency key token (spaces, slashes, and dots become underscores). */
    static String latencyToken(String value) {
        return value.replace(" ", "_").replace("/", "_").replace(".", "_");
    }

    // --- internals ---

    private String scopeTokens() {
        return hasCrew() ? namespace + DOT + crew : namespace;
    }

    private String requireCrew() {
        if (!hasCrew()) {
            throw new IllegalStateException("no crew in scope for namespace " + namespace);
        }
        return crew;
    }

    /**
     * The first {@code count} tokens after {@code prefix}, the last one absorbing any
     * remaining dots; empty when the subject does not start with the prefix or has too
     * few non-empty tokens.
     */
    private static String[] tokensAfter(String subject, String prefix, int count) {
        if (subject == null || !subject.startsWith(prefix)) {
            return new String[0];
        }
        String[] t = subject.substring(prefix.length()).split("\\.", count);
        if (t.length != count) {
            return new String[0];
        }
        for (String token : t) {
            if (token.isEmpty()) return new String[0];
        }
        return t;
    }
}
