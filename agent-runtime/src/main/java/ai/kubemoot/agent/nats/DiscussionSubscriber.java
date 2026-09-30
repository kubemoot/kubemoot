package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.ToolCallFailure;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.util.GpuLabels;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.Message;
import io.nats.client.PushSubscribeOptions;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.ObjectStoreConfiguration;
import io.nats.client.api.StorageType;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.eclipse.microprofile.config.inject.ConfigProperty;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.*;
import java.util.concurrent.*;

/**
 * Tooler discussion subscriber — phased protocol.
 *
 * Activates on two orchestrator signals:
 * 1. advisory_ready: Evaluation phase — decide if relevant, use tools, contribute
 * 2. review_ready: Review phase — see others' responses, cross-check, supplement
 *
 * Does NOT activate on thread_start (coordinator generates advisory first).
 * Also handles follow_up and reply for multi-round discussions.
 *
 * Supports two subscription modes:
 * - Core NATS Dispatcher (default): ephemeral subscriptions, fire-and-forget
 * - JetStream push-subscribe: durable consumer with explicit ack, enables KEDA scale-to-zero
 */
@ApplicationScoped
public class DiscussionSubscriber {

    private static final Logger log = LoggerFactory.getLogger(DiscussionSubscriber.class);
    private static final ObjectMapper mapper = new ObjectMapper();

    private static final String NOTHING_TO_ADD = "NOTHING_TO_ADD";
    // A reply that starts with this raises a concern with the rest of the reply.
    static final String CONCERN_SENTINEL = "CONCERN:";
    private static final String TOOL_GAP_SENTINEL = "TOOL_GAP:";
    private static final String SIGNAL_CONCERN = "concern";
    private static final String FIELD_REVIEW_MODE = "reviewMode";
    private static final String REVIEW_MODE_CONCUR = "concur";
    private static final String SIGNAL_AGREE = "agree";
    private static final String SIGNAL_STAND_ASIDE = "stand_aside";
    /** Published once when an agent starts waiting for GPU capacity. */
    private static final String SIGNAL_WAITING = "waiting";
    private static final String SIGNAL_EVALUATING = "evaluating";
    /**
     * First-class consensus signal for "agent tried to evaluate but couldn't
     * complete due to infrastructure failure" (MCP tool timeout, model OOM,
     * etc.). Distinct from stand_aside (which is "agent chose not to weigh
     * in"). Carries structured cause metadata so the coordinator's settle
     * logic, dashboard, and GapDetector can distinguish "no relevant
     * tooler" from "toolers exist but their tools failed."
     */
    private static final String SIGNAL_FAILURE = "failure";

    // JSON field name constants (used in NATS message construction/parsing)
    private static final String FIELD_MESSAGE_ID = "messageId";
    private static final String FIELD_THREAD_ID = "threadId";
    private static final String FIELD_AGENT_NAME = "agentName";
    private static final String FIELD_MESSAGE_TYPE = "messageType";
    private static final String FIELD_CONTENT = "content";
    private static final String FIELD_CHANNEL = "channel";
    private static final String FIELD_TIMESTAMP = "timestamp";
    private static final String FIELD_METADATA = "metadata";
    private static final String FIELD_MODEL = "model";
    private static final String FIELD_INNER_CIRCLE = "innerCircle";

    // Artifact spill: a contribution larger than this is written to the NATS
    // Object Store and replaced inline with a preview + reference, so bulk data
    // is not truncated on the discussion bus (see Agent Collaborative Artifact
    // Store). The artifact-access sidecar materializes it for the no-network
    // sandbox; network-capable consumers read the structured metadata.artifact.
    private static final int ARTIFACT_SPILL_THRESHOLD = 4096;
    // Inline content cap for a contribution that does NOT spill (<= threshold) and
    // for the fallback when a spill FAILS (no artifact, so keep what we can inline).
    // A successfully-spilled contribution posts ONLY the marker, never a preview.
    private static final int INLINE_TRUNCATE_CHARS = 5000;
    // Short preview carried in the structured reference descriptor only.
    private static final int ARTIFACT_PREVIEW_CHARS = 800;
    // The inline spill marker prefix. On overflow the inline content is ONLY this
    // marker (key + bytes + /artifacts path) - no truncated preview - so consumers
    // read the full file rather than tally an incomplete preview. Kept compatible
    // with ChatService.ARTIFACT_KEY_PATTERN (key terminated by whitespace).
    private static final String ARTIFACT_MARKER_PREFIX = "[ARTIFACT key=";
    // JetStream API error code for "stream name already in use": a benign result when
    // the artifact bucket already exists (a concurrent or prior create won the race).
    private static final int JSAPI_STREAM_NAME_IN_USE = 10058;
    // Eviction caps for the in-memory dedup/context caches; cleared wholesale past these.
    private static final int MAX_PROCESSED_MESSAGES = 5000;
    private static final int MAX_THREAD_CONTEXT_ENTRIES = 500;

    // Failure-signal truncation caps (chars), by purpose.
    // Full error detail stored in failure metadata.lastError.
    private static final int ERROR_DETAIL_CHARS = 500;
    // Short error snippet echoed into a warning log line.
    private static final int ERROR_LOG_PREVIEW_CHARS = 200;
    // Error summary placed in the failure signal's user-visible content field.
    private static final int FAILURE_CONTENT_CHARS = 300;

    // Discussion heartbeat schedule (seconds): initial delay then fixed period.
    private static final long HEARTBEAT_INITIAL_DELAY_SECONDS = 15;
    private static final long HEARTBEAT_PERIOD_SECONDS = 30;
    // Period (minutes) between rate-limiter and stale-thread cleanup sweeps.
    private static final long CLEANUP_INTERVAL_MINUTES = 5;
    // Retry interval (seconds) for re-attempting NATS subscription at startup.
    private static final long SUBSCRIBE_RETRY_SECONDS = 30;

    // Message type constants
    private static final String MSG_THREAD_START = "thread_start";
    private static final String MSG_ADVISORY_READY = "advisory_ready";
    private static final String MSG_REVIEW_READY = "review_ready";
    private static final String MSG_REPLY = "reply";
    private static final String ROLE_ANALYST = "analyst";

    // Conversation context field constants
    private static final String FIELD_QUERY = "query";
    private static final String FIELD_RESPONSE = "response";

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;
    private final DiscussionRateLimiter rateLimiter;
    private final DiscussionMetrics metrics;
    private final AgentProperties properties;
    private final String ollamaBaseUrl;
    private final List<String> channels;
    private final boolean tooler;
    private final String priority;
    private final String jetstreamConsumer;
    /** Trigger message types for this agent, selected by its discuss-role. */
    private final Set<String> triggerTypes;
    /**
     * Loads skill ADL bodies from the per-crew ConfigMap mount. Reads ONLY the
     * coordinator-selected names for each thread (progressive disclosure). The
     * dir is injected so tests can supply a temp directory without touching the
     * real /app/config/skills path.
     */
    private SkillBodyLoader skillBodyLoader;

    private final ConcurrentHashMap<String, List<ThreadMessage>> threadContext = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<String, List<String>> threadTechnologies = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<String, List<?>> threadConversationContext = new ConcurrentHashMap<>();
    // The user's question per thread, from its thread_start, for knowledge retrieval.
    private final ConcurrentHashMap<String, String> threadQuestions = new ConcurrentHashMap<>();
    /** Selected skill names broadcast by the coordinator in advisory_ready, keyed by threadId. */
    private final ConcurrentHashMap<String, List<String>> threadSelectedSkills = new ConcurrentHashMap<>();
    private final Set<String> processedMessages = ConcurrentHashMap.newKeySet();
    private final Set<String> closedThreads = ConcurrentHashMap.newKeySet();
    /** Wake-ups for agents waiting for GPU capacity, run when their thread ends. */
    private final ConcurrentHashMap<String, List<Runnable>> threadEndWakers = new ConcurrentHashMap<>();
    // Ensure the artifact Object Store bucket once per process (idempotent create).
    private volatile boolean artifactBucketEnsured = false;

    private final ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(2);
    /**
     * Plans first calls at selection on its own thread, so a plan never queues behind
     * a long-running evaluation and a needed load still overlaps triage.
     */
    private final java.util.concurrent.ExecutorService planner = Executors.newSingleThreadExecutor(r -> {
        Thread t = new Thread(r, "discussion-call-planner");
        t.setDaemon(true);
        return t;
    });

    // Caller/tooler agents act in the EVALUATING phase (advisory_ready) and
    // multi-round turns (follow_up, reply). They do NOT trigger on review_ready:
    // re-triaging every agent in the review phase caused an all-agents re-triage
    // storm, so review_ready stays off their trigger set.
    private static final Set<String> TOOLER_TRIGGERS = Set.of(
            MSG_ADVISORY_READY, "follow_up", MSG_REPLY
    );
    // Analyst agents act in the REVIEW phase only. review_ready is re-enabled as a
    // trigger SCOPED to this role — only analysts wake on it, so the old storm
    // cannot recur. review_ready MAY carry an innerCircle when the coordinator
    // selected domain-relevant analysts (Coordinator Domain-Scoped Analyst
    // Subcommittee); isExcludedByInnerCircle gates on it the same as advisory_ready,
    // so a non-selected analyst silently skips. When no analysts were selected the
    // innerCircle is absent and all analysts review (the prior behavior). The
    // analyst reasons over the callers' gathered data the EVALUATING phase produced
    // (already accumulated in thread context).
    private static final Set<String> ANALYST_TRIGGERS = Set.of(MSG_REVIEW_READY);

    /** The trigger message types an agent acts on, by its discuss-role. */
    static Set<String> triggerTypesFor(String role) {
        return ROLE_ANALYST.equalsIgnoreCase(role) ? ANALYST_TRIGGERS : TOOLER_TRIGGERS;
    }

    // Single constructor: Quarkus ArC requires one constructor (or one annotated
    // @Inject). A second constructor silently dropped this bean's @Observes
    // StartupEvent observer in the native image, so toolers never subscribed.
    // SkillBodyLoader is built here from the env; tests override it via
    // setSkillBodyLoaderForTest so they need no second constructor.
    public DiscussionSubscriber(
            NatsConnectionProvider natsProvider,
            ChatService chatService,
            DiscussionMetrics metrics,
            AgentProperties properties,
            @ConfigProperty(name = "quarkus.langchain4j.ollama.base-url", defaultValue = "http://localhost:11434") String ollamaBaseUrl
    ) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
        this.metrics = metrics;
        this.properties = properties;
        this.ollamaBaseUrl = ollamaBaseUrl;
        this.skillBodyLoader = new SkillBodyLoader(System.getenv("KUBEMOOT_SKILLS_DIR"));
        this.tooler = properties.discuss().tooler();
        this.priority = properties.discuss().priority();
        this.jetstreamConsumer = properties.discuss().jetstreamConsumer().orElse(null);
        this.triggerTypes = triggerTypesFor(properties.discuss().role());
        this.rateLimiter = new DiscussionRateLimiter(
                properties.discuss().maxInferencesPerMinute(),
                properties.discuss().maxContributionsPerThread()
        );

        String channelsConfig = properties.discuss().channels().orElse("");
        if (!channelsConfig.isEmpty()) {
            this.channels = Arrays.asList(channelsConfig.split(","));
        } else {
            this.channels = List.of();
        }
    }

    /** Test seam: override the skill body loader with a temp-dir-backed one. */
    void setSkillBodyLoaderForTest(SkillBodyLoader loader) {
        this.skillBodyLoader = loader;
    }

    private volatile boolean subscribed = false;

    void onStart(@Observes StartupEvent event) {
        if (!tooler) {
            log.info("Agent discuss-role is observer — tooler subscriber disabled");
            return;
        }

        if ("high".equalsIgnoreCase(priority) || "low".equalsIgnoreCase(priority)) {
            log.info("Agent priority is {} — tooler subscriber defers to dedicated subscriber", priority);
            return;
        }

        if (channels.isEmpty()) {
            log.info("No discussion channels configured — subscriber inactive");
            return;
        }

        if (!natsProvider.isConfigured()) {
            log.info("NATS not configured — discussion subscriber disabled");
            return;
        }

        if (!trySubscribe()) {
            log.info("NATS not yet available — will retry subscription every 30s");
            scheduler.scheduleAtFixedRate(() -> {
                if (!subscribed) trySubscribe();
            }, SUBSCRIBE_RETRY_SECONDS, SUBSCRIBE_RETRY_SECONDS, TimeUnit.SECONDS);
        }
    }

    boolean trySubscribe() {
        Connection conn = natsProvider.getConnection();
        if (conn == null) return false;

        if (jetstreamConsumer != null && !jetstreamConsumer.isEmpty()) {
            return tryJetStreamSubscribe(conn);
        }
        return tryDispatcherSubscribe(conn);
    }

    /**
     * JetStream push-subscribe mode: uses a durable consumer created by the operator.
     * The consumer's server-side filter subjects handle channel matching.
     * Messages require explicit ack — ack after signal published or immediately for non-triggers.
     */
    private boolean tryJetStreamSubscribe(Connection conn) {
        try {
            JetStream js = conn.jetStream();

            // Bind to the existing durable consumer (created by the operator).
            // Use wildcard subject — the consumer's server-side filter subjects define what we actually receive.
            PushSubscribeOptions opts = PushSubscribeOptions.builder()
                    .durable(jetstreamConsumer)
                    .build();

            js.subscribe(natsProvider.scope().discussWildcard(), conn.createDispatcher(), msg -> {
                try {
                    handleJetStreamMessage(msg);
                } catch (Exception e) {
                    log.warn("Error handling JetStream message on {}: {}", msg.getSubject(), e.getMessage());
                    // Ack on error to prevent infinite redelivery of poison messages
                    msg.ack();
                }
            }, false, opts);

            log.info("JetStream push-subscribe active: consumer={}, agent={}",
                    jetstreamConsumer, properties.agentName());

        } catch (Exception e) {
            log.warn("Failed to create JetStream subscription for consumer {}: {}",
                    jetstreamConsumer, e.getMessage());
            return false;
        }

        scheduler.scheduleAtFixedRate(() -> {
            rateLimiter.cleanup();
            cleanupOldThreads();
        }, CLEANUP_INTERVAL_MINUTES, CLEANUP_INTERVAL_MINUTES, TimeUnit.MINUTES);

        subscribed = true;
        log.info("Discussion subscriber active (JetStream, priority={}) consumer={} for agent: {}",
                priority, jetstreamConsumer, properties.agentName());
        return true;
    }

    /**
     * Core NATS Dispatcher mode (default): ephemeral subscriptions, fire-and-forget.
     * Original behavior — backward compatible when no JetStream consumer is configured.
     */
    private boolean tryDispatcherSubscribe(Connection conn) {
        Dispatcher dispatcher = conn.createDispatcher();

        CrewScope scope = natsProvider.scope();
        String broadcastSubject = scope.channelWildcard("broadcast");
        dispatcher.subscribe(broadcastSubject, msg -> {
            try {
                handleMessage(msg.getSubject(), new String(msg.getData()));
            } catch (Exception e) {
                log.warn("Error handling broadcast message on {}: {}", msg.getSubject(), e.getMessage());
            }
        });
        log.info("Subscribed to broadcast: {}", broadcastSubject);

        for (String channel : channels) {
            String subject = scope.channelWildcard(channel);
            dispatcher.subscribe(subject, msg -> {
                try {
                    handleMessage(msg.getSubject(), new String(msg.getData()));
                } catch (Exception e) {
                    log.warn("Error handling discussion message on {}: {}", msg.getSubject(), e.getMessage());
                }
            });
            log.info("Subscribed to discussion channel: {}", subject);
        }

        scheduler.scheduleAtFixedRate(() -> {
            rateLimiter.cleanup();
            cleanupOldThreads();
        }, CLEANUP_INTERVAL_MINUTES, CLEANUP_INTERVAL_MINUTES, TimeUnit.MINUTES);

        subscribed = true;
        log.info("Discussion subscriber active (Dispatcher, priority={}) on {} channels for agent: {}",
                priority, channels.size(), properties.agentName());
        return true;
    }

    /**
     * Handle a JetStream message with explicit ack management.
     * - Non-trigger messages (thread context, closed threads): ack immediately
     * - Irrelevant messages: ack after publishing stand_aside
     * - Relevant messages: ack after evaluateAndRespond completes
     */
    private void handleJetStreamMessage(Message natsMsg) {
        String subject = natsMsg.getSubject();
        String data = new String(natsMsg.getData());

        try {
            var msg = mapper.readTree(data);
            String messageType = msg.has(FIELD_MESSAGE_TYPE) ? msg.get(FIELD_MESSAGE_TYPE).asText() : "";
            String threadId = msg.has(FIELD_THREAD_ID) ? msg.get(FIELD_THREAD_ID).asText() : "";
            String agentName = msg.has(FIELD_AGENT_NAME) ? msg.get(FIELD_AGENT_NAME).asText() : "";
            String content = msg.has(FIELD_CONTENT) ? msg.get(FIELD_CONTENT).asText() : "";
            String messageId = msg.has(FIELD_MESSAGE_ID) ? msg.get(FIELD_MESSAGE_ID).asText() : "";

            if (properties.agentName().equals(agentName)) { natsMsg.ack(); return; }
            if (!messageId.isEmpty() && !processedMessages.add(messageId)) { natsMsg.ack(); return; }

            trackThreadState(threadId, agentName, messageType, content);
            extractMetadata(data, messageType, threadId);

            if (closedThreads.contains(threadId) || !triggerTypes.contains(messageType)) { natsMsg.ack(); return; }
            if (!rateLimiter.tryAcquire(threadId)) { natsMsg.ack(); return; }
            if (isExcludedByInnerCircle(data, threadId)) { natsMsg.ack(); return; }

            // Subcommittee selection belongs to the coordinator (resume model via
            // vector pre-filter, or its LLM triage over the same resumes — which
            // embed each agent's keywords). isExcludedByInnerCircle has already
            // dropped non-selected agents; a redundant agent-side keyword gate only
            // vetoed correct semantic picks (e.g. "activity" ≠ literal "utilization"),
            // so it is removed. A selected agent is relevant by definition.
            var messages = threadContext.get(threadId);
            log.info("Evaluating thread {} on trigger '{}' for agent {} (selected by coordinator)",
                    threadId, messageType, properties.agentName());

            String conversation = formatThread(messages, threadId);
            boolean selected = isExplicitlySelected(data);
            boolean concurrence = selected && isConcurrenceRequest(data);
            scheduler.submit(() -> {
                try {
                    evaluateAndRespond(subject, threadId, conversation, selected, concurrence);
                } finally {
                    natsMsg.ack();
                }
            });

        } catch (Exception e) {
            log.warn("Failed to handle JetStream discussion message: {}", e.getMessage());
            natsMsg.ack();
        }
    }

    /**
     * Handle a core NATS Dispatcher message (no ack needed).
     */
    private void handleMessage(String subject, String data) {
        try {
            var msg = mapper.readTree(data);
            String messageType = msg.has(FIELD_MESSAGE_TYPE) ? msg.get(FIELD_MESSAGE_TYPE).asText() : "";
            String threadId = msg.has(FIELD_THREAD_ID) ? msg.get(FIELD_THREAD_ID).asText() : "";
            String agentName = msg.has(FIELD_AGENT_NAME) ? msg.get(FIELD_AGENT_NAME).asText() : "";
            String content = msg.has(FIELD_CONTENT) ? msg.get(FIELD_CONTENT).asText() : "";
            String messageId = msg.has(FIELD_MESSAGE_ID) ? msg.get(FIELD_MESSAGE_ID).asText() : "";

            if (properties.agentName().equals(agentName)) return;
            if (!messageId.isEmpty() && !processedMessages.add(messageId)) return;

            trackThreadState(threadId, agentName, messageType, content);
            extractMetadata(data, messageType, threadId);

            if (closedThreads.contains(threadId)) return;
            if (!triggerTypes.contains(messageType)) return;
            if (!rateLimiter.tryAcquire(threadId)) return;
            if (isExcludedByInnerCircle(data, threadId)) return;

            // Coordinator owns subcommittee selection; no agent-side keyword gate
            // (see handleJetStreamMessage). A selected agent is relevant by definition.
            var messages = threadContext.get(threadId);
            log.info("Evaluating thread {} on trigger '{}' for agent {} (selected by coordinator)",
                    threadId, messageType, properties.agentName());

            String conversation = formatThread(messages, threadId);
            boolean selected = isExplicitlySelected(data);
            boolean concurrence = selected && isConcurrenceRequest(data);
            scheduler.submit(() -> evaluateAndRespond(subject, threadId, conversation, selected, concurrence));

        } catch (Exception e) {
            log.warn("Failed to handle discussion message: {}", e.getMessage());
        }
    }

    private void trackThreadState(String threadId, String agentName, String messageType, String content) {
        if ("synthesis".equals(messageType) || "thread_close".equals(messageType)) {
            closedThreads.add(threadId);
            wakeThreadEnd(threadId);
        }
        if (MSG_REPLY.equals(messageType) && "human".equals(agentName)) {
            closedThreads.remove(threadId);
        }
        var messages = threadContext.computeIfAbsent(threadId, k -> new CopyOnWriteArrayList<>());
        messages.add(new ThreadMessage(agentName, messageType, content, Instant.now()));
    }

    /**
     * Extract metadata (technologies, conversation context, selected skills) from
     * thread_start/advisory_ready messages. All extractions are best-effort:
     * a parse failure in any field must not abort message handling.
     */
    private void extractMetadata(String data, String messageType, String threadId) {
        if (!MSG_THREAD_START.equals(messageType) && !MSG_ADVISORY_READY.equals(messageType)) {
            return;
        }
        try {
            var msgNode = mapper.readTree(data);
            if (MSG_THREAD_START.equals(messageType)) {
                recordQuestion(threadId, DiscussionOrchestrator.extractUserQuery(msgNode));
            }
            if (!msgNode.has(FIELD_METADATA)) {
                return;
            }
            var meta = msgNode.get(FIELD_METADATA);
            extractTechnologies(meta, threadId);
            extractConversationContext(meta, threadId);
            extractSelectedSkills(meta, threadId);
        } catch (Exception ignored) {
            // Metadata is best-effort enrichment; a malformed message must not
            // abort message handling, so parse failures are silently skipped.
        }
    }

    private void extractTechnologies(JsonNode meta, String threadId) {
        if (!meta.has("technologies")) {
            return;
        }
        var techs = new ArrayList<String>();
        meta.get("technologies").forEach(n -> techs.add(n.asText()));
        threadTechnologies.put(threadId, techs);
    }

    /**
     * Extract the coordinator-selected skill names from advisory_ready metadata.
     * Stored per thread so the mulling phase can load the bodies on demand.
     * Only stored once per thread (first advisory_ready wins; follow-up advisory
     * messages use the same skills as the original).
     */
    private void extractSelectedSkills(JsonNode meta, String threadId) {
        if (!meta.has("selectedSkills") || threadSelectedSkills.containsKey(threadId)) {
            return;
        }
        var skillsNode = meta.get("selectedSkills");
        if (!skillsNode.isArray() || skillsNode.isEmpty()) {
            return;
        }
        var names = new ArrayList<String>();
        skillsNode.forEach(n -> names.add(n.asText()));
        threadSelectedSkills.put(threadId, names);
        log.info("Thread {} has {} selected skill(s): {}", threadId, names.size(), names);
    }

    private void extractConversationContext(JsonNode meta, String threadId) {
        if (!meta.has("conversationContext") || threadConversationContext.containsKey(threadId)) {
            return;
        }
        var contextNode = meta.get("conversationContext");
        if (!contextNode.isArray() || contextNode.isEmpty()) {
            return;
        }
        var contextList = new ArrayList<Map<String, String>>();
        for (var item : contextNode) {
            var turn = new HashMap<String, String>();
            if (item.has(FIELD_QUERY)) turn.put(FIELD_QUERY, item.get(FIELD_QUERY).asText());
            if (item.has(FIELD_RESPONSE)) turn.put(FIELD_RESPONSE, item.get(FIELD_RESPONSE).asText());
            contextList.add(turn);
        }
        threadConversationContext.put(threadId, contextList);
        log.info("Thread {} has {} prior conversation turns for context",
                threadId, contextList.size());
    }

    // Template from config + agent identity + tools, rebuilt when the tool list changes
    private String triagePromptBase;
    private String triagePromptTools;

    private String buildTriagePrompt(String threadId) {
        var toolNames = chatService.getToolNames();
        String toolList = toolNames.isEmpty() ? "(none)" : String.join(", ", toolNames);
        if (triagePromptBase == null || !toolList.equals(triagePromptTools)) {
            triagePromptTools = toolList;
            String template = properties.discuss().triagePrompt().orElse(DEFAULT_TRIAGE_TEMPLATE);

            String description = properties.agentDescription();
            if (description == null || description.isEmpty()) {
                description = "(no description)";
            }

            triagePromptBase = template
                    .replace("{agent_name}", properties.agentName())
                    .replace("{agent_description}", description)
                    .replace("{tool_names}", toolList);
        }

        // Advisory technologies are per-thread — substitute at call time
        var technologies = threadTechnologies.get(threadId);
        String techList = (technologies != null && !technologies.isEmpty())
                ? String.join(", ", technologies) : "(not yet determined)";

        return triagePromptBase.replace("{advisory_technologies}", techList);
    }

    private static final String DEFAULT_TRIAGE_TEMPLATE = """
            You are agent '{agent_name}': {agent_description}
            Your available tools: {tool_names}

            The coordinator identified these relevant areas: {advisory_technologies}

            Based on the conversation below, could your tools or expertise contribute
            ANY useful data? Even partial data from your domain adds value.

            Reply with EXACTLY one of:
            - CONTRIBUTE — if your tools or knowledge can help answer this question
            - NOTHING_TO_ADD — if this is outside your expertise or you have nothing useful to add

            Do not explain your reasoning. Reply with only one of the above.""";

    /**
     * Two-phase evaluation: triage (lightweight model) then mulling (full model).
     *
     * Phase 1 — Triage: Quick "should I contribute?" on the smaller GPU (e.g., 4090/qwen3:14b).
     *   ~80% of agents stand aside here, saving the expensive GPU for agents that matter.
     *
     * Phase 2 — Mulling: Full inference with tools on the larger GPU (e.g., 5090/qwen3:32b).
     *   Only runs for agents that passed triage.
     */
    private void evaluateAndRespond(String subject, String threadId, String conversation,
                                    boolean explicitlySelected, boolean concurrence) {
        try {
            if (concurrence) {
                answerConcurrence(subject, threadId, conversation);
            } else {
                evaluateSelected(subject, threadId, conversation, explicitlySelected);
            }
        } finally {
            // Whatever the outcome (answered, stood aside, failed), a plan made at
            // selection that the first call did not use is released here.
            chatService.releasePlan(threadId);
        }
    }

    /**
     * The coordinator selected this agent: plan its first mulling call now so a
     * needed model load overlaps triage and prompt building.
     */
    // Visible for testing
    void commitToThread(String threadId, String conversation) {
        long selectedAtMs = System.currentTimeMillis();
        planner.submit(() -> {
            try {
                chatService.commitToThread(threadId, selectedAtMs, conversation);
            } catch (Exception e) {
                log.warn("Planning the first call for thread {} failed: {}", threadId, e.getMessage());
            }
        });
    }

    /**
     * A concurrence request is addressed to this agent by name, so there is no
     * should-I-contribute question to triage: the agent goes straight to its
     * evaluation and answers (agree, or a concern).
     */
    private void answerConcurrence(String subject, String threadId, String conversation) {
        try {
            if (closedThreads.contains(threadId)) {
                publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", 0, 0, 0, 0, "none");
                return;
            }
            commitToThread(threadId, conversation);
            log.info("Agent {} answering a concurrence request for thread {}", properties.agentName(), threadId);
            runMullingPhase(subject, threadId, conversation, 0, System.currentTimeMillis());
        } catch (Exception e) {
            log.warn("Failed to answer the concurrence request on thread {}: {}", threadId, e.getMessage());
            publishExceptionAsFailure(subject, threadId, e, 0, 0, GpuLabels.fromEndpoint(ollamaBaseUrl));
        }
    }

    private void evaluateSelected(String subject, String threadId, String conversation,
                                  boolean explicitlySelected) {
        try {
            if (closedThreads.contains(threadId)) {
                log.debug("Thread {} closed before evaluation — {} standing aside", threadId, properties.agentName());
                publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", 0, 0, 0, 0, "none");
                return;
            }

            commitToThread(threadId, conversation);
            String triageGpuLabel = GpuLabels.fromEndpoint(chatService.getTriageEndpoint());
            publishSignal(subject, threadId, "triaging", "Queued for triage assessment",
                    0, System.currentTimeMillis(), 0, 0, triageGpuLabel);

            long triageStartMs = System.currentTimeMillis();
            String triageResponse = runTriage(subject, threadId, conversation, triageGpuLabel, triageStartMs);
            if (triageResponse == null) return;
            long triageMs = System.currentTimeMillis() - triageStartMs;

            if (isClosedAfterTriage(subject, threadId, triageMs, triageStartMs)) return;

            if (triageResponse.contains(NOTHING_TO_ADD) && !explicitlySelected) {
                log.debug("Agent {} standing aside after triage for thread {} ({}ms)",
                        properties.agentName(), threadId, triageMs);
                String triageGpu = GpuLabels.fromEndpoint(chatService.getTriageEndpoint());
                publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", triageMs, triageStartMs, 0, 0, triageGpu);
                return;
            }
            if (triageResponse.contains(NOTHING_TO_ADD)) {
                log.info("Agent {} triaged NOTHING_TO_ADD but was coordinator-selected for thread {} - "
                        + "gathering with tools instead of vetoing on the lightweight model",
                        properties.agentName(), threadId);
            }

            runMullingPhase(subject, threadId, conversation, triageMs, triageStartMs);

        } catch (Exception e) {
            log.warn("Failed to evaluate thread {}: {} — publishing stand_aside", threadId, e.getMessage());
            publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", 0, 0, 0, 0);
        }
    }

    // Visible for testing
    String runTriage(String subject, String threadId, String conversation,
                             String triageGpuLabel, long triageStartMs) {
        try {
            String triagePrompt = buildTriagePrompt(threadId);
            return chatService.triageChat(triagePrompt, conversation);
        } catch (ai.kubemoot.agent.provider.NoFitException nfe) {
            // The triage call could not be placed: the same capacity stand-aside as a
            // mulling call, with its reason, so the coordinator can name the cluster.
            handleNoFit(subject, threadId, nfe, 0L, triageStartMs, triageStartMs, triageGpuLabel);
            return null;
        } catch (Exception e) {
            long triageMs = System.currentTimeMillis() - triageStartMs;
            log.info("Triage failed for {} on thread {} ({}ms) - standing aside: {}",
                    properties.agentName(), threadId, triageMs, e.getMessage());
            publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", triageMs, triageStartMs, 0, 0, triageGpuLabel);
            return null;
        }
    }

    private boolean isClosedAfterTriage(String subject, String threadId, long triageMs, long triageStartMs) {
        if (!closedThreads.contains(threadId)) return false;
        log.info("Thread {} closed during triage — {} standing aside",
                threadId, properties.agentName());
        String triageGpu = GpuLabels.fromEndpoint(chatService.getTriageEndpoint());
        publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", triageMs, triageStartMs, 0, 0, triageGpu);
        return true;
    }

    private void runMullingPhase(String subject, String threadId, String conversation,
                                  long triageMs, long triageStartMs) {
        // The coordinator already judged this agent relevant, so it always runs
        // the full tool-calling evaluation on the primary GPU.
        String mullingGpuLabel = GpuLabels.fromEndpoint(ollamaBaseUrl);

        publishSignal(subject, threadId, SIGNAL_EVALUATING, "Running tool-calling evaluation",
                triageMs, triageStartMs, 0, 0, mullingGpuLabel);
        log.info("Agent {} passed triage ({}ms), running full evaluation for thread {} (gpu={})",
                properties.agentName(), triageMs, threadId, mullingGpuLabel);

        var heartbeat = startHeartbeat(subject, threadId, mullingGpuLabel);

        long mullingStartMs = System.currentTimeMillis();
        ChatService.ChatResult result;
        try {
            result = runMullingInference(conversation, threadId,
                    new ThreadCapacityWait(subject, threadId, mullingGpuLabel));
        } catch (ToolCallFailure tcf) {
            handleToolCallFailure(subject, threadId, tcf, triageMs, triageStartMs, mullingStartMs, mullingGpuLabel);
            return;
        } catch (ai.kubemoot.agent.provider.NoFitException nfe) {
            handleNoFit(subject, threadId, nfe, triageMs, triageStartMs, mullingStartMs, mullingGpuLabel);
            return;
        } catch (Exception other) {
            handleMullingException(subject, threadId, other, triageMs, triageStartMs, mullingStartMs, mullingGpuLabel);
            return;
        } finally {
            heartbeat.cancel(false);
        }
        long mullingMs = System.currentTimeMillis() - mullingStartMs;
        long totalMs = triageMs + mullingMs;
        long inTok = result != null ? result.inputTokens() : 0;
        long outTok = result != null ? result.outputTokens() : 0;
        metrics.recordAgentInference(java.time.Duration.ofMillis(totalMs));
        metrics.recordTokens(inTok, outTok);

        classifyAndPublishResult(subject, threadId, result, totalMs, triageStartMs, inTok, outTok, mullingGpuLabel);
    }

    /**
     * Bounded tool-retry exhaustion. Publish a first-class failure
     * signal with cause metadata instead of silently retrying or
     * synthesising a stand_aside that conflates infrastructure
     * failure with the agent choosing not to weigh in. The
     * coordinator's settle logic treats failure like stand_aside
     * (don't wait) but counts it separately for gap detection.
     */
    private void handleToolCallFailure(String subject, String threadId, ToolCallFailure tcf,
                                       long triageMs, long triageStartMs, long mullingStartMs, String mullingGpuLabel) {
        long mullingMs = System.currentTimeMillis() - mullingStartMs;
        long totalMs = triageMs + mullingMs;
        publishFailure(subject, threadId, tcf, totalMs, triageStartMs, mullingGpuLabel);
    }

    /**
     * No GPU could run this call. Publish stand_aside carrying
     * {@code metadata.reason} ({@code gpu-busy}: every GPU that could hold the
     * model stayed busy until the thread ended or the capacity wait reached its
     * safety limit; {@code model-too-large}: no GPU can ever hold it;
     * {@code prompt-too-large}: the prompt exceeds every provider's context) and
     * {@code metadata.model}, so the coordinator and the dashboard can say the
     * cluster, not the crew design, kept this agent out. Distinct from a
     * triage-time stand-aside (nothing to add) and from a failure (something
     * broke during the call).
     */
    private void handleNoFit(String subject, String threadId, ai.kubemoot.agent.provider.NoFitException nfe,
                             long triageMs, long triageStartMs, long mullingStartMs, String mullingGpuLabel) {
        long mullingMs = System.currentTimeMillis() - mullingStartMs;
        long totalMs = triageMs + mullingMs;
        log.info("Agent {} stand-aside ({}) on thread {}: {}",
                properties.agentName(), nfe.reason(), threadId, nfe.predictorReason());
        publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, noFitContent(nfe),
                totalMs, triageStartMs, 0, 0, mullingGpuLabel, noFitMetadata(nfe));
    }

    // Visible for testing
    static String noFitContent(ai.kubemoot.agent.provider.NoFitException nfe) {
        if (ai.kubemoot.agent.provider.NoFitException.REASON_MODEL_TOO_LARGE.equals(nfe.reason())) {
            return "No GPU in this cluster can hold the model " + nfe.model();
        }
        if (ai.kubemoot.agent.provider.NoFitException.REASON_PROMPT_TOO_LARGE.equals(nfe.reason())) {
            return "The prompt is larger than the context window any GPU gives " + nfe.model();
        }
        return "Could not get a GPU: every GPU that can hold " + nfe.model() + " was busy";
    }

    // Visible for testing
    static Map<String, Object> noFitMetadata(ai.kubemoot.agent.provider.NoFitException nfe) {
        return Map.of("reason", nfe.reason(),
                FIELD_MODEL, nfe.model(),
                "predictorReason", nfe.predictorReason() == null ? "" : nfe.predictorReason());
    }

    /** Runs and forgets every capacity-wait wake-up registered for the thread. */
    private void wakeThreadEnd(String threadId) {
        chatService.releasePlan(threadId);
        List<Runnable> wakers = threadEndWakers.remove(threadId);
        if (wakers != null) {
            wakers.forEach(Runnable::run);
        }
    }

    /**
     * The subscriber's side of a GPU capacity wait for one thread: the wait ends
     * with the thread, {@code waiting} is published when it starts, and
     * {@code evaluating} is published again when capacity arrives.
     */
    final class ThreadCapacityWait implements ai.kubemoot.agent.provider.CapacityWait {
        private final String subject;
        private final String threadId;
        private final String gpuLabel;

        ThreadCapacityWait(String subject, String threadId, String gpuLabel) {
            this.subject = subject;
            this.threadId = threadId;
            this.gpuLabel = gpuLabel;
        }

        @Override
        public boolean threadEnded() {
            return closedThreads.contains(threadId);
        }

        @Override
        public void onWaiting(String model) {
            publishSignal(subject, threadId, SIGNAL_WAITING, "Waiting for a GPU with room for " + model,
                    0, System.currentTimeMillis(), 0, 0, gpuLabel,
                    Map.of(FIELD_MODEL, model, "reason", ai.kubemoot.agent.provider.NoFitException.REASON_GPU_BUSY));
        }

        @Override
        public void onCapacity(String model) {
            publishSignal(subject, threadId, SIGNAL_EVALUATING, "Running tool-calling evaluation",
                    0, System.currentTimeMillis(), 0, 0, gpuLabel, Map.of(FIELD_MODEL, model));
        }

        @Override
        public void wakeOnEnd(Runnable wake) {
            threadEndWakers.computeIfAbsent(threadId, k -> new CopyOnWriteArrayList<>()).add(wake);
            if (closedThreads.contains(threadId)) {
                wakeThreadEnd(threadId);
            }
        }
    }

    /**
     * FIX #2 of three: widen the failure-signal coverage to ANY
     * unexpected exception during mulling (HTTP timeout from the
     * Ollama POST, RESTEasy ProcessingException, OOM, NATS hiccup,
     * GraalVM native runtime issues, etc.) — not just
     * ToolCallFailure. Previously these exceptions bubbled past
     * this catch into evaluateAndRespond's generic catch which
     * published `stand_aside`, conflating "agent gave up" with
     * "agent couldn't complete due to infrastructure failure".
     * Now they surface as proper `failure` signals with cause
     * metadata so the dashboard timeline, coordinator gap
     * detection, and operator can see the real shape of the
     * problem. Observed concretely 2026-05-25 on k8s-metrics'
     * Ollama timeout: 6 min retry storm → generic stand_aside
     * → coordinator wait → discussion stuck.
     */
    private void handleMullingException(String subject, String threadId, Exception other,
                                        long triageMs, long triageStartMs, long mullingStartMs, String mullingGpuLabel) {
        long mullingMs = System.currentTimeMillis() - mullingStartMs;
        long totalMs = triageMs + mullingMs;
        publishExceptionAsFailure(subject, threadId, other, totalMs, triageStartMs, mullingGpuLabel);
    }

    /**
     * Build metadata Map carrying the JIT-selected provider name when
     * present, otherwise empty. Per-Call Provider Attribution (Card #4
     * of [[Epic - JIT GPU Scheduling]]): surfaces which provider actually
     * served THIS inference call so the dashboard timeline can show real
     * per-call GPU usage instead of the static reconcile-time label.
     * Empty result skips the field entirely (omitempty-equivalent).
     */
    // Visible for testing
    static Map<String, Object> providerAttribution(ChatService.ChatResult result) {
        if (result == null || result.providerName() == null || result.providerName().isEmpty()) {
            return null;
        }
        // pickReason carries the predictor's reasoning (e.g. "warm, slot 1/2 |
        // SR=0.92 over 23 samples, EMA latency 4200ms") so the dashboard shows WHY
        // the selector picked this provider; model is the model the call ran on,
        // which differs from the bound model when a warm candidate was used.
        var meta = new HashMap<String, Object>();
        meta.put("provider", result.providerName());
        if (result.model() != null && !result.model().isEmpty()) {
            meta.put(FIELD_MODEL, result.model());
        }
        if (result.pickReason() != null && !result.pickReason().isEmpty()) {
            meta.put("pickReason", result.pickReason());
        }
        if (result.evicted() != null && !result.evicted().isEmpty()) {
            meta.put("evicted", result.evicted());
        }
        return meta;
    }

    /**
     * Publish a {@link #SIGNAL_FAILURE} signal for an unexpected
     * exception thrown during mulling — HTTP timeout (Ollama call
     * exceeded its window), processing exception, OOM, etc. Distinct
     * from {@link #publishFailure(String, String, ToolCallFailure, long, long, String)}
     * which handles the well-typed ToolCallFailure path: here the
     * cause comes from arbitrary downstream exceptions that we still
     * want to surface as first-class consensus failures rather than
     * letting them collapse to a generic stand_aside.
     *
     * Failure-type classification heuristic uses the exception's class
     * and message to map to a known failureType bucket the dashboard /
     * GapDetector understand. Unknown exceptions get "internal_exception".
     */
    private void publishExceptionAsFailure(String subject, String threadId, Exception e,
                                           long totalMs, long triageStartMs, String gpuLabel) {
        String exceptionClass = e.getClass().getName();
        String message = e.getMessage() != null ? e.getMessage() : exceptionClass;
        // Heuristic classification — keep simple, expand as observed
        // failure modes accumulate. Ollama timeout via Vertx surfaces
        // as ProcessingException with "timeout period of X" message.
        String failureType;
        if (message.contains("timeout period of") || message.contains("Timed out")
                || exceptionClass.contains("Timeout")) {
            failureType = "model_timeout";
        } else if (exceptionClass.contains("OutOfMemory")) {
            failureType = "model_oom";
        } else if (message.contains("Connection refused") || message.contains("ConnectException")) {
            failureType = "provider_unreachable";
        } else {
            failureType = "internal_exception";
        }
        var meta = new HashMap<String, Object>();
        meta.put("failureType", failureType);
        meta.put("exceptionClass", exceptionClass);
        meta.put("lastError", truncate(message, ERROR_DETAIL_CHARS));
        log.warn("Agent {} publishing failure signal for thread {}: {} ({})",
                properties.agentName(), threadId, failureType, truncate(message, ERROR_LOG_PREVIEW_CHARS));
        publishSignal(subject, threadId, SIGNAL_FAILURE, "Mulling failed: " + truncate(message, FAILURE_CONTENT_CHARS),
                totalMs, triageStartMs, 0, 0, gpuLabel, meta);
    }

    /**
     * Publish a {@link #SIGNAL_FAILURE} signal carrying structured cause
     * metadata derived from the {@link ToolCallFailure}. Visible inline on
     * the dashboard timeline next to other signals; the coordinator counts
     * it separately from stand_aside so GapDetector can distinguish
     * "no tooler had relevant expertise" from "toolers tried but
     * their tools failed."
     */
    private void publishFailure(String subject, String threadId, ToolCallFailure tcf,
                                long totalMs, long triageStartMs, String gpuLabel) {
        var failureMeta = new HashMap<String, Object>();
        failureMeta.put("failureType", tcf.failureType().name().toLowerCase());
        if (tcf.toolName() != null) {
            failureMeta.put("failedTool", tcf.toolName());
        }
        failureMeta.put("failureCount", tcf.failureCount());
        if (tcf.lastErrorContent() != null) {
            failureMeta.put("lastError", truncate(tcf.lastErrorContent(), ERROR_DETAIL_CHARS));
        }
        log.warn("Agent {} publishing failure signal for thread {}: {} (tool={}, count={})",
                properties.agentName(), threadId, tcf.failureType(),
                tcf.toolName(), tcf.failureCount());
        publishSignal(subject, threadId, SIGNAL_FAILURE, tcf.getMessage(),
                totalMs, triageStartMs, 0, 0, gpuLabel, failureMeta);
    }

    private java.util.concurrent.ScheduledFuture<?> startHeartbeat(String subject, String threadId, String mullingGpuLabel) {
        var heartbeatRef = new java.util.concurrent.atomic.AtomicReference<java.util.concurrent.ScheduledFuture<?>>();
        heartbeatRef.set(scheduler.scheduleAtFixedRate(() -> {
            try {
                if (closedThreads.contains(threadId)) {
                    log.info("Thread {} closed — stopping heartbeat for {}", threadId, properties.agentName());
                    var self = heartbeatRef.get();
                    if (self != null) self.cancel(false);
                    return;
                }
                log.info("Publishing discussion heartbeat for {} on thread {}", properties.agentName(), threadId);
                publishSignal(subject, threadId, "heartbeat", "Queued or running inference",
                        0, 0, 0, 0, mullingGpuLabel);
            } catch (Exception e) {
                log.warn("Heartbeat publish failed for {} on thread {}: {}", properties.agentName(), threadId, e.getMessage());
            }
        }, HEARTBEAT_INITIAL_DELAY_SECONDS, HEARTBEAT_PERIOD_SECONDS, TimeUnit.SECONDS));
        return heartbeatRef.get();
    }

    private ChatService.ChatResult runMullingInference(String conversation, String threadId,
                                                       ai.kubemoot.agent.provider.CapacityWait wait) {
        // Pass threadId as discussionThreadId so the LLM sees the discussion
        // context in its system prompt — used by scheduler-advisor (and any
        // future MCP) to thread conversational continuity through tool calls.
        //
        // Skill injection (Chunk 4): when the coordinator selected skills for this
        // thread, prepend their ADL bodies to the conversation so the specialist
        // sees the guidance inline. ZERO guarantee: when no skills were selected
        // (the baseline), skillContext is empty and the message is byte-identical
        // to what it was before skills were introduced.
        String skillContext = skillBodyLoader.load(threadSelectedSkills.get(threadId));
        String message = skillContext.isEmpty() ? conversation : skillContext + conversation;
        var request = new ChatService.ChatRequest(threadId, message, null, threadId, retrievalQueryFor(threadId));
        return chatService.directChat(request, false, wait);
    }

    /**
     * The user's own words in a thread, for knowledge retrieval: the question that
     * opened it, followed by the latest human reply when there is one. Null when the
     * thread's question has not been seen, so retrieval falls back to the message.
     */
    // Visible for testing
    String retrievalQueryFor(String threadId) {
        String question = threadQuestions.get(threadId);
        String latestReply = latestHumanReply(threadId);
        if (question == null) {
            return latestReply;
        }
        return latestReply == null ? question : question + "\n" + latestReply;
    }

    private void recordQuestion(String threadId, String question) {
        if (question != null && !question.isBlank()) {
            threadQuestions.putIfAbsent(threadId, question);
        }
    }

    private String latestHumanReply(String threadId) {
        var messages = threadContext.get(threadId);
        String latest = null;
        if (messages != null) {
            for (var msg : messages) {
                if (isHumanReply(msg)) {
                    latest = msg.content;
                }
            }
        }
        return latest;
    }

    private static boolean isHumanReply(ThreadMessage msg) {
        return MSG_REPLY.equals(msg.messageType) && "human".equals(msg.agentName) && !msg.content.isBlank();
    }

    private void classifyAndPublishResult(String subject, String threadId, ChatService.ChatResult result,
                                           long totalMs, long triageStartMs, long inTok, long outTok, String gpuLabel) {
        // The GPU badge should reflect WHERE this inference actually ran — the
        // JIT-selected provider — not the agent's static reconcile-time
        // endpoint. Without this, an agent that JIT-picked the 5090 still shows
        // its static 4090 label (observed 2026-05-26, thread 0d310372:
        // obs-metrics ran on rig0/5090 but the dashboard showed 4090). Fall
        // back to the static label when no JIT pick was made (selector unwired
        // / static fallback). See [[Per-Call Provider Attribution]].
        String jitGpu = result == null ? null : GpuLabels.fromProvider(result.providerName());
        if (jitGpu != null) gpuLabel = jitGpu;

        if (closedThreads.contains(threadId)) {
            log.info("Thread {} closed during mulling — {} publishing late stand_aside",
                    threadId, properties.agentName());
            publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", totalMs, triageStartMs, inTok, outTok, gpuLabel);
            return;
        }

        if (result == null || result.response() == null || result.response().isEmpty()) {
            log.warn("No response from mulling for thread {} — publishing stand_aside", threadId);
            publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", totalMs, triageStartMs, inTok, outTok, gpuLabel);
            return;
        }

        String content = result.response();

        if (content.contains(NOTHING_TO_ADD)) {
            log.debug("Agent {} standing aside after mulling for thread {}", properties.agentName(), threadId);
            publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, "", totalMs, triageStartMs, inTok, outTok, gpuLabel);
            return;
        }

        String concern = concernIn(content);
        if (concern != null) {
            log.info("Agent {} raised a concern on thread {}: {}", properties.agentName(), threadId,
                    truncate(concern, ERROR_LOG_PREVIEW_CHARS));
            publishSignal(subject, threadId, SIGNAL_CONCERN, concern, totalMs, triageStartMs, inTok, outTok, gpuLabel,
                    providerAttribution(result));
            return;
        }

        // Legacy: older tool-loop exhaustion produced a stand_aside with this
        // sentinel string. The new path throws ToolCallFailure (caught above
        // in runMullingPhase) so this branch is only hit if older callers
        // bypass the throw path. Kept for backwards-compat; new code paths
        // surface as failure signals.
        if (content.contains("unable to complete the request within the allowed number of tool calls")) {
            log.info("Agent {} exhausted tool loop for thread {} — standing aside (legacy)", properties.agentName(), threadId);
            publishSignal(subject, threadId, SIGNAL_STAND_ASIDE, content, totalMs, triageStartMs, inTok, outTok, gpuLabel);
            return;
        }

        publishSignal(subject, threadId, SIGNAL_AGREE, content, totalMs, triageStartMs, inTok, outTok, gpuLabel,
                providerAttribution(result));
    }

    /**
     * The concern a reply raises, or null when it raises none. A reply that starts
     * with {@code TOOL_GAP:} (the tool the agent lacks) or {@code CONCERN:} (what is
     * missing or wrong in the results) is a concern carrying the text after the
     * sentinel. Case-sensitive, like the other reply sentinels.
     */
    // Visible for testing
    static String concernIn(String content) {
        String reply = content == null ? "" : content.strip();
        for (String sentinel : List.of(TOOL_GAP_SENTINEL, CONCERN_SENTINEL)) {
            if (reply.startsWith(sentinel)) {
                return reply.substring(sentinel.length()).trim();
            }
        }
        return null;
    }

    /** True when the message is a review_ready that asks for concurrence (reviewMode=concur). */
    // Visible for testing
    boolean isConcurrenceRequest(String data) {
        try {
            var meta = mapper.readTree(data).path(FIELD_METADATA);
            return REVIEW_MODE_CONCUR.equals(meta.path(FIELD_REVIEW_MODE).asText(""));
        } catch (Exception e) {
            return false;
        }
    }

    private String formatThread(List<ThreadMessage> messages, String threadId) {
        var sb = new StringBuilder();
        sb.append("You are participating in a team discussion (thread ").append(threadId).append(").\n\n");

        // Prepend conversation context if this is a follow-up question
        var convContext = threadConversationContext.get(threadId);
        if (convContext != null && !convContext.isEmpty()) {
            sb.append(DiscussionOrchestrator.formatConversationContext(convContext));
        }

        sb.append("--- Thread conversation ---\n\n");

        for (var msg : messages) {
            String label = switch (msg.messageType) {
                case MSG_THREAD_START -> "User Question";
                case "advisory" -> "Advisory (" + msg.agentName + ")";
                case MSG_ADVISORY_READY -> "Evaluation Phase Started";
                case MSG_REVIEW_READY -> "Review Phase - Other Agents' Responses";
                case SIGNAL_AGREE, "contribution" -> "Response (" + msg.agentName + ")";
                case "concern" -> "Concern (" + msg.agentName + ")";
                case SIGNAL_STAND_ASIDE, "decline" -> "Stand Aside (" + msg.agentName + ")";
                case "proposal" -> "Proposal (" + msg.agentName + ")";
                case MSG_REPLY -> "User Reply";
                case "follow_up" -> "Facilitator Follow-up";
                case "synthesis" -> "Synthesis";
                default -> msg.messageType + " (" + msg.agentName + ")";
            };

            sb.append("[").append(label).append("]\n");
            if (!msg.content.isEmpty()) {
                sb.append(msg.content).append("\n");
            }
            sb.append("\n");
        }

        return sb.toString();
    }

    private void publishSignal(String originalSubject, String threadId, String signal, String content,
                               long inferenceMs, long inferenceStartMs,
                               long inputTokens, long outputTokens) {
        publishSignal(originalSubject, threadId, signal, content, inferenceMs, inferenceStartMs,
                inputTokens, outputTokens, GpuLabels.fromEndpoint(ollamaBaseUrl));
    }

    private void publishSignal(String originalSubject, String threadId, String signal, String content,
                               long inferenceMs, long inferenceStartMs,
                               long inputTokens, long outputTokens, String gpuLabel) {
        publishSignal(originalSubject, threadId, signal, content, inferenceMs, inferenceStartMs,
                inputTokens, outputTokens, gpuLabel, null);
    }

    private void publishSignal(String originalSubject, String threadId, String signal, String content,
                               long inferenceMs, long inferenceStartMs,
                               long inputTokens, long outputTokens, String gpuLabel,
                               Map<String, Object> extraMetadata) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            CrewScope scope = natsProvider.scope();
            String channel = scope.channelOf(originalSubject, "general");

            var metadata = new HashMap<String, Object>();
            metadata.put("signal", signal);
            if (SIGNAL_AGREE.equals(signal)) {
                metadata.put("toolsUsed", properties.enabledTools().orElse(List.of()));
            }
            metadata.put("role", properties.discuss().role());
            metadata.put("modelName", properties.model().model());
            metadata.put("gpuLabel", gpuLabel);
            metadata.put("inferenceMs", inferenceMs);
            metadata.put("inferenceStartMs", inferenceStartMs);
            metadata.put("inputTokens", inputTokens);
            metadata.put("outputTokens", outputTokens);
            metadata.put("totalTokens", inputTokens + outputTokens);
            if (extraMetadata != null) {
                metadata.putAll(extraMetadata);
            }

            // Spill large contributions to the artifact store (adds metadata.artifact
            // and publishes a reference for the materializer); small ones inline.
            String contentField = resolveContentField(conn, threadId, signal, content, metadata);

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, signal,
                    FIELD_CONTENT, contentField,
                    FIELD_CHANNEL, channel,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, metadata
            );

            String publishSubject = scope.discussSubject(channel, threadId);
            conn.publish(publishSubject, mapper.writeValueAsBytes(message));
            log.info("Published {} signal to {} for thread {}", signal, publishSubject, threadId);

        } catch (Exception e) {
            log.warn("Failed to publish {} signal: {}", signal, e.getMessage());
        }
    }

    /**
     * Decide the inline content for a published signal. Small content is inlined
     * (truncated as before). Large content is spilled to the artifact store: the
     * full bytes go to the Object Store, a structured reference is added to the
     * signal metadata and published for the materializer sidecar, and the inline
     * content becomes a short preview plus a marker pointing at the materialized
     * file. This is what stops bulk tool output being truncated on the bus.
     */
    // Package-private for unit testing (see DiscussionSubscriberHelpersTest).
    // Every contribution is inlined up to INLINE_TRUNCATE_CHARS (unchanged from
    // the historic behavior). A contribution over ARTIFACT_SPILL_THRESHOLD is
    // ALSO spilled whole to the artifact store and gets a marker appended to the
    // inline preview, so non-artifact consumers lose no context and the compute
    // tooler can read the full data from the materialized file.
    String resolveContentField(Connection conn, String threadId, String signal,
                               String content, Map<String, Object> metadata) {
        if (content == null) {
            return "";
        }
        if (content.length() <= ARTIFACT_SPILL_THRESHOLD) {
            return truncate(content, INLINE_TRUNCATE_CHARS);
        }
        Map<String, Object> ref = spillArtifact(conn, threadId, signal, content);
        if (ref == null) {
            return truncate(content, INLINE_TRUNCATE_CHARS);
        }
        metadata.put("artifact", ref);
        // Overflow: the FULL data is in the artifact, so post ONLY the reference -
        // never the truncated inline preview. A truncated preview pollutes the bus
        // with incomplete data and tempts consumers to answer from it (the compute
        // tooler tallied the preview; the coordinator under-reported lists). Artifact
        // -aware consumers read the file instead: the compute tooler via execute_code
        // over /artifacts/<key>, network-capable agents via the artifact read-ops
        // tools (head/tail/grep/count) on the key. See [[Measurement Integrity -
        // Grade Inflation and Fabrication]] and [[Agent Collaborative Artifact Store]].
        return ARTIFACT_MARKER_PREFIX + ref.get("key") + " bytes=" + ref.get("bytes")
                + " - the FULL data is in the file /artifacts/" + ref.get("key")
                + "; READ it (execute_code, or head/tail/grep/count over the artifact key) - "
                + "do NOT answer from this reference line, it carries no data]";
    }

    /**
     * Write a large contribution to the NATS Object Store and publish a reference
     * for the artifact-access materializer. Returns the reference map, or null on
     * any failure (the caller falls back to inline-truncated content).
     */
    private Map<String, Object> spillArtifact(Connection conn, String threadId, String signal, String content) {
        try {
            byte[] bytes = content.getBytes(StandardCharsets.UTF_8);
            CrewScope scope = natsProvider.scope();
            String key = DiscussionArtifacts.key(scope, threadId, properties.agentName(), signal,
                    UUID.randomUUID().toString());

            ensureArtifactBucket(conn);
            // Put the full byte[] (NATS computes the digest over it deterministically).
            // A streaming put(InputStream) was tried (heap optimization) but corrupted
            // larger objects - NATS digest mismatch on read-back - and gave no benefit at
            // our KB-scale artifact sizes; see [[Agent Collaborative Artifact Store]].
            conn.objectStore(DiscussionArtifacts.BUCKET).put(key, bytes);
            verifyWriteReadback(conn, key, bytes);

            var ref = new LinkedHashMap<String, Object>();
            ref.put("bucket", DiscussionArtifacts.BUCKET);
            ref.put("key", key);
            ref.put("bytes", bytes.length);
            ref.put("agent", properties.agentName());
            ref.put("tool", signal);
            ref.put("createdAt", Instant.now().toString());
            ref.put("preview", truncate(content, ARTIFACT_PREVIEW_CHARS));

            conn.publish(scope.artifactSubject(threadId),
                    mapper.writeValueAsBytes(Map.of("artifact", ref)));
            log.info("Spilled {} contribution ({} bytes) to artifact {}", signal, bytes.length, key);
            return ref;
        } catch (Exception e) {
            log.warn("Artifact spill failed for {} ({}): {} - falling back to inline",
                    signal, e.getClass().getSimpleName(), e.getMessage());
            return null;
        }
    }

    /**
     * Ensure the artifact bucket exists, once per process. create() is called
     * unconditionally (no check-then-create TOCTOU); an "already in use" error
     * (code 10058) from a concurrent or prior create is benign and ignored.
     */
    private void ensureArtifactBucket(Connection conn) throws Exception {
        if (artifactBucketEnsured) {
            return;
        }
        try {
            conn.objectStoreManagement().create(ObjectStoreConfiguration.builder(DiscussionArtifacts.BUCKET)
                    .ttl(Duration.ofHours(48))
                    .storageType(StorageType.File)
                    .build());
        } catch (JetStreamApiException e) {
            if (e.getApiErrorCode() != JSAPI_STREAM_NAME_IN_USE) {
                throw e;
            }
        }
        artifactBucketEnsured = true;
    }

    /**
     * DIAGNOSTIC (artifact-corruption hunt): immediately read the just-written object
     * back, in this same agent process, and log whether it is intact AT THE SOURCE.
     * This splits the problem: a SIZE/CONTENT mismatch or error here means the write
     * produced a bad object under load; an OK here means corruption appears later, on
     * the materializer's read path. Logs an ARTIFACT-DIAG line per spill. Remove once
     * the root cause is pinned. See [[Agent Collaborative Artifact Store]].
     */
    private void verifyWriteReadback(Connection conn, String key, byte[] expected) {
        try {
            long t0 = System.nanoTime();
            var bos = new java.io.ByteArrayOutputStream(expected.length);
            conn.objectStore(DiscussionArtifacts.BUCKET).get(key, bos);
            long ms = (System.nanoTime() - t0) / 1_000_000;
            byte[] back = bos.toByteArray();
            if (back.length != expected.length) {
                log.error("ARTIFACT-DIAG write/readback SIZE-MISMATCH key={} wrote={} readback={} readMs={}",
                        key, expected.length, back.length, ms);
            } else if (!java.util.Arrays.equals(back, expected)) {
                log.error("ARTIFACT-DIAG write/readback CONTENT-MISMATCH key={} bytes={} readMs={}",
                        key, expected.length, ms);
            } else {
                log.info("ARTIFACT-DIAG write/readback OK key={} bytes={} readMs={}", key, expected.length, ms);
            }
        } catch (Exception e) {
            log.error("ARTIFACT-DIAG write/readback ERROR key={} bytes={}: {}: {}",
                    key, expected.length, e.getClass().getSimpleName(), e.getMessage());
        }
    }

    /**
     * Check if this agent is excluded by the triage inner circle.
     * Reads innerCircle from the most recent advisory_ready metadata for this thread.
     * Returns true if excluded (caller should stand aside and return).
     * Returns false if included or if no innerCircle is present (backward compat).
     */
    // Visible for testing
    boolean isExcludedByInnerCircle(String data, String threadId) {
        try {
            var msgNode = mapper.readTree(data);
            if (!msgNode.has(FIELD_METADATA)) return false;
            var meta = msgNode.get(FIELD_METADATA);
            if (!meta.has(FIELD_INNER_CIRCLE)) return false;

            var innerCircleNode = meta.get(FIELD_INNER_CIRCLE);
            if (!innerCircleNode.isArray() || innerCircleNode.isEmpty()) return false;

            var innerCircle = new HashSet<String>();
            innerCircleNode.forEach(n -> innerCircle.add(n.asText()));

            if (innerCircle.contains(properties.agentName())) {
                log.info("Thread {} — {} is in subcommittee ({}), proceeding to evaluate",
                        threadId, properties.agentName(), innerCircle);
                return false;
            }

            log.debug("Thread {} — {} not in subcommittee ({}), skipping silently",
                    threadId, properties.agentName(), innerCircle);
            // Don't publish stand_aside — the triage_result message already shows
            // who was selected. Publishing 15+ stand_asides per thread is noise.
            return true;

        } catch (Exception e) {
            log.debug("Failed to check innerCircle for thread {}: {}", threadId, e.getMessage());
            return false; // On error, don't exclude — let the agent participate.
        }
    }

    /**
     * True when the coordinator EXPLICITLY selected this agent: the message carries a
     * NON-EMPTY innerCircle that contains this agent. Such an agent must NOT re-veto
     * itself in triage - the coordinator's reasoning-select (a stronger model over the
     * resumes) already judged it relevant, so a NOTHING_TO_ADD from the lightweight
     * triage model is a false negative; it gathers instead. Distinct from the no-inner
     * circle case (open broadcast), where the triage relevance gate still applies.
     */
    // Visible for testing
    boolean isExplicitlySelected(String data) {
        try {
            var meta = mapper.readTree(data).path(FIELD_METADATA);
            var circle = meta.path(FIELD_INNER_CIRCLE);
            if (!circle.isArray() || circle.isEmpty()) {
                return false;
            }
            for (var n : circle) {
                if (properties.agentName().equals(n.asText())) {
                    return true;
                }
            }
            return false;
        } catch (Exception e) {
            return false;
        }
    }

    private void cleanupOldThreads() {
        if (processedMessages.size() > MAX_PROCESSED_MESSAGES) {
            processedMessages.clear();
        }
        if (threadContext.size() > MAX_THREAD_CONTEXT_ENTRIES) {
            threadContext.clear();
            threadTechnologies.clear();
            threadConversationContext.clear();
            threadSelectedSkills.clear();
            threadQuestions.clear();
            threadEndWakers.clear();
        }
    }

    private static String truncate(String text, int maxLen) {
        if (text == null) return "";
        return text.length() > maxLen ? text.substring(0, maxLen) + "..." : text;
    }

    // Visible for testing
    boolean isUsingJetStream() {
        return jetstreamConsumer != null && !jetstreamConsumer.isEmpty();
    }

    /**
     * Test entry-point: feeds a raw NATS message through the same parse and
     * metadata-extraction path as the real dispatcher. Used by skills tests to
     * simulate receiving an advisory_ready without spinning up a real NATS server.
     */
    // Visible for testing
    void handleMessageForTest(String subject, String data) {
        handleMessage(subject, data);
    }

    /**
     * Test entry-point: exercises the skill-injection path of runMullingInference
     * without the full evaluateAndRespond lifecycle (no triage, no heartbeat,
     * no signal publish). The ChatService mock captures the message the LLM
     * would receive so tests can assert on skill body injection.
     */
    // Visible for testing
    ChatService.ChatResult runMullingInferenceForTest(String conversation, String threadId) {
        return runMullingInference(conversation, threadId, ai.kubemoot.agent.provider.CapacityWait.NONE);
    }

    private record ThreadMessage(String agentName, String messageType, String content, Instant timestamp) {}
}
