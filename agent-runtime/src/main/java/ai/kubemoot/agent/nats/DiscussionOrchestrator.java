package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.config.PhaseBudgetDefaults;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import ai.kubemoot.agent.util.GpuLabels;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicReference;

/**
 * Signal-driven discussion facilitator with early phase transitions.
 *
 * Per-thread phase machine: SUBMITTED, ADVISORY, EVALUATING, REVIEW, SYNTHESIZING,
 * CLOSED (plus PAUSED). The authoritative state graph - auto edges (the periodic
 * checker) and event edges (NATS message handlers) - is documented on
 * {@link #buildAutoTransitions()}.
 *
 * Phase 1 (ADVISORY): Generate advisory inline via LLM (~1-2s).
 * Phase 2 (EVALUATING): Toolers respond with tools. Transitions early when
 *          signals settle (quiet after the last signal), or on per-agent deadline.
 * Phase 3 (REVIEW): Skipped for single-agree discussions (no cross-checking needed).
 *          Otherwise waits for its roster to settle.
 * Phase 4 (SYNTHESIZING): LLM synthesizes the final answer. Always runs - tooler
 *          responses contain internal formatting that must be cleaned for end users.
 *
 * Only activated in coordinator agents (KUBEMOOT_DISCUSS_COORDINATOR=true).
 */
@ApplicationScoped
public class DiscussionOrchestrator {

    private static final Logger log = LoggerFactory.getLogger(DiscussionOrchestrator.class);
    private static final ObjectMapper mapper = new ObjectMapper();

    // JSON field name constants (used in NATS message construction/parsing)
    private static final String FIELD_MESSAGE_ID = "messageId";
    private static final String FIELD_THREAD_ID = "threadId";
    private static final String FIELD_AGENT_NAME = "agentName";
    private static final String FIELD_MESSAGE_TYPE = "messageType";
    private static final String FIELD_CONTENT = "content";
    private static final String FIELD_CHANNEL = "channel";
    private static final String FIELD_TIMESTAMP = "timestamp";
    private static final String FIELD_METADATA = "metadata";

    // Message type constants
    private static final String MSG_ADVISORY = "advisory";
    private static final String MSG_STAND_ASIDE = "stand_aside";
    /**
     * First-class consensus signal for infrastructure failure during agent
     * evaluation (MCP tool timeout exhaustion, model OOM, tool-loop iteration
     * limit). Coordinator settle logic treats failure like stand_aside (don't
     * block transitions) but counts it separately so GapDetector can
     * distinguish missing-tooler gaps from broken-infrastructure gaps.
     */
    private static final String MSG_FAILURE = "failure";
    /** An agent started waiting for GPU capacity. */
    private static final String MSG_WAITING = "waiting";
    /** Stand-aside reason: every GPU that could hold the model stayed busy. */
    static final String REASON_GPU_BUSY = ai.kubemoot.agent.provider.NoFitException.REASON_GPU_BUSY;
    /** Stand-aside reason: no GPU can ever hold the model. */
    static final String REASON_MODEL_TOO_LARGE = ai.kubemoot.agent.provider.NoFitException.REASON_MODEL_TOO_LARGE;
    /** Stand-aside reason: the prompt is larger than every GPU's context window for the model. */
    static final String REASON_PROMPT_TOO_LARGE = ai.kubemoot.agent.provider.NoFitException.REASON_PROMPT_TOO_LARGE;
    /** The stand-aside reasons that mean the cluster, not the crew, kept an agent out. */
    private static final Set<String> CAPACITY_REASONS =
            Set.of(REASON_GPU_BUSY, REASON_MODEL_TOO_LARGE, REASON_PROMPT_TOO_LARGE);
    private static final String MSG_THREAD_START = "thread_start";
    private static final String MSG_ADVISORY_READY = "advisory_ready";
    private static final String MSG_REVIEW_READY = "review_ready";
    private static final String MSG_REVIEW_DECISION = "review_decision";
    private static final String FIELD_REVIEW_MODE = "reviewMode";
    private static final String TIER_REASONING = "reasoning";
    private static final String TIER_FAST = "fast";
    private static final String MSG_SYNTHESIS = "synthesis";
    private static final String MSG_THREAD_CLOSE = "thread_close";
    private static final String MSG_STOP_REQUESTED = "stop_requested";
    private static final String MSG_THREAD_PAUSE = "thread_pause";
    private static final String MSG_THREAD_RESUME = "thread_resume";

    // Orphan-artifact reaper (GC Layer 3). Conservative by design: the grace window is
    // far longer than any discussion, so a live thread's artifacts are never reaped; the
    // reaper only repairs leaks (failed lifecycle delete, coordinator crash). The 48h
    // bucket TTL backstops anything this misses.
    private static final int ORPHAN_REAP_INTERVAL_MINUTES = 30;
    private static final Duration ORPHAN_GRACE = Duration.ofHours(2);
    private static final String MSG_WAKING = "waking";
    private static final String MSG_READY = "ready";

    // Truncation caps (chars), named by purpose. Different purposes keep distinct
    // constants even when the value coincides.
    // conversationId prefix shown in log lines.
    private static final int LOG_ID_PREVIEW_CHARS = 12;
    // user-query snippet shown in a log line.
    private static final int LOG_QUERY_PREVIEW_CHARS = 100;
    // generic content/brief/triage/wisdom snippet shown in logs and the advisory summary.
    private static final int LOG_PREVIEW_CHARS = 200;
    // advisory "wisdom" stored in the advisory message content field.
    private static final int ADVISORY_CONTENT_CHARS = 2000;
    // per-agent agree/concern snippet in the review-phase summary.
    private static final int REVIEW_SNIPPET_CHARS = 1000;
    // per-agent agree snippet in the synthesis-fallback response.
    private static final int FALLBACK_SNIPPET_CHARS = 2000;
    // prior-turn response echoed into advisory/coordinator prompt context.
    private static final int CONTEXT_RESPONSE_CHARS = 300;
    // prior-turn response echoed into the subcommittee-selection prompt.
    private static final int SELECTION_CONTEXT_RESPONSE_CHARS = 200;
    // content cap when republishing a message to a thread (bus hygiene for
    // intra-discussion findings; the synthesis is exempt - see below).
    private static final int PUBLISHED_CONTENT_CHARS = 5000;
    // The synthesis is the terminal answer streamed to the user, who is NOT
    // artifact-aware. Unlike a finding it is published WHOLE (a generous cap well
    // under the NATS payload limit) so the user always sees the complete answer,
    // and any internal artifact-spill marker is stripped so the user is never
    // handed an /artifacts/<key> path they cannot open. See [[Measurement
    // Integrity - Grade Inflation and Fabrication]].
    private static final int SYNTHESIS_CONTENT_CHARS = 200_000;
    private static final java.util.regex.Pattern ARTIFACT_SPILL_MARKER =
            java.util.regex.Pattern.compile("\\s*\\[ARTIFACT key=[^\\]]*\\]");
    // Absolute floor of a table's names that must appear in a draft for the synthesis
    // completeness contract to treat the draft as enumerating that table. Below this the
    // draft is a count/free-form answer, not an inventory. See enumeratedTable().
    private static final int MIN_ENUMERATED_PRESENT = 5;
    // turn response persisted to the conversation-history KV record.
    private static final int PERSISTED_RESPONSE_CHARS = 500;
    // resume content snippet shown in a parse-failure debug log.
    private static final int RESUME_PARSE_LOG_CHARS = 50;

    // Generous deadline (ms) granted to an agent waking from scale-to-zero.
    private static final long WAKING_DEADLINE_MS = 60_000;
    // Age (seconds) after which a CLOSED thread is evicted from the in-memory map.
    private static final long CLOSED_THREAD_EVICT_SECONDS = 600;
    // Age (seconds) after which a non-CLOSED zombie thread is force-closed and evicted.
    private static final long ZOMBIE_THREAD_EVICT_SECONDS = 1800;

    // Closes a quoted user-query value in a prompt builder: literal `"` then a blank line.
    private static final String QUOTE_BLANK_LINE = "\"\n\n";

    // JSON field name for technologies in metadata
    private static final String FIELD_TECHNOLOGIES = "technologies";

    // JSON field names for the new combined select+brief LLM output
    private static final String FIELD_SELECTED = "selected";
    private static final String FIELD_BRIEF = "brief";

    // Additional field name constants (NATS message metadata keys)
    private static final String FIELD_USER_QUERY = "userQuery";
    private static final String FIELD_PRIMARY_CHANNEL = "primaryChannel";
    private static final String FIELD_CONVERSATION_CONTEXT = "conversationContext";
    private static final String FIELD_INNER_CIRCLE = "innerCircle";
    private static final String FIELD_OVERALL_CONFIDENCE = "overallConfidence";
    private static final String FIELD_INFERENCE_MS = "inferenceMs";
    private static final String FIELD_INPUT_TOKENS = "inputTokens";
    private static final String FIELD_OUTPUT_TOKENS = "outputTokens";

    // Channel constants
    private static final String CHANNEL_BROADCAST = "broadcast";
    private static final String CHANNEL_GENERAL = "general";

    // Metadata key constants (NATS message metadata)
    private static final String FIELD_GPU_LABEL = "gpuLabel";
    private static final String FIELD_MODEL_NAME = "modelName";
    // Token-count map keys (input/output) used in the synthesis token report
    private static final String TOKENS_IN = "in";
    private static final String TOKENS_OUT = "out";

    // Conversation context field constants
    private static final String FIELD_QUERY = "query";
    private static final String FIELD_RESPONSE = "response";
    private static final String FIELD_NAME = "name";
    private static final String FIELD_REASON = "reason";
    private static final String FIELD_AGENTS = "agents";
    private static final String FIELD_WISDOM = "wisdom";
    private static final String FIELD_LAYERS = "layers";
    private static final String FIELD_CONFIDENCE = "confidence";
    private static final String FIELD_ROLE = "role";
    private static final String FIELD_DESCRIPTION = "description";
    private static final String FIELD_TOOLS = "tools";
    private static final String ROLE_TOOLER = "tooler";
    private static final String ROLE_ANALYST = "analyst";
    private static final String ROLE_RESEARCHER = "researcher";
    private static final String KIND_SKILL = "skill";
    private static final String FIELD_KIND = "kind";
    private static final String FIELD_SKILLS = "skills";
    private static final String FIELD_SELECTED_SKILLS = "selectedSkills";
    private static final String SKILLS_CATALOG_HEADER = "Skills available:";

    // NATS subject prefix

    // Configurable timing constants for phase transitions.
    // Defaults tuned for mixed GPU cluster: RTX 5090 (~20s tool-calling) + RTX 4090 (~60s tool-calling).
    private final int settleSeconds;
    private final int minEvalSeconds;
    private final int minReviewSeconds;

    /** Declarative phase machine: phase -> how the checker advances out of it. */
    private final Map<Phase, PhaseRule> autoTransitions;

    private final NatsConnectionProvider natsProvider;
    private final AgentProperties properties;
    private final ChatService chatService;
    private final DiscussionMetrics metrics;
    private final LatencyTracker latencyTracker;
    private final ResumeSearchClient resumeSearchClient;
    private final boolean coordinator;
    // When the crew declares any analyst-role agent, the coordinator must run the
    // REVIEW phase (where analysts self-select) rather than taking the single-agree
    // fast path that skips it. Set by the operator (KUBEMOOT_DISCUSS_HAS_ANALYSTS).
    private final boolean hasAnalysts;
    // Whether the coordinator may answer a turn directly (empty tooler selection).
    // Off for single-specialist crews (the fitness judge) so the coordinator always
    // delegates to its specialist. Set via KUBEMOOT_DISCUSS_ANSWER_DIRECTLY.
    private final boolean answerDirectlyEnabled;
    // Whether the crew declares the review decision (KUBEMOOT_DISCUSS_REVIEW_DECISION):
    // one coordinator call after EVALUATING that chooses the shape of the review.
    private final boolean reviewDecisionEnabled;
    // The model tier of that call: "fast" (the triage model) or "reasoning" (the main model).
    private final String reviewDecisionTier;
    private final int advisoryTimeoutSeconds;
    private final int evaluationTimeoutSeconds;
    private final int reviewTimeoutSeconds;
    private final int synthesisTimeoutSeconds;
    private final List<String> channels;
    private final long evalGraceMs;

    private final ConcurrentHashMap<String, ThreadState> threads = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<String, CompletableFuture<String>> pendingResults = new ConcurrentHashMap<>();

    // Conversation history: durable via NATS KV, with in-memory read-through cache.
    // Allows follow-up questions to reference prior answers (e.g., "How many of these rigs have GPUs?")
    private final ConcurrentHashMap<String, ConversationHistory> conversationCache = new ConcurrentHashMap<>();
    private final int maxConversationTurns;
    private final int conversationTtlMinutes;
    private final String conversationKvBucket;

    private final ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(2);
    // Separate executor for LLM calls so they never starve the scheduler's
    // periodic tasks (checkPhaseTransitions, cleanupOldThreads).
    private final ExecutorService llmExecutor = Executors.newCachedThreadPool();

    // System prompt loaded from file (composed by operator from PromptModules)
    private volatile String systemPromptCache;

    // Crew resumes loaded from ConfigMap-mounted file (generated by operator).
    // The mount location is resolved from config (key below) rather than a
    // hardcoded path literal so it is deployment-overridable; the default
    // preserves prior behavior.
    private static final String CREW_RESUMES_PATH_KEY = "kubemoot.discuss.crew-resumes-path";
    // The path IS customizable via CREW_RESUMES_PATH_KEY (above); this is only the
    // default. The literal default is unavoidable, so the S1075 "get from a parameter"
    // intent is already satisfied.
    private static final String DEFAULT_CREW_RESUMES_PATH = "/app/config/crew-resumes.json"; // NOSONAR S1075
    private final String crewResumesPath = org.eclipse.microprofile.config.ConfigProvider.getConfig()
            .getOptionalValue(CREW_RESUMES_PATH_KEY, String.class)
            .orElse(DEFAULT_CREW_RESUMES_PATH);
    // NATS KV bucket the operator keeps current with each crew's full capability
    // catalog (key = crew name). The coordinator reads it when no resumes file is
    // mounted - the grounding source for reasoning-based subcommittee selection.
    private static final String CREW_RESUMES_KV_BUCKET = "kubemoot_crew_resumes";
    private volatile String crewResumesCache;
    // Where the cached catalog came from: a mounted file is read once; a KV catalog is
    // re-checked by revision at each discussion so an added agent or skill is seen.
    private volatile boolean crewResumesFromFile;
    private volatile long crewResumesRevision = -1;
    // One reload at a time: the catalog, its revision, and the known agent and skill
    // names change together, so concurrent discussions never pair one with another's.
    private final Object crewResumesLock = new Object();
    private final AtomicReference<List<String>> knownAgentNames = new AtomicReference<>(List.of());
    private final AtomicReference<List<String>> knownSkillNames = new AtomicReference<>(List.of());

    /** Package-private: inject known agent names for unit tests without file I/O. */
    void loadKnownAgentNamesForTest(List<String> names) {
        knownAgentNames.set(List.copyOf(names));
    }

    /** Package-private: inject known skill names for unit tests without file I/O. */
    void loadKnownSkillNamesForTest(List<String> names) {
        knownSkillNames.set(List.copyOf(names));
    }

    /** Package-private: install a crew capability catalog for unit tests without a file or NATS. */
    void loadCrewResumesForTest(String rawResumes) {
        synchronized (crewResumesLock) {
            crewResumesCache = rawResumes;
            crewResumesFromFile = true;
            extractAgentNamesFromResumes(rawResumes);
        }
    }

    /** Package-private: register a pending result future so a unit test can observe completion. */
    CompletableFuture<String> registerPendingForTest(String threadId) {
        return pendingResults.computeIfAbsent(threadId, k -> new CompletableFuture<>());
    }


    public DiscussionOrchestrator(
            NatsConnectionProvider natsProvider,
            AgentProperties properties,
            ChatService chatService,
            DiscussionMetrics metrics,
            LatencyTracker latencyTracker,
            ResumeSearchClient resumeSearchClient
    ) {
        this.natsProvider = natsProvider;
        this.properties = properties;
        this.chatService = chatService;
        this.metrics = metrics;
        this.latencyTracker = latencyTracker;
        this.resumeSearchClient = resumeSearchClient;
        this.coordinator = properties.discuss().coordinator();
        this.hasAnalysts = properties.discuss().hasAnalysts();
        this.answerDirectlyEnabled = properties.discuss().answerDirectly();
        this.reviewDecisionEnabled = properties.discuss().reviewDecision();
        this.reviewDecisionTier = properties.discuss().reviewDecisionTier();
        var discuss = properties.discuss();
        this.advisoryTimeoutSeconds = PhaseBudgetDefaults.orDefault(discuss.advisoryTimeoutSeconds(),
                PhaseBudgetDefaults.ADVISORY_SECONDS);
        this.evaluationTimeoutSeconds = PhaseBudgetDefaults.orDefault(discuss.evaluationTimeoutSeconds(),
                PhaseBudgetDefaults.EVALUATION_SECONDS);
        this.reviewTimeoutSeconds = PhaseBudgetDefaults.orDefault(discuss.reviewTimeoutSeconds(),
                PhaseBudgetDefaults.REVIEW_SECONDS);
        this.synthesisTimeoutSeconds = PhaseBudgetDefaults.orDefault(discuss.synthesisTimeoutSeconds(),
                PhaseBudgetDefaults.SYNTHESIS_SECONDS);
        this.settleSeconds = properties.discuss().settleSeconds();
        this.minEvalSeconds = properties.discuss().minEvalSeconds();
        this.minReviewSeconds = properties.discuss().minReviewSeconds();
        this.evalGraceMs = properties.discuss().evalGraceSeconds() * 1000L;
        this.maxConversationTurns = properties.discuss().conversationMaxTurns();
        this.conversationTtlMinutes = properties.discuss().conversationTtlMinutes();
        this.conversationKvBucket = properties.discuss().conversationKvBucket();

        String channelsConfig = properties.discuss().channels().orElse("");
        if (!channelsConfig.isEmpty()) {
            this.channels = Arrays.asList(channelsConfig.split(","));
        } else {
            this.channels = List.of();
        }

        this.autoTransitions = buildAutoTransitions();
    }

    private volatile boolean subscribed = false;

    public boolean isEnabled() {
        return coordinator && !channels.isEmpty() && natsProvider.isAvailable();
    }

    void onStart(@Observes StartupEvent event) {
        if (!coordinator) {
            log.info("Not a coordinator — discussion orchestrator disabled");
            return;
        }

        if (channels.isEmpty() || !natsProvider.isConfigured()) {
            log.info("Discussion orchestrator disabled (no channels or NATS not configured)");
            return;
        }

        if (!trySubscribe()) {
            log.info("NATS not yet available — orchestrator will retry subscription every 30s");
            scheduler.scheduleAtFixedRate(() -> {
                if (!subscribed) trySubscribe();
            }, 30, 30, TimeUnit.SECONDS);
        }
    }

    private boolean trySubscribe() {
        Connection conn = natsProvider.getConnection();
        if (conn == null) return false;

        Dispatcher dispatcher = conn.createDispatcher();

        // Subscribe to this namespace's crew-scoped discussion subjects
        CrewScope scope = natsProvider.scope();
        String sub = scope.discussWildcard();
        dispatcher.subscribe(sub, msg -> {
            try {
                handleMessage(new String(msg.getData()));
            } catch (Exception e) {
                log.debug("Error handling orchestrator message: {}", e.getMessage());
            }
        });

        // Subscribe to lifecycle signals (waking/ready) on a separate subject tree.
        // These are published outside KUBEMOOT_DISCUSS JetStream to prevent bootstrap
        // storms where waking signals create consumer lag that wakes more agents.
        String lifecycleSub = scope.lifecycleWildcard();
        dispatcher.subscribe(lifecycleSub, msg -> {
            try {
                handleMessage(new String(msg.getData()));
            } catch (Exception e) {
                log.debug("Error handling lifecycle message: {}", e.getMessage());
            }
        });
        log.info("Subscribed to lifecycle signals: {}", lifecycleSub);

        // Phase transition checker (1s interval for responsive signal-driven transitions)
        scheduler.scheduleAtFixedRate(this::checkPhaseTransitions, 1, 1, TimeUnit.SECONDS);

        // Periodic cleanup of old threads
        scheduler.scheduleAtFixedRate(this::cleanupOldThreads, 1, 1, TimeUnit.MINUTES);

        // Periodic orphan-artifact reaper (GC Layer 3) - repairs leaked artifacts whose
        // thread is gone. Coordinator-only (this whole method is) and crew-scoped.
        scheduler.scheduleAtFixedRate(this::reapOrphanArtifacts,
                ORPHAN_REAP_INTERVAL_MINUTES, ORPHAN_REAP_INTERVAL_MINUTES, TimeUnit.MINUTES);

        subscribed = true;
        log.info("Discussion orchestrator active: advisory={}s, review={}s, synthesis={}s, minEval={}s, settle={}s, channels={}",
                advisoryTimeoutSeconds, reviewTimeoutSeconds, synthesisTimeoutSeconds, minEvalSeconds, settleSeconds, channels);
        return true;
    }

    /**
     * Synchronous bridge for the chat API.
     * Starts a thread, waits for phased synthesis or timeout.
     * Links threads via conversationId for follow-up context.
     */
    public record OrchestrateResult(String threadId, String response) {}

    public OrchestrateResult orchestrate(String userQuery, String conversationId, String crew) {
        if (!isEnabled()) return null;

        Connection conn = natsProvider.getConnection();
        if (conn == null) return null;

        String threadId = UUID.randomUUID().toString();
        String primaryChannel = CHANNEL_GENERAL;

        log.info("Starting discussion thread {} (crew={}, conversation={}) for: {}",
                threadId, crew, truncate(conversationId, LOG_ID_PREVIEW_CHARS), truncate(userQuery, LOG_QUERY_PREVIEW_CHARS));

        // The watchdog (forceCloseIfOverCeiling) completes the future at the hard
        // ceiling, so this future.get budget is only the ultimate fallback - set it
        // just above the ceiling, not the old 30-minute wait that let a stuck
        // discussion block the request pump. See [[Discussion Request Pump Can Wedge a Crew]].
        int totalBudget = Math.toIntExact(hardCeilingSeconds() + 60);

        try {
            var state = initThreadState(threadId, userQuery, primaryChannel, conversationId, crew);
            var conversationContext = buildConversationContext(conversationId);
            var metadata = buildThreadStartMetadata(userQuery, primaryChannel, conversationId, conversationContext, crew, threadId);

            var threadStartPayload = mapper.writeValueAsBytes(Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, MSG_THREAD_START,
                    FIELD_CONTENT, userQuery,
                    FIELD_CHANNEL, primaryChannel,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, metadata
            ));

            // Primary path: coordinator reasoning call (select+brief) in transitionToEvaluating.
            // Similarity pre-filter is demoted to a fallback within transitionToEvaluating.
            publishBroadcastFallback(conn, crew, threadId, threadStartPayload, state);

            var future = pendingResults.computeIfAbsent(threadId, k -> new CompletableFuture<>());
            String result = future.get(totalBudget, TimeUnit.SECONDS);

            if (result != null && conversationId != null) {
                recordConversationTurn(conversationId, userQuery, result, threadId);
            }

            return new OrchestrateResult(threadId, result);

        } catch (TimeoutException e) {
            log.info("Thread {} timed out waiting for synthesis (budget={}s)", threadId, totalBudget);
            metrics.threadTimedOut();
            pendingResults.remove(threadId);
            return new OrchestrateResult(threadId, null);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.warn("Discussion orchestration failed for thread {}: {}", threadId, e.getMessage());
            pendingResults.remove(threadId);
            return null;
        } catch (Exception e) {
            log.warn("Discussion orchestration failed for thread {}: {}", threadId, e.getMessage());
            pendingResults.remove(threadId);
            return null;
        }
    }

    private ThreadState initThreadState(String threadId, String userQuery, String primaryChannel,
                                         String conversationId, String crew) {
        var state = new ThreadState(threadId);
        state.userQuery = userQuery;
        state.primaryChannel = primaryChannel;
        state.conversationId = conversationId;
        state.crew = crew;
        threads.put(threadId, state);
        metrics.threadStarted();
        return state;
    }

    // Visible for testing
    Map<String, Object> buildThreadStartMetadata(String userQuery, String primaryChannel,
                                                          String conversationId, List<?> conversationContext,
                                                          String crew, String threadId) {
        var metadata = new HashMap<String, Object>();
        metadata.put(FIELD_USER_QUERY, userQuery);
        metadata.put(FIELD_PRIMARY_CHANNEL, primaryChannel);
        if (conversationId != null) {
            metadata.put("conversationId", conversationId);
        }
        if (!conversationContext.isEmpty()) {
            metadata.put(FIELD_CONVERSATION_CONTEXT, conversationContext);
            log.info("Thread {} includes {} prior turns from conversation {}",
                    threadId, conversationContext.size(), truncate(conversationId, LOG_ID_PREVIEW_CHARS));
        }
        if (crew != null && !crew.isEmpty()) {
            metadata.put("crew", crew);
        }
        // Crew chart version (provenance): which crew VERSION produced this thread.
        // Absent for hand-applied crews with no chart version.
        properties.crewVersion()
                .filter(v -> !v.isBlank())
                .ifPresent(v -> metadata.put("crewVersion", v));
        return metadata;
    }

    private void publishBroadcastFallback(Connection conn, String crew, String threadId,
                                           byte[] threadStartPayload, ThreadState state) throws Exception {
        String broadcastSubject = broadcastSubject(crew, threadId);
        conn.publish(broadcastSubject, threadStartPayload);
        conn.flush(Duration.ofSeconds(2));

        state.phase = Phase.ADVISORY;
        state.phaseStarted = Instant.now();
        log.info("Thread {} → ADVISORY phase (reasoning select+brief will run in transitionToEvaluating)", threadId);
    }

    private void handleMessage(String data) {
        try {
            var msg = mapper.readTree(data);
            String threadId = msg.has(FIELD_THREAD_ID) ? msg.get(FIELD_THREAD_ID).asText() : "";
            String agentName = msg.has(FIELD_AGENT_NAME) ? msg.get(FIELD_AGENT_NAME).asText() : "";
            String messageType = msg.has(FIELD_MESSAGE_TYPE) ? msg.get(FIELD_MESSAGE_TYPE).asText() : "";
            String content = msg.has(FIELD_CONTENT) ? msg.get(FIELD_CONTENT).asText() : "";
            String channel = msg.has(FIELD_CHANNEL) ? msg.get(FIELD_CHANNEL).asText() : CHANNEL_GENERAL;

            // Waking/ready signals have empty threadId — apply to all active threads
            if (threadId.isEmpty()) {
                handleThreadlessSignal(agentName, messageType);
                return;
            }

            // Skip messages we published ourselves that would otherwise echo back.
            if (isOwnControlEcho(agentName, messageType)) return;

            var state = threads.computeIfAbsent(threadId, k -> new ThreadState(threadId));

            if (isDuplicateMessage(state, msg)) return;

            var messageRecord = new MessageRecord(agentName, messageType, content, channel, Instant.now());
            state.messages.add(messageRecord);

            trackThreadStart(state, msg, messageType, channel);

            // External dashboard controls (stop / pause / resume) short-circuit.
            if (handleExternalControl(state, threadId, agentName, messageType)) return;

            // Track advisory arrival (published by coordinator's inline advisory generation)
            trackAdvisoryMetadata(state, msg, messageType, content);

            // Track agent signals during EVALUATING and REVIEW phases
            if (!properties.agentName().equals(agentName)) {
                handleAgentSignal(state, agentName, messageType, content, msg);
            }

            // Handle human reply on closed thread — reopen to EVALUATING
            handleThreadReopen(state, threadId, messageType, agentName);

        } catch (Exception e) {
            log.debug("Failed to parse orchestrator message: {}", e.getMessage());
        }
    }

    /** A threadless waking/ready signal from another agent applies to all active threads. */
    private void handleThreadlessSignal(String agentName, String messageType) {
        if (!properties.agentName().equals(agentName)
                && (MSG_WAKING.equals(messageType) || MSG_READY.equals(messageType))) {
            handleWakeUpSignal(agentName, messageType);
        }
    }

    /** The control messages the coordinator publishes and ignores when they echo back. */
    private static final Set<String> OWN_CONTROL_MESSAGES =
            Set.of(MSG_THREAD_CLOSE, MSG_ADVISORY_READY, MSG_REVIEW_READY, MSG_REVIEW_DECISION);

    /** True for an echo of a control message this coordinator published. */
    // Visible for testing
    boolean isOwnControlEcho(String agentName, String messageType) {
        return properties.agentName().equals(agentName) && OWN_CONTROL_MESSAGES.contains(messageType);
    }

    /** Dedup by messageId; an already-seen id is a redelivery to drop. */
    private boolean isDuplicateMessage(ThreadState state, JsonNode msg) {
        String messageId = msg.has(FIELD_MESSAGE_ID) ? msg.get(FIELD_MESSAGE_ID).asText() : "";
        return !messageId.isEmpty() && !state.seenMessageIds.add(messageId);
    }

    /** Capture the user query + primary channel from the first thread_start. */
    private void trackThreadStart(ThreadState state, JsonNode msg, String messageType, String channel) {
        if (MSG_THREAD_START.equals(messageType) && state.userQuery == null) {
            state.userQuery = extractUserQuery(msg);
            state.primaryChannel = channel.equals(CHANNEL_BROADCAST) ? CHANNEL_GENERAL : channel;
        }
    }

    /**
     * Handle the external dashboard controls (stop / pause / resume) from another
     * agent. Returns true when the message was a control message (caller stops
     * further processing), false otherwise. Each control is a dedicated handler per
     * the dispatch-by-type rule.
     */
    private boolean handleExternalControl(ThreadState state, String threadId, String agentName,
            String messageType) {
        if (properties.agentName().equals(agentName)) return false;
        switch (messageType) {
            case MSG_STOP_REQUESTED -> handleStopRequest(state, threadId, agentName);
            case MSG_THREAD_PAUSE -> handlePauseRequest(state, threadId, agentName);
            case MSG_THREAD_RESUME -> handleResumeRequest(state, threadId, agentName);
            default -> {
                return false;
            }
        }
        return true;
    }

    /** A stop is honored only before the thread has already reached synthesis/closed. */
    static boolean canForceSynthesis(Phase phase) {
        return phase != Phase.SYNTHESIZING && phase != Phase.CLOSED;
    }

    /**
     * A pause is honored only from a live phase: not already paused, closed, or
     * synthesizing, and not while the review decision call is running (it lasts
     * seconds and advances on its own completion, not on the phase checker).
     */
    static boolean canPause(Phase phase) {
        return phase != Phase.PAUSED && phase != Phase.CLOSED && phase != Phase.SYNTHESIZING
                && phase != Phase.DECIDING;
    }

    /**
     * The phase a resume should restore, or null if the thread is not paused (resume
     * is a no-op). Restores the saved pre-pause phase, defaulting to EVALUATING.
     */
    static Phase resumeTarget(Phase current, Phase beforePause) {
        if (current != Phase.PAUSED) return null;
        return beforePause != null ? beforePause : Phase.EVALUATING;
    }

    /** Dashboard "Stop": force synthesis on accumulated signals, unless already past it. */
    // Visible for testing
    void handleStopRequest(ThreadState state, String threadId, String agentName) {
        // Under the thread's lock, so a stop and the end of DECIDING cannot both move the phase.
        synchronized (state) {
            if (canForceSynthesis(state.phase)) {
                log.info("Thread {} stop requested by {} - forcing synthesis from phase {}",
                        threadId, agentName, state.phase);
                transitionToSynthesis(state);
            }
        }
    }

    /** Dashboard "Pause": save the current phase; the phase-check skips PAUSED threads. */
    private void handlePauseRequest(ThreadState state, String threadId, String agentName) {
        if (canPause(state.phase)) {
            state.phaseBeforePause = state.phase;
            state.phase = Phase.PAUSED;
            log.info("Thread {} paused by {} — saved phase {}", threadId, agentName,
                    state.phaseBeforePause);
        }
    }

    /** Dashboard "Resume": restore the saved phase (default EVALUATING) and reset the clock. */
    private void handleResumeRequest(ThreadState state, String threadId, String agentName) {
        Phase restored = resumeTarget(state.phase, state.phaseBeforePause);
        if (restored != null) {
            state.phase = restored;
            state.phaseStarted = Instant.now();
            state.phaseBeforePause = null;
            log.info("Thread {} resumed by {} → {}", threadId, agentName, restored);
        }
    }

    /**
     * Track advisory metadata (technologies list) when an advisory message arrives.
     */
    private void trackAdvisoryMetadata(ThreadState state, JsonNode msg,
                                        String messageType, String content) {
        if (!MSG_ADVISORY.equals(messageType)) return;
        state.advisoryContent = content;
        if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has(FIELD_TECHNOLOGIES)) {
            var techList = new ArrayList<String>();
            msg.get(FIELD_METADATA).get(FIELD_TECHNOLOGIES).forEach(n -> techList.add(n.asText()));
            state.advisoryTechnologies = new CopyOnWriteArrayList<>(techList);
        }
    }

    /**
     * Dispatch an agent signal to the appropriate handler: triaging, evaluating,
     * heartbeat, agree/contribution, concern, stand_aside/decline, or block.
     */
    // Visible for testing
    void handleAgentSignal(ThreadState state, String agentName, String messageType,
                           String content, JsonNode msg) {
        switch (messageType) {
            case "triaging" -> handleTriagingSignal(state, agentName, msg);
            case "evaluating" -> handleEvaluatingSignal(state, agentName, msg);
            case "heartbeat" -> handleHeartbeatSignal(state, agentName, msg);
            case MSG_WAITING -> handleWaitingSignal(state, agentName, msg);
            case "agree", "contribution" -> handleAgreeSignal(state, agentName, content, msg);
            case "concern" -> handleConcernSignal(state, agentName, content, msg);
            case MSG_STAND_ASIDE, "decline" -> handleStandAsideSignal(state, agentName, msg);
            case MSG_FAILURE -> handleFailureSignal(state, agentName, content, msg);
            case "block" -> handleBlockSignal(state, agentName, content, msg);
            default -> {
                // Other message types (e.g. advisory, proposal, consent) don't need signal tracking
            }
        }
    }

    /**
     * Handle a {@code failure} signal published by an agent whose tool-loop
     * exhausted bounded retries (MCP timeout, OOM, tool unavailable, etc.).
     * Treats the agent as having completed its turn (clears from
     * pendingEvaluations so the coordinator doesn't wait), but records the
     * failure in {@code state.failureSignals} so GapDetector and the
     * dashboard can distinguish infrastructure gaps from missing-tooler
     * gaps. Records latency if the agent had a deadline (evaluating signal
     * preceded the failure) — same semantics as stand_aside.
     */
    private void handleFailureSignal(ThreadState state, String agentName, String content, JsonNode msg) {
        Long deadline = state.pendingEvaluations.remove(agentName);
        if (deadline != null) {
            recordLatencyFromSignal(agentName, msg, deadline);
        }
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        state.waitingAgents.remove(agentName);
        state.failureSignals.put(agentName, content);
        state.lastSignalReceived = Instant.now();
        log.info("Thread {} — agent {} reported failure: {}",
                state.threadId, agentName, truncate(content, LOG_PREVIEW_CHARS));
    }

    private void handleTriagingSignal(ThreadState state, String agentName, JsonNode msg) {
        // Agent is queued on the triage GPU — set a generous deadline so the
        // coordinator doesn't close the thread while agents wait in GPU queue.
        // This deadline will be replaced by the evaluating signal's tighter deadline
        // if the agent passes triage.
        long triageDeadline = System.currentTimeMillis()
                + (properties.triageModel().timeoutSeconds() * 1000L);
        state.pendingEvaluations.put(agentName, triageDeadline);
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        log.info("Agent {} is triaging on {} (deadline {}s)",
                agentName, extractProvider(msg), properties.triageModel().timeoutSeconds());
    }

    // The agent's heartbeat cadence (first beat 15s after evaluating, then every
    // 30s) — MUST match DiscussionSubscriber's startHeartbeat schedule.
    private static final long HEARTBEAT_INTERVAL_MS = 30_000L;

    // deadlineWindowMs is how long an evaluating/heartbeating agent gets before the
    // coordinator expires it. It is FLOORED at the heartbeat interval: a fast-P90
    // agent that is slow THIS round (e.g. an empty-first-turn retry that doubles
    // inference) still heartbeats, but if P90+grace were shorter than the heartbeat
    // interval the deadline would expire in the gap between beats and the agent be
    // wrongly failed (observed: k8s-workloads on threads 642f732b / 7d5626c3 — it
    // heartbeated, then was failed ~9s before its next beat). Flooring guarantees a
    // heartbeating agent always survives to its next beat.
    static long deadlineWindowMs(int expectedSeconds, long graceMs) {
        return Math.max(expectedSeconds * 1000L, HEARTBEAT_INTERVAL_MS) + graceMs;
    }

    private void handleEvaluatingSignal(ThreadState state, String agentName, JsonNode msg) {
        // Agent has passed triage and is about to call tools — update deadline
        // to the (heartbeat-floored) P90-based evaluation deadline.
        String provider = extractProvider(msg);
        int expectedSeconds = latencyTracker.getExpectedSeconds(agentName, provider);
        long deadline = System.currentTimeMillis() + deadlineWindowMs(expectedSeconds, evalGraceMs);
        state.pendingEvaluations.put(agentName, deadline);
        state.waitingAgents.remove(agentName);
        metrics.evaluatingSignalReceived();
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        log.info("Agent {} is evaluating on {}, expected deadline in {}s (P90 from history)",
                agentName, provider, expectedSeconds);
    }

    private void handleHeartbeatSignal(ThreadState state, String agentName, JsonNode msg) {
        // Agent is still alive (queued on GPU or mid-inference). Refresh deadline
        // so the coordinator doesn't expire it while it waits for GPU access.
        if (state.pendingEvaluations.containsKey(agentName)) {
            String provider = extractProvider(msg);
            int expectedSeconds = latencyTracker.getExpectedSeconds(agentName, provider);
            long deadline = System.currentTimeMillis() + deadlineWindowMs(expectedSeconds, evalGraceMs);
            state.pendingEvaluations.put(agentName, deadline);
            log.info("Agent {} heartbeat — deadline refreshed (P90 {}s, floored to heartbeat cadence)",
                    agentName, expectedSeconds);
        }
    }

    private void handleAgreeSignal(ThreadState state, String agentName, String content, JsonNode msg) {
        // Terminal signal — clear pending slot and record latency
        Long deadline = state.pendingEvaluations.remove(agentName);
        recordLatencyFromSignal(agentName, msg, deadline);
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        // Detect researcher role from signal metadata
        if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has(FIELD_ROLE)
                && ROLE_RESEARCHER.equals(msg.get(FIELD_METADATA).get(FIELD_ROLE).asText())) {
            state.researcherAgents.add(agentName);
        }
        state.waitingAgents.remove(agentName);
        state.agreeSignals.put(agentName, content);
        state.lastSignalReceived = Instant.now();
    }

    private void handleConcernSignal(ThreadState state, String agentName, String content, JsonNode msg) {
        Long deadline = state.pendingEvaluations.remove(agentName);
        recordLatencyFromSignal(agentName, msg, deadline);
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        state.waitingAgents.remove(agentName);
        state.concernSignals.put(agentName, content);
        state.lastSignalReceived = Instant.now();
    }

    private void handleStandAsideSignal(ThreadState state, String agentName, JsonNode msg) {
        Long deadline = state.pendingEvaluations.remove(agentName);
        // Only record latency when stand_aside follows an evaluating signal
        // (i.e., agent did real work but found nothing to add after tool calls).
        // Stand-asides from triage failures have no evaluating predecessor — skip.
        if (deadline != null) {
            recordLatencyFromSignal(agentName, msg, deadline);
        }
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        state.waitingAgents.remove(agentName);
        state.standAsideSignals.add(agentName);
        recordCapacityReason(state, agentName, msg);
        state.lastSignalReceived = Instant.now();
    }

    /**
     * An agent is waiting for GPU capacity. Remember the model it waits for (so a
     * synthesis that finds no contribution can say the GPUs were busy) and keep
     * its deadline alive like a heartbeat.
     */
    private void handleWaitingSignal(ThreadState state, String agentName, JsonNode msg) {
        String model = msg.path(FIELD_METADATA).path("model").asText("");
        state.waitingAgents.put(agentName, model);
        handleHeartbeatSignal(state, agentName, msg);
        log.info("Thread {} - agent {} is waiting for a GPU for {}", state.threadId, agentName, model);
    }

    /**
     * Record a stand-aside's capacity reason ({@code gpu-busy}, {@code model-too-large}
     * or {@code prompt-too-large}) and, for model-too-large, the model no GPU can hold.
     * Stand-asides without one of these reasons are ordinary and record nothing.
     */
    // Visible for testing
    static void recordCapacityReason(ThreadState state, String agentName, JsonNode msg) {
        JsonNode meta = msg == null ? null : msg.path(FIELD_METADATA);
        String reason = meta == null ? "" : meta.path(FIELD_REASON).asText("");
        if (!CAPACITY_REASONS.contains(reason)) {
            return;
        }
        state.capacityStandAsides.put(agentName, reason);
        String model = meta.path("model").asText("");
        if (REASON_MODEL_TOO_LARGE.equals(reason) && !model.isEmpty()) {
            state.tooLargeModels.add(model);
        }
    }

    private void handleBlockSignal(ThreadState state, String agentName, String content, JsonNode msg) {
        Long deadline = state.pendingEvaluations.remove(agentName);
        recordLatencyFromSignal(agentName, msg, deadline);
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
        state.waitingAgents.remove(agentName);
        state.blockSignals.put(agentName, content);
        state.lastSignalReceived = Instant.now();
    }

    /**
     * Handle human reply on a closed thread — reopen to EVALUATING phase
     * and clear all previous signals.
     */
    // Visible for testing
    void handleThreadReopen(ThreadState state, String threadId,
                            String messageType, String agentName) {
        if (state.phase != Phase.CLOSED || !"reply".equals(messageType) || !"human".equals(agentName)) {
            return;
        }
        state.phase = Phase.EVALUATING;
        state.phaseStarted = Instant.now();
        state.agreeSignals.clear();
        state.concernSignals.clear();
        state.standAsideSignals.clear();
        state.blockSignals.clear();
        state.failureSignals.clear();
        state.clearCapacitySignals();
        state.researcherAgents.clear();
        state.pendingEvaluations.clear();
        state.phaseRoster = Set.of();
        state.resumeRanking = null;
        state.concurrer = null;
        log.info("Thread {} reopened by human reply → EVALUATING", threadId);
    }

    /**
     * Drive phase transitions based on signals AND timeouts.
     *
     * Signal-driven: transitions happen as soon as responses settle, not when
     * arbitrary timeouts expire. Timeouts are safety nets, not the primary driver.
     */
    private void checkPhaseTransitions() {
        var now = Instant.now();

        for (var entry : threads.entrySet()) {
            var state = entry.getValue();

            // Hard-ceiling backstop FIRST, before the phase table: a discussion must
            // ALWAYS reach a terminal state. This catches threads stuck in ANY phase -
            // including SYNTHESIZING and any phase with no auto edge - that the normal
            // per-phase settles failed to conclude (a missed transition, a hung LLM
            // call, dirty NATS state, a rogue agent). See [[Discussion Request Pump Can Wedge a Crew]].
            if (forceCloseIfOverCeiling(state, now)) continue;

            // The transition table IS the phase machine: a phase absent from it has
            // no automatic edge. SUBMITTED waits for thread_start, PAUSED for
            // thread_resume, SYNTHESIZING/CLOSED are terminal/event-driven. Only
            // ADVISORY, EVALUATING, and REVIEW auto-advance here.
            var rule = autoTransitions.get(state.phase);
            if (rule == null) continue;
            if (!state.transitioning.compareAndSet(false, true)) continue;

            try {
                long elapsed = Duration.between(state.phaseStarted, now).getSeconds();
                rule.advance(state, now, elapsed);
            } finally {
                state.transitioning.set(false);
            }
        }
    }

    // Margin added on top of the phase budgets + cold-start grace to derive the
    // hard ceiling. Generous so the backstop NEVER fires on a healthy discussion
    // (even a cold, slow-GPU one); it exists only for the stuck/rogue case.
    private static final int HARD_CEILING_MARGIN_SECONDS = 180;

    /**
     * Hard backstop ceiling for a single discussion, in seconds. The per-phase
     * settles are the normal path and always conclude first; this bound exists only
     * to guarantee a stuck discussion still terminates. Computed from the phase
     * budgets + the eval cold-start grace + a generous margin so it scales with
     * config and never trips a legitimate run. Visible for testing.
     */
    long hardCeilingSeconds() {
        return (long) COLD_START_GRACE_SECONDS // NOSONAR S1905: deliberate widening so the int sum below is computed in long arithmetic (no overflow)
                + advisoryTimeoutSeconds + evaluationTimeoutSeconds
                + reviewTimeoutSeconds + synthesisTimeoutSeconds
                + HARD_CEILING_MARGIN_SECONDS;
    }

    /**
     * The "adult in the room": guarantee every discussion reaches a terminal state.
     * Returns true when the thread is already terminal OR was force-closed here (the
     * caller then skips further phase processing for it this tick).
     *
     * A discussion that overruns {@link #hardCeilingSeconds()} - because a phase
     * transition was missed, an LLM call hung, the NATS state was dirty, or an agent
     * went rogue - is force-closed with a (salvaged-if-possible) failure synthesis.
     * We publish thread_close so the caller still receives a {@code done} event, and
     * complete the pending future so the request pump unblocks immediately instead of
     * waiting out the orchestrate budget. The normal path never reaches here.
     * See [[Discussion Request Pump Can Wedge a Crew]].
     */
    boolean forceCloseIfOverCeiling(ThreadState state, Instant now) {
        if (state.phase == Phase.CLOSED) return true;
        // PAUSED is a deliberate human suspension; cleanupOldThreads reaps abandoned
        // pauses. Don't force-close a paused discussion.
        if (state.phase == Phase.PAUSED) return false;
        long age = Duration.between(state.threadCreated, now).getSeconds();
        if (age <= hardCeilingSeconds()) return false;
        if (!state.transitioning.compareAndSet(false, true)) return true; // another sweep owns it
        try {
            if (state.phase == Phase.CLOSED) return true; // closed normally between checks
            log.warn("Thread {} exceeded hard ceiling ({}s, phase={}) - force-closing so the "
                    + "discussion never hangs", state.threadId, age, state.phase);
            state.advisoryPending.set(false);
            metrics.threadTimedOut();
            // Funnel through the one terminal-close path so a ceiling-terminated thread
            // gets the same synthesis + thread_close + threadCompleted metric + future
            // completion as a normal close - it must not be a divergent copy.
            closeThread(state, resolveChannel(state), buildFallbackResponse(state));
        } finally {
            state.transitioning.set(false);
        }
        return true;
    }

    /**
     * The per-thread discussion phase machine, as DATA. Each entry maps a phase to
     * how the periodic checker advances a thread OUT of it; phases absent from the
     * table have no automatic edge. One settle implementation ({@link #settlePhase})
     * backs EVALUATING and REVIEW so they cannot drift apart (the analyst-drop bug
     * was two hand-coded settle copies diverging).
     *
     * Full lifecycle (auto = this table; event = NATS message handlers):
     * <pre>
     *   SUBMITTED    --thread_start (event)----------> ADVISORY
     *   ADVISORY     --auto------------------------->  EVALUATING   (advisory generated inline)
     *   EVALUATING   --auto: settle---------------->   DECIDING     (roster settled OR fast-path)
     *   EVALUATING   --auto: settle---------------->   SYNTHESIZING (single agree, crew has no analysts)
     *   DECIDING     --review decision (event)----->   CONCURRING | REVIEW | SYNTHESIZING
     *   CONCURRING   --auto: settle---------------->   REVIEW (concern or failure) | SYNTHESIZING
     *   REVIEW       --auto: settle---------------->   SYNTHESIZING (roster settled; no fast-path)
     *   SYNTHESIZING --completeSynthesis (event)--->   CLOSED
     *   any\{SYNTHESIZING,CLOSED} --thread_pause (event)--> PAUSED
     *   PAUSED       --thread_resume (event)-------->   {phaseBeforePause}
     *   any\{SYNTHESIZING,CLOSED} --stop_requested (event)--> SYNTHESIZING
     * </pre>
     * See [[Discussion Phase Lifecycle as Explicit State Machine]].
     */
    private Map<Phase, PhaseRule> buildAutoTransitions() {
        var table = new EnumMap<Phase, PhaseRule>(Phase.class);
        // ADVISORY: advance immediately; the async LLM call in transitionToEvaluating
        // publishes the advisory + advisory_ready when complete.
        table.put(Phase.ADVISORY, (state, now, elapsed) -> transitionToEvaluating(state));
        // EVALUATING: roster-gated settle with a cold-start grace and the
        // sufficient-consensus fast path; advances to REVIEW.
        table.put(Phase.EVALUATING, (state, now, elapsed) -> settlePhase(state, now, elapsed,
                new PhaseSettle(minEvalSeconds, COLD_START_GRACE_SECONDS, true, true,
                        () -> transitionToReview(state))));
        // REVIEW: roster-gated settle. fastPath=false so it does NOT settle on the
        // tooler agrees carried over from EVAL (that dropped the analyst mid-reasoning,
        // the exact bug); it waits for its own roster. Advances to SYNTHESIZING.
        table.put(Phase.REVIEW, (state, now, elapsed) -> settlePhase(state, now, elapsed,
                new PhaseSettle(minReviewSeconds, 0, false, false,
                        () -> transitionToSynthesis(state))));
        // CONCURRING: the same roster-gated settle over the one analyst asked to
        // concur; a concern (or a failure) escalates to REVIEW, otherwise synthesis.
        table.put(Phase.CONCURRING, (state, now, elapsed) -> settlePhase(state, now, elapsed,
                new PhaseSettle(minReviewSeconds, 0, false, false,
                        () -> afterConcurrence(state))));
        return table;
    }

    /** A phase's automatic-advance rule, invoked by the periodic phase checker. */
    @FunctionalInterface
    interface PhaseRule {
        void advance(ThreadState state, Instant now, long elapsed);
    }

    /** The phases that have an automatic transition edge (visible for testing). */
    Set<Phase> autoTransitionPhases() {
        return Collections.unmodifiableSet(autoTransitions.keySet());
    }

    // Eval cold-start grace: 0-signal threads may have agents scaling from zero
    // (KEDA ~60s pod startup + ~60s model load/triage). Wait this long before
    // declaring "no responses". Eval-only; REVIEW agents are already warm.
    private static final int COLD_START_GRACE_SECONDS = 120;

    /**
     * Settle-and-advance for a roster-gated discussion phase. ONE implementation
     * shared by EVALUATING and REVIEW, parameterized by {@link PhaseSettle}.
     * Previously these were two functions that drifted apart - eval was
     * roster-gated, review was a blunt timer - which is exactly how a slow analyst's
     * REVIEW contribution got dropped (the coordinator synthesized before the
     * analyst finished). Sharing the logic makes that divergence impossible.
     *
     * Advance only when the phase's participants have SETTLED: every agent that
     * published triaging/evaluating has reached a terminal signal (agree / concern /
     * stand_aside / block / failure-by-deadline), the table has been quiet for
     * settleSeconds, OR (eval cold-start) nobody has responded yet and we are still
     * inside the grace. No fixed wall-clock phase timeout - per-agent P90 deadlines
     * (checkEvaluationTimeouts) bound it.
     */
    void settlePhase(ThreadState state, Instant now, long elapsed, PhaseSettle cfg) {
        // Don't advance while the advisory LLM call is still running (eval only):
        // agents won't respond until advisory_ready is published.
        if (cfg.checkAdvisoryPending() && state.advisoryPending.get()) return;

        // Expire agents past their P90+grace deadline -> synthetic failure. The
        // ONLY per-agent timeout; heartbeats keep a deadline refreshed.
        checkEvaluationTimeouts(state);

        // State first: once every participant the phase is waiting for has
        // signalled, it advances without waiting out the floor or a quiet period.
        // The floor below is only a safety net for a phase whose roster is unknown
        // or still has signals pending.
        if (rosterSignalled(state)) {
            log.info("Thread {} every participant of {} has signalled after {}s - transitioning",
                    state.threadId, state.phase, elapsed);
            cfg.advance().run();
            return;
        }

        if (elapsed < cfg.floorSeconds()) return;

        // Fast settle: enough substantive agrees + quiet, without waiting out a
        // slow straggler's deadline. See [[Coordinator Sufficient-Agrees Settle]].
        if (cfg.fastPath() && hasSufficientConsensus(state, now)) {
            log.info("Thread {} sufficient consensus ({} agrees), settling - {} pending dropped",
                    state.threadId, toolerAgreeCount(state), state.pendingEvaluations.size());
            cfg.advance().run();
            return;
        }

        // Roster gate: never advance while a participant is still in-flight. This
        // is the fix for REVIEW - it now waits for the analyst the same way eval
        // waits for toolers, instead of synthesizing on a blunt minReviewSeconds timer.
        if (!canSettle(state)) return;

        advanceIfSettled(state, now, elapsed, cfg);
    }

    /** Advance once the roster has settled; otherwise hold inside the cold-start grace. */
    private void advanceIfSettled(ThreadState state, Instant now, long elapsed, PhaseSettle cfg) {
        if (hasSubstantiveSignal(state, toolerAgreeCount(state)) && isQuiet(state, now)) {
            log.info("Thread {} all participants settled after {}s - transitioning", state.threadId, elapsed);
            cfg.advance().run();
            return;
        }
        if (!state.pendingEvaluations.isEmpty()) return;        // someone still pending
        if (isColdStartWaiting(state, elapsed, cfg)) return;    // 0 signals, agents scaling from zero
        log.info("Thread {} no pending participants after {}s - transitioning", state.threadId, elapsed);
        cfg.advance().run();
    }

    /**
     * True when the phase's roster is known and every member has reached a
     * terminal signal (agree, concern, block, stand aside, or failure) and none is
     * still pending. An unknown (empty) roster is never complete.
     */
    // Visible for testing
    static boolean rosterSignalled(ThreadState state) {
        var roster = state.phaseRoster;
        if (roster == null || roster.isEmpty()) return false;
        for (String member : roster) {
            if (state.pendingEvaluations.containsKey(member) || !hasTerminalSignal(state, member)) {
                return false;
            }
        }
        return true;
    }

    /** True when {@code agent} has published a terminal signal on this thread. */
    static boolean hasTerminalSignal(ThreadState state, String agent) {
        boolean spoke = state.agreeSignals.containsKey(agent) || state.concernSignals.containsKey(agent)
                || state.blockSignals.containsKey(agent);
        return spoke || state.failureSignals.containsKey(agent) || state.standAsideSignals.contains(agent);
    }

    /** Quiet = terminal-signal silence (heartbeats don't count) for settleSeconds. */
    private boolean isQuiet(ThreadState state, Instant now) {
        return state.lastSignalReceived != null
                && Duration.between(state.lastSignalReceived, now).getSeconds() >= settleSeconds;
    }

    /** Substantive = a non-researcher agree, a concern, or a block (researcher agrees don't count). */
    // Visible for testing
    static boolean hasSubstantiveSignal(ThreadState state, long toolerAgrees) {
        return toolerAgrees > 0 || !state.concernSignals.isEmpty() || !state.blockSignals.isEmpty();
    }

    /** Eval cold-start hold: grace enabled, zero signals, still inside the window. */
    // Visible for testing
    static boolean isColdStartWaiting(ThreadState state, long elapsed, PhaseSettle cfg) {
        return cfg.coldStartGraceSeconds() > 0 && hasZeroSignals(state)
                && elapsed < cfg.coldStartGraceSeconds();
    }

    /**
     * Per-phase settle configuration. {@code advance} is the next-phase transition
     * (transitionToReview for EVALUATING, transitionToSynthesis for REVIEW).
     */
    record PhaseSettle(int floorSeconds, int coldStartGraceSeconds,
                       boolean fastPath, boolean checkAdvisoryPending, Runnable advance) {}

    /**
     * Returns true when a thread has received zero terminal signals of any kind.
     */
    // Visible for testing
    static boolean hasZeroSignals(ThreadState state) {
        return state.agreeSignals.isEmpty()
                && state.standAsideSignals.isEmpty()
                && state.concernSignals.isEmpty()
                && state.blockSignals.isEmpty()
                && state.failureSignals.isEmpty();
    }

    /**
     * ADVISORY → EVALUATING: One coordinator reasoning call that selects the subcommittee
     * AND produces a framing brief, grounded in the crew capability catalog.
     *
     * Primary path: coordinator LLM call over crewResumesCache produces
     * {"selected": [...], "brief": "...", "technologies": [...]}.
     * The brief is posted as the advisory message (dashboard timeline);
     * the selected list becomes the innerCircle.
     *
     * Fallback chain (if reasoning fails or returns no usable selection):
     * 1. Vector similarity via ResumeSearchClient (same as the old primary path).
     * 2. runTriage LLM call (existing logic, uses advisory technologies).
     * 3. Broadcast to all (no innerCircle).
     *
     * Runs async to avoid blocking the phase transition checker.
     */
    private void transitionToEvaluating(ThreadState state) {
        state.phase = Phase.EVALUATING;
        state.phaseStarted = Instant.now();
        state.advisoryPending.set(true);

        log.info("Thread {} → EVALUATING phase (coordinator reasoning select+brief)", state.threadId);

        llmExecutor.submit(() -> runAdvisoryGeneration(state));
    }

    /**
     * The EVALUATING-phase advisory generation, run on the llmExecutor. Picks the
     * subcommittee + framing via one of three paths (reasoning select+brief,
     * answer-directly, or the legacy similarity/advisory fallback), then publishes
     * the advisory and resets the evaluation clock. Decomposed from a CC-46 method;
     * each path is its own helper.
     */
    private void runAdvisoryGeneration(ThreadState state) {
        try {
            // Pre-filter, then reason: the resume vector search ranks the crew by
            // similarity to the question and returns the top-K; the coordinator reasons
            // + briefs over that narrow candidate set rather than all 23 agents (recall
            // from the search, precision from the LLM). When the resume search is
            // unavailable or empty, the catalog widens to the full crew.
            List<String> candidates = preFilterCandidates(state);
            String catalog = buildAdvisoryCatalog(loadCrewResumes(), candidates);
            ReasoningResult reasoning = null;
            if (catalog != null && !catalog.isEmpty()) {
                reasoning = runReasoningSelectBrief(state, catalog);
            } else {
                log.info("Thread {} - no crew capability catalog available, skipping reasoning select+brief",
                        state.threadId);
            }

            AdvisoryOutcome outcome;
            if (reasoning != null && !reasoning.selected().isEmpty()) {
                outcome = applyReasoningSelection(state, reasoning);
            } else if (shouldAnswerDirectly(reasoning, answerDirectlyEnabled)) {
                // Reasoning SUCCEEDED but deliberately selected NO toolers: the coordinator
                // can answer this itself (general knowledge) and its brief IS that answer.
                // Honor it - answer directly and close, waking no toolers. Distinct from a
                // reasoning FAILURE (null), which falls back below. Gated by
                // kubemoot.discuss.answer-directly: a single-specialist crew (the fitness
                // judge) disables it so the coordinator always delegates to its specialist
                // instead of answering a coordinator-answerable-looking embedded question.
                // See [[Coordinator Empty Selection Treated As Failure]].
                state.advisoryInputTokens = reasoning.inputTokens();
                state.advisoryOutputTokens = reasoning.outputTokens();
                state.advisoryPending.set(false);
                answerDirectly(state, reasoning);
                return;
            } else {
                outcome = selectViaFallback(state);
            }

            state.advisoryContent = outcome.wisdom();
            state.advisoryTechnologies = new CopyOnWriteArrayList<>(outcome.technologies());
            state.primaryChannel = classifyChannelFromAdvisory(outcome.technologies());

            metrics.advisoryCompleted(Duration.ofMillis(outcome.inferenceMs()));
            log.info("Thread {} advisory done in {}ms: technologies={}, layers={}, channel={}",
                    state.threadId, outcome.inferenceMs(), outcome.technologies(), outcome.layers(),
                    state.primaryChannel);

            publishAdvisory(state.threadId, outcome.technologies(), outcome.layers(), outcome.wisdom(),
                    outcome.inferenceMs());
            // advisory_ready triggers tooler evaluation (includes innerCircle)
            publishAdvisoryReady(state, outcome.technologies(), outcome.wisdom());
            resetEvaluationClock(state);
            log.info("Thread {} evaluation clock reset after advisory_ready ({}ms advisory)",
                    state.threadId, outcome.inferenceMs());

        } catch (Exception e) {
            log.warn("Advisory generation failed for thread {}: {}", state.threadId, e.getMessage());
            // Still publish advisory_ready without advisory content, and reset the clock so
            // toolers get a full evaluation window.
            publishAdvisoryReady(state, List.of(), "Advisory generation failed");
            resetEvaluationClock(state);
        }
    }

    /** Primary path: apply the reasoning call's selection as the inner circle. */
    private AdvisoryOutcome applyReasoningSelection(ThreadState state, ReasoningResult reasoning) {
        state.advisoryInputTokens = reasoning.inputTokens();
        state.advisoryOutputTokens = reasoning.outputTokens();
        state.innerCircle = ConcurrentHashMap.newKeySet();
        state.innerCircle.addAll(reasoning.selected());
        state.triageConfidence = 0.9; // Reasoning path is high confidence
        log.info("Thread {} - reasoning selected {} agents in {}ms: {}",
                state.threadId, reasoning.selected().size(), reasoning.inferenceMs(), reasoning.selected());
        return new AdvisoryOutcome(reasoning.technologies(), List.of(), reasoning.brief(),
                reasoning.inferenceMs());
    }

    /**
     * Fallback path when reasoning failed/empty: a vector-similarity pre-filter plus
     * the classic advisory JSON call. Similarity-selected agents become the inner
     * circle; otherwise runTriage selects over the advisory technologies.
     */
    private AdvisoryOutcome selectViaFallback(ThreadState state) {
        log.info("Thread {} - reasoning select+brief failed or empty, trying similarity fallback",
                state.threadId);
        List<String> similaritySelected = preFilterCandidates(state);

        String advisoryPrompt = buildLegacyAdvisoryPrompt(state.userQuery, state);
        String systemPrompt = loadSystemPrompt();
        long inferenceStart = System.currentTimeMillis();
        var advisoryResult = chatService.hasDistinctTriageModel()
                ? chatService.triageChatWithTokens(systemPrompt, advisoryPrompt)
                : chatService.simpleLlmCallWithTokens(systemPrompt, advisoryPrompt);
        long inferenceMs = System.currentTimeMillis() - inferenceStart;
        state.advisoryInputTokens = advisoryResult.inputTokens();
        state.advisoryOutputTokens = advisoryResult.outputTokens();

        LegacyAdvisory parsed = parseLegacyAdvisory(advisoryResult.text());

        if (similaritySelected != null && !similaritySelected.isEmpty()) {
            // Similarity pre-filter returned agents (already logged by the pre-filter) -
            // use them as the inner circle and skip runTriage (avoids a second LLM call).
            state.innerCircle = ConcurrentHashMap.newKeySet();
            state.innerCircle.addAll(similaritySelected);
        } else {
            runTriage(state, parsed.technologies(), parsed.wisdom());
        }
        return new AdvisoryOutcome(parsed.technologies(), parsed.layers(), parsed.wisdom(), inferenceMs);
    }

    /**
     * Reset the evaluation clock and clear signals AFTER advisory_ready is published.
     * Signals received during advisory generation (from agents reacting to the
     * thread_start broadcast) are stale - they predate the advisory. Resetting makes
     * the settle window and timeouts measure from when toolers actually receive
     * advisory_ready, not from when the advisory LLM call started.
     */
    private void resetEvaluationClock(ThreadState state) {
        state.phaseStarted = Instant.now();
        state.lastSignalReceived = null;
        state.agreeSignals.clear();
        state.concernSignals.clear();
        state.standAsideSignals.clear();
        state.blockSignals.clear();
        state.failureSignals.clear();
        state.clearCapacitySignals();
        state.researcherAgents.clear();
        state.pendingEvaluations.clear();
        state.phaseRoster = evaluationRoster(state.innerCircle, analystNamesFromResumes());
        state.advisoryPending.set(false);
    }

    /**
     * The agents EVALUATING waits for: the selected agents minus the analysts,
     * which act only in review. Empty (unknown) when no inner circle was selected.
     */
    static Set<String> evaluationRoster(Set<String> innerCircle, Set<String> analysts) {
        if (innerCircle == null || innerCircle.isEmpty()) return Set.of();
        var roster = new HashSet<>(innerCircle);
        roster.removeAll(analysts);
        return Set.copyOf(roster);
    }

    /** The advisory inputs the EVALUATING tail needs, regardless of which path produced them. */
    record AdvisoryOutcome(List<String> technologies, List<String> layers, String wisdom, long inferenceMs) {}

    /** Parsed legacy advisory JSON: technologies, layers, and the wisdom/brief text. */
    record LegacyAdvisory(List<String> technologies, List<String> layers, String wisdom) {}

    /**
     * Parse the legacy advisory response into technologies / layers / wisdom. Tolerant
     * of a ```json code fence and of non-JSON: invalid JSON falls back to the raw text
     * as wisdom with empty technologies/layers. Pure (uses the static mapper) so it is
     * unit-tested directly.
     */
    static LegacyAdvisory parseLegacyAdvisory(String advisoryResponse) {
        List<String> technologies = new ArrayList<>();
        List<String> layers = new ArrayList<>();
        String wisdom = advisoryResponse != null ? advisoryResponse : "";
        if (advisoryResponse == null || advisoryResponse.isEmpty()) {
            return new LegacyAdvisory(technologies, layers, wisdom);
        }
        try {
            var jsonNode = mapper.readTree(stripCodeFence(advisoryResponse));
            if (jsonNode.has(FIELD_TECHNOLOGIES)) {
                jsonNode.get(FIELD_TECHNOLOGIES).forEach(n -> technologies.add(n.asText()));
            }
            if (jsonNode.has(FIELD_LAYERS)) {
                jsonNode.get(FIELD_LAYERS).forEach(n -> layers.add(n.asText()));
            }
            if (jsonNode.has(FIELD_WISDOM)) {
                wisdom = jsonNode.get(FIELD_WISDOM).asText();
            }
        } catch (Exception e) {
            log.debug("Fallback advisory response not valid JSON, using raw text: {}", e.getMessage());
        }
        return new LegacyAdvisory(technologies, layers, wisdom);
    }

    /** Strip a leading/trailing ``` or ```json code fence and trim. Null-safe. */
    static String stripCodeFence(String raw) {
        if (raw == null) return "";
        String cleaned = raw.trim();
        if (cleaned.startsWith("```")) {
            cleaned = cleaned.replaceAll("^```\\w*\\n?", "").replaceAll("\\n?```$", "").trim();
        }
        return cleaned;
    }

    /**
     * Result of the coordinator's combined select+brief reasoning call.
     * All fields are parsed via mapper.readTree() - never via record/readValue (GraalVM native).
     */
    record ReasoningResult(
            List<String> selected,
            String brief,
            List<String> technologies,
            long inferenceMs,
            long inputTokens,
            long outputTokens
    ) {}

    /**
     * True when the coordinator's reasoning SUCCEEDED but deliberately selected no
     * toolers AND produced an answer in its brief - the "answer directly" decision
     * (general knowledge / coordinator-answerable). Distinct from a reasoning
     * FAILURE (null) or an empty/unusable result, both of which fall back to
     * broadcast. See [[Coordinator Empty Selection Treated As Failure]].
     */
    static boolean shouldAnswerDirectly(ReasoningResult reasoning) {
        return reasoning != null && reasoning.selected().isEmpty() && !reasoning.brief().isBlank();
    }

    /**
     * Answer-directly gated by the crew's feature flag. When {@code enabled} is false
     * (single-specialist crews like the fitness judge), the coordinator NEVER answers
     * directly - it always falls through to selection so it delegates to its specialist.
     */
    static boolean shouldAnswerDirectly(ReasoningResult reasoning, boolean enabled) {
        return enabled && shouldAnswerDirectly(reasoning);
    }

    /**
     * Run the primary coordinator reasoning call: ONE LLM call that selects the
     * subcommittee AND produces a framing brief grounded in the crew capability catalog.
     *
     * Output contract: {"selected": ["agent-a","agent-b",...], "brief": "...", "technologies": [...]}
     * Parsed with readTree() only - never records or readValue (GraalVM native fails silently on those).
     *
     * @return ReasoningResult on success, or null if the call failed or produced unusable output
     */
    ReasoningResult runReasoningSelectBrief(ThreadState state, String resumes) {
        try {
            String prompt = buildReasoningSelectPrompt(state.userQuery, resumes, state);
            String systemPrompt = loadSystemPrompt();
            long inferenceStart = System.currentTimeMillis();
            // This is a reasoning call (not a fast classification) - use the main model,
            // not the triage model, so the coordinator has full reasoning capacity.
            var result = chatService.simpleLlmCallWithTokens(systemPrompt, prompt);
            long inferenceMs = System.currentTimeMillis() - inferenceStart;

            String response = result.text();
            if (response == null || response.isEmpty()) {
                log.warn("Thread {} - reasoning select+brief returned empty response", state.threadId);
                return null;
            }

            // Parse with readTree() - never readValue/records (GraalVM native rule)
            String cleaned = response.trim();
            if (cleaned.startsWith("```")) {
                cleaned = cleaned.replaceAll("^```\\w*\\n?", "").replaceAll("\\n?```$", "").trim();
            }
            var jsonNode = mapper.readTree(cleaned);

            // Parse "selected" - list of agent names
            var selected = new ArrayList<String>();
            if (jsonNode.has(FIELD_SELECTED) && jsonNode.get(FIELD_SELECTED).isArray()) {
                jsonNode.get(FIELD_SELECTED).forEach(n -> {
                    String name = n.asText("").trim();
                    if (!name.isEmpty()) selected.add(name);
                });
            }

            // Validate selected names against known agents (drop hallucinated names)
            var validSelected = validateAgentNamesList(selected);
            if (validSelected.size() < selected.size()) {
                log.warn("Thread {} - reasoning dropped {} hallucinated agent names (kept {})",
                        state.threadId, selected.size() - validSelected.size(), validSelected.size());
            }

            // Parse "brief" - the framing words of wisdom
            String brief = jsonNode.has(FIELD_BRIEF) ? jsonNode.get(FIELD_BRIEF).asText("") : "";

            // Parse "technologies" - optional, for channel classification
            var technologies = new ArrayList<String>();
            if (jsonNode.has(FIELD_TECHNOLOGIES) && jsonNode.get(FIELD_TECHNOLOGIES).isArray()) {
                jsonNode.get(FIELD_TECHNOLOGIES).forEach(n -> technologies.add(n.asText()));
            }

            // Parse "skills" - optional array of skill names selected by the coordinator
            var selectedSkills = parseAndValidateSkills(jsonNode);
            if (!selectedSkills.isEmpty()) {
                state.selectedSkills = ConcurrentHashMap.newKeySet();
                state.selectedSkills.addAll(selectedSkills);
                log.info("Thread {} - coordinator selected {} skills: {}",
                        state.threadId, selectedSkills.size(), selectedSkills);
            }

            log.info("Thread {} - reasoning select+brief parsed: selected={}, technologies={}, brief={}",
                    state.threadId, validSelected, technologies, truncate(brief, LOG_PREVIEW_CHARS));

            return new ReasoningResult(validSelected, brief, technologies, inferenceMs,
                    result.inputTokens(), result.outputTokens());

        } catch (Exception e) {
            log.warn("Thread {} - reasoning select+brief call failed: {}", state.threadId, e.getMessage());
            return null;
        }
    }

    /**
     * RAG pre-filter: rank the crew by resume-vector similarity to the question and
     * return the top-K candidate agent names. Used by BOTH the primary path (to narrow
     * the catalog the coordinator reasons over) and the fallback path (where the
     * similarity result is applied directly as the inner circle). Returns null when the
     * resume search is unavailable or finds nothing, so the caller widens to the full
     * catalog. Does NOT mutate state.innerCircle.
     */
    private List<String> preFilterCandidates(ThreadState state) {
        if (resumeSearchClient == null || !resumeSearchClient.isAvailable()) {
            log.debug("Thread {} - resume search not available for pre-filter", state.threadId);
            return null;
        }
        try {
            int topK = properties.discuss().resumePreFilterTopK();
            var agents = resumeSearchClient.searchResumes(state.userQuery, topK);
            if (agents != null && !agents.isEmpty()) {
                var candidates = withAlwaysCandidates(agents);
                log.info("Thread {} - resume pre-filter ranked {} candidate agents: {}",
                        state.threadId, candidates.size(), candidates);
                return candidates;
            }
            log.info("Thread {} - resume pre-filter returned no matches", state.threadId);
        } catch (Exception e) {
            log.warn("Thread {} - resume pre-filter failed: {}", state.threadId, e.getMessage());
        }
        return null;
    }

    /**
     * Union the cross-cutting always-candidate agents (config) into the domain-ranked
     * candidate list so a domain-agnostic capability (compute: counting, sorting, math)
     * reaches reasoning-select even though it never wins the domain similarity ranking.
     * See B0 in [[Crew Evasion - Answer From Available Data]].
     */
    private List<String> withAlwaysCandidates(List<String> ranked) {
        return unionAlwaysCandidates(ranked, alwaysCandidateNames(), knownAgentNames.get());
    }

    /** Comma-separated always-candidate agent names from config, trimmed and non-empty. */
    private List<String> alwaysCandidateNames() {
        return properties.discuss().alwaysCandidateAgents()
                .map(DiscussionOrchestrator::splitCsv)
                .orElseGet(List::of);
    }

    /** Split a comma-separated string into trimmed, non-empty tokens. */
    static List<String> splitCsv(String csv) {
        var out = new ArrayList<String>();
        for (var tok : csv.split(",")) {
            var t = tok.trim();
            if (!t.isEmpty()) out.add(t);
        }
        return out;
    }

    /**
     * Append each always-candidate name to the ranked list when it is not already
     * present and is a known crew agent. Preserves the ranked order; extras follow.
     * knownNames may be null/empty (validation disabled), in which case extras are
     * added as-is.
     */
    static List<String> unionAlwaysCandidates(List<String> ranked, List<String> extras,
            List<String> knownNames) {
        if (extras == null || extras.isEmpty()) return ranked;
        var result = new ArrayList<String>(ranked == null ? List.of() : ranked);
        boolean validate = knownNames != null && !knownNames.isEmpty();
        for (var name : extras) {
            boolean admissible = !validate || knownNames.contains(name);
            if (admissible && !result.contains(name)) {
                result.add(name);
            }
        }
        return result;
    }

    /**
     * Validate a plain list of agent name strings against knownAgentNames.
     * Returns only names that appear in the known list. Preserves order.
     */
    private List<String> validateAgentNamesList(List<String> names) {
        var known = knownAgentNames.get();
        if (known.isEmpty()) {
            return names;
        }
        var validated = new ArrayList<String>();
        for (var name : names) {
            if (known.contains(name)) {
                validated.add(name);
            } else {
                log.warn("Reasoning selected unknown agent '{}' - dropping (hallucinated name)", name);
            }
        }
        return validated;
    }

    /**
     * Parse the optional "skills" array from the coordinator JSON and validate names
     * against knownSkillNames. Returns empty list when field is absent or empty.
     * Mirrors validateAgentNamesList for agent names.
     */
    private List<String> parseAndValidateSkills(JsonNode jsonNode) {
        var raw = new ArrayList<String>();
        if (jsonNode.has(FIELD_SKILLS) && jsonNode.get(FIELD_SKILLS).isArray()) {
            jsonNode.get(FIELD_SKILLS).forEach(n -> {
                String name = n.asText("").trim();
                if (!name.isEmpty()) raw.add(name);
            });
        }
        if (raw.isEmpty()) return raw;
        return validateSkillNamesList(raw);
    }

    /**
     * Validate a list of skill names against knownSkillNames.
     * Returns only names present in the known set. Preserves order.
     */
    private List<String> validateSkillNamesList(List<String> names) {
        var known = knownSkillNames.get();
        if (known.isEmpty()) {
            return names;
        }
        var validated = new ArrayList<String>();
        for (var name : names) {
            if (known.contains(name)) {
                validated.add(name);
            } else {
                log.warn("Coordinator selected unknown skill '{}' - dropping (hallucinated name)", name);
            }
        }
        return validated;
    }

    private void publishAdvisory(String threadId, List<String> technologies, List<String> layers,
                                  String wisdom, long inferenceMs) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var metadata = new HashMap<String, Object>();
            metadata.put(FIELD_TECHNOLOGIES, technologies);
            metadata.put(FIELD_LAYERS, layers);
            metadata.put(FIELD_WISDOM, wisdom);
            metadata.put(FIELD_INFERENCE_MS, inferenceMs);
            // Surface coordinator GPU + model so the dashboard timeline can
            // render the same colored badge it does for toolers.
            metadata.put(FIELD_GPU_LABEL, GpuLabels.fromEndpoint(properties.model().endpoint()));
            metadata.put(FIELD_MODEL_NAME, properties.model().model());
            // Plumb the LangChain4j-reported Ollama load_duration through the
            // most recent simpleLlmCall, if any — so the dashboard can split
            // the inference span into a pale "model load" prefix.
            long loadMs = chatService.getLastLoadDurationMs();
            if (loadMs > 0) {
                metadata.put("loadDurationMs", loadMs);
            }

            var advisory = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, MSG_ADVISORY,
                    FIELD_CONTENT, truncate(wisdom, ADVISORY_CONTENT_CHARS),
                    FIELD_CHANNEL, CHANNEL_BROADCAST,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, metadata
            );

            String crew = threads.containsKey(threadId) ? threads.get(threadId).crew : null;
            String subject = broadcastSubject(crew, threadId);
            conn.publish(subject, mapper.writeValueAsBytes(advisory));
            log.info("Published advisory to {} for thread {}", subject, threadId);

        } catch (Exception e) {
            log.debug("Failed to publish advisory: {}", e.getMessage());
        }
    }

    private void publishAdvisoryReady(ThreadState state, List<String> technologies, String wisdom) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var metadata = new HashMap<String, Object>();
            metadata.put(FIELD_USER_QUERY, state.userQuery);
            metadata.put(FIELD_PRIMARY_CHANNEL, state.primaryChannel);
            metadata.put(FIELD_GPU_LABEL, GpuLabels.fromEndpoint(properties.model().endpoint()));
            metadata.put(FIELD_MODEL_NAME, properties.model().model());
            if (!technologies.isEmpty()) {
                metadata.put(FIELD_TECHNOLOGIES, technologies);
            }
            if (wisdom != null && !wisdom.isEmpty()) {
                metadata.put(MSG_ADVISORY, wisdom);
            }
            // Include conversation context so toolers can resolve follow-up references
            var conversationContext = buildConversationContext(state.conversationId);
            if (!conversationContext.isEmpty()) {
                metadata.put(FIELD_CONVERSATION_CONTEXT, conversationContext);
            }
            // Include triage inner circle so toolers can skip evaluation early
            if (state.innerCircle != null && !state.innerCircle.isEmpty()) {
                metadata.put(FIELD_INNER_CIRCLE, new ArrayList<>(state.innerCircle));
                metadata.put("triageConfidence", state.triageConfidence);
            }
            // Propagate selected skills so a later chunk can inject skill bodies.
            // Absent when no skills were selected (behavior unchanged from baseline).
            if (state.selectedSkills != null && !state.selectedSkills.isEmpty()) {
                metadata.put(FIELD_SELECTED_SKILLS, new ArrayList<>(state.selectedSkills));
            }

            String advisorySummary = !technologies.isEmpty()
                    ? "Technologies: " + String.join(", ", technologies) + ". " + truncate(wisdom, LOG_PREVIEW_CHARS)
                    : truncate(wisdom, LOG_PREVIEW_CHARS);

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, state.threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, MSG_ADVISORY_READY,
                    FIELD_CONTENT, advisorySummary,
                    FIELD_CHANNEL, CHANNEL_BROADCAST,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, metadata
            );

            String subject = broadcastSubject(state.crew, state.threadId);
            conn.publish(subject, mapper.writeValueAsBytes(message));
            log.info("Published advisory_ready to {} for thread {}", subject, state.threadId);

        } catch (Exception e) {
            log.warn("Failed to publish advisory_ready: {}", e.getMessage());
        }
    }

    /**
     * Whether to skip the REVIEW phase and go straight to synthesis. True only
     * when exactly one tooler agreed, there is no dissent (no concerns,
     * no blocks), AND the crew has no analyst-role agents. Analysts act in
     * REVIEW, so if any exist the phase must run for them to self-select even
     * on a single caller agree.
     */
    static boolean shouldSkipReview(long toolerAgrees, int concerns, int blocks, boolean hasAnalysts) {
        return toolerAgrees == 1 && concerns == 0 && blocks == 0 && !hasAnalysts;
    }

    /**
     * Start REVIEW's roster: every agent review_ready wakes is pending from the
     * moment REVIEW begins, with the same deadline a triaging signal opens. Without
     * it the roster would be empty until each reviewer's first signal arrived, and
     * REVIEW could settle at its floor before any reviewer had started. Eval-phase
     * stragglers are dropped so REVIEW waits only for its own participants.
     */
    // Visible for testing
    void seedReviewRoster(ThreadState state, Collection<String> reviewers, long nowMs) {
        state.pendingEvaluations.clear();
        long deadline = nowMs + properties.triageModel().timeoutSeconds() * 1000L;
        for (String reviewer : reviewers) {
            state.pendingEvaluations.put(reviewer, deadline);
        }
        log.info("Thread {} review roster: {}", state.threadId, reviewers);
    }

    /**
     * EVALUATING has settled. A crew with no analysts and a single clean tooler
     * agree goes straight to synthesis. Otherwise the thread enters DECIDING and
     * the review is shaped off the phase-checker thread (the review decision call
     * and the analysts' resume ranking both do I/O): a crew that declares the
     * review decision gets one coordinator call that chooses concur, full, or
     * none; any other crew runs the full review.
     */
    private void transitionToReview(ThreadState state) {
        metrics.evaluationCompleted(Duration.between(state.phaseStarted, Instant.now()));
        metrics.recordSignals(state.agreeSignals.size(), state.concernSignals.size(),
                state.standAsideSignals.size(), state.blockSignals.size());
        log.info("Thread {} evaluation complete ({} agrees, {} stand-asides, {} concerns, {} failures)",
                state.threadId, state.agreeSignals.size(), state.standAsideSignals.size(),
                state.concernSignals.size(), state.failureSignals.size());

        long toolerAgrees = toolerAgreeCount(state);
        if (shouldSkipReview(toolerAgrees, state.concernSignals.size(),
                state.blockSignals.size(), hasAnalysts)) {
            log.info("Thread {} - single tooler agree and no analysts in the crew, skipping review -> synthesis",
                    state.threadId);
            transitionToSynthesis(state);
            return;
        }
        state.phase = Phase.DECIDING;
        state.phaseStarted = Instant.now();
        llmExecutor.submit(() -> decideReviewOrSynthesize(state, toolerAgrees));
    }

    /** {@link #decideReview}, synthesizing what the thread has when shaping the review fails. */
    // Visible for testing
    void decideReviewOrSynthesize(ThreadState state, long toolerAgrees) {
        try {
            decideReview(state, toolerAgrees);
        } catch (Exception e) {
            log.warn("Thread {} could not start its review, synthesizing: {}", state.threadId, e.getMessage());
            synchronized (state) {
                if (state.phase == Phase.DECIDING) {
                    transitionToSynthesis(state);
                }
            }
        }
    }

    /**
     * The phase the review starts in and the analysts it wakes. SYNTHESIZING with no
     * reviewers when there is no review to run.
     */
    record ReviewPlan(Phase phase, List<String> reviewers) {
        static final ReviewPlan SYNTHESIS = new ReviewPlan(Phase.SYNTHESIZING, List.of());

        static ReviewPlan of(Phase phase, List<String> reviewers) {
            return reviewers.isEmpty() ? SYNTHESIS : new ReviewPlan(phase, reviewers);
        }
    }

    /**
     * Shape the review and start it. The decision and the reviewers are worked out
     * first (both may do I/O); the phase then moves under the thread's lock, and a
     * thread that left DECIDING meanwhile (the dashboard's Stop) is left as it is.
     * Without the crew's review decision this is the full review. A decision call
     * that hangs is bounded by the discussion's hard ceiling.
     */
    // Visible for testing
    void decideReview(ThreadState state, long toolerAgrees) {
        var decision = reviewDecisionEnabled
                ? reviewDecisionFor(state, toolerAgrees)
                : new ReviewDecision.Decision(ReviewDecision.Shape.FULL, "no review decision declared", true);
        var plan = planReview(state, decision.shape());
        synchronized (state) {
            if (state.phase != Phase.DECIDING) {
                log.info("Thread {} left DECIDING ({}) before the review started", state.threadId, state.phase);
                return;
            }
            if (reviewDecisionEnabled) {
                log.info("Thread {} review decision: {} ({}{})", state.threadId, decision.shape().wireName(),
                        decision.reason(), decision.forced() ? ", runtime guard" : "");
                publishReviewDecision(state, decision);
            }
            startPlannedReview(state, plan);
        }
    }

    /** The plan for a decided shape: the concurrer, the full review's analysts, or synthesis. */
    private ReviewPlan planReview(ThreadState state, ReviewDecision.Shape shape) {
        return switch (shape) {
            case NONE -> ReviewPlan.SYNTHESIS;
            case CONCUR -> ReviewPlan.of(Phase.CONCURRING, ReviewRoster.concurrer(selectedAnalysts(state),
                    analystNamesFromResumes(), analystRanking(state), Set.of()));
            case FULL -> ReviewPlan.of(Phase.REVIEW, fullReviewers(state, Set.of()));
        };
    }

    /** Enter the planned phase and wake its analysts; with none to wake, synthesize. */
    private void startPlannedReview(ThreadState state, ReviewPlan plan) {
        if (plan.phase() == Phase.SYNTHESIZING) {
            log.info("Thread {} - no review to run, synthesizing", state.threadId);
            transitionToSynthesis(state);
            return;
        }
        boolean concurrence = plan.phase() == Phase.CONCURRING;
        state.concurrer = concurrence ? plan.reviewers().get(0) : state.concurrer;
        enterReviewPhase(state, plan.phase(), plan.reviewers());
        publishReviewReady(state, plan.reviewers(), concurrence);
    }

    /**
     * The review decision: a runtime guard's shape when one applies (a failure,
     * a concern, a block, or no tooler agreement always escalates), otherwise the
     * crew's policy through one model call. A failed call is a full review.
     */
    // Visible for testing
    ReviewDecision.Decision reviewDecisionFor(ThreadState state, long toolerAgrees) {
        var evidence = new ReviewDecision.Evidence(toolerAgrees, state.failureSignals.size(),
                state.concernSignals.size(), state.blockSignals.size());
        var forced = ReviewDecision.forced(evidence);
        if (forced.isPresent()) {
            return forced.get();
        }
        try {
            String prompt = ReviewDecision.prompt(state.userQuery, buildSignalContext(state, toolerAgrees),
                    selectedAnalysts(state), reviewSummary(state));
            return ReviewDecision.parse(callReviewDecisionModel(prompt).text());
        } catch (Exception e) {
            log.warn("Thread {} review decision call failed, running the full review: {}",
                    state.threadId, e.getMessage());
            return new ReviewDecision.Decision(ReviewDecision.Shape.FULL, "the review decision call failed", true);
        }
    }

    /** The fast tier (the triage model) unless the crew declares the reasoning tier. */
    private ChatService.SimpleLlmResult callReviewDecisionModel(String prompt) {
        String system = loadSystemPrompt();
        if (!TIER_REASONING.equals(reviewDecisionTier) && chatService.hasDistinctTriageModel()) {
            return chatService.triageChatWithTokens(system, prompt);
        }
        return chatService.simpleLlmCallWithTokens(system, prompt);
    }

    /**
     * CONCURRING has settled. A concern, an objection, or a failure from the
     * analyst escalates to the full review, with its view on the board and without
     * waking it again; agreement or a stand aside goes to synthesis.
     */
    // Visible for testing
    void afterConcurrence(ThreadState state) {
        boolean dissented = concurrerDissented(state);
        var plan = dissented
                ? ReviewPlan.of(Phase.REVIEW, fullReviewers(state, Set.of(state.concurrer)))
                : ReviewPlan.SYNTHESIS;
        synchronized (state) {
            if (state.phase != Phase.CONCURRING) {
                log.info("Thread {} left CONCURRING ({}) before it settled", state.threadId, state.phase);
                return;
            }
            if (dissented) {
                log.info("Thread {} - {} did not concur, escalating to the full review",
                        state.threadId, state.concurrer);
            }
            startPlannedReview(state, plan);
        }
    }

    /** True when the analyst asked to concur raised a concern or an objection, or failed. */
    static boolean concurrerDissented(ThreadState state) {
        String concurrer = state.concurrer;
        if (concurrer == null) return false;
        boolean objected = state.concernSignals.containsKey(concurrer) || state.blockSignals.containsKey(concurrer);
        return objected || state.failureSignals.containsKey(concurrer);
    }

    /** The full review's analysts, never every analyst in the crew. */
    private List<String> fullReviewers(ThreadState state, Set<String> exclude) {
        return ReviewRoster.full(selectedAnalysts(state), analystNamesFromResumes(), analystRanking(state), exclude);
    }

    /** Enter a review phase whose roster is {@code reviewers}, all pending from now. */
    // Visible for testing
    void enterReviewPhase(ThreadState state, Phase phase, List<String> reviewers) {
        state.phase = phase;
        state.phaseStarted = Instant.now();
        state.phaseRoster = Set.copyOf(reviewers);
        seedReviewRoster(state, reviewers, System.currentTimeMillis());
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
    }

    /** The analysts the coordinator selected for this thread, by name. */
    private List<String> selectedAnalysts(ThreadState state) {
        var selected = new ArrayList<>(analystCircleFrom(state.innerCircle, analystNamesFromResumes()));
        Collections.sort(selected);
        return selected;
    }

    /**
     * The crew's resume ranking for this thread's question, looked up once per
     * thread: every agent the search returns, best match first. Empty when the
     * resume search is unavailable.
     */
    private List<String> analystRanking(ThreadState state) {
        var cached = state.resumeRanking;
        if (cached != null) return cached;
        List<String> ranking = List.of();
        if (resumeSearchClient != null) {
            try {
                int topK = Math.max(properties.discuss().resumePreFilterTopK(), 3 * knownAgentNames.get().size());
                var found = resumeSearchClient.searchResumes(state.userQuery, topK);
                ranking = found == null ? List.of() : List.copyOf(found);
            } catch (Exception e) {
                log.warn("Thread {} - resume ranking for the review failed: {}", state.threadId, e.getMessage());
            }
        }
        state.resumeRanking = ranking;
        return ranking;
    }

    /** Every contribution and concern so far, each truncated, as the reviewers and the decision see them. */
    private static String reviewSummary(ThreadState state) {
        var summary = new StringBuilder();
        for (var agreeEntry : state.agreeSignals.entrySet()) {
            summary.append("[").append(agreeEntry.getKey()).append("] ")
                    .append(truncate(agreeEntry.getValue(), REVIEW_SNIPPET_CHARS)).append("\n\n");
        }
        for (var concernEntry : state.concernSignals.entrySet()) {
            summary.append("[CONCERN from ").append(concernEntry.getKey()).append("] ")
                    .append(truncate(concernEntry.getValue(), REVIEW_SNIPPET_CHARS)).append("\n\n");
        }
        return summary.toString();
    }

    /**
     * Publish review_ready to exactly {@code reviewers} (the inner circle the
     * subscribers gate on). A concurrence request carries reviewMode=concur and
     * opens with {@link ReviewDecision#CONCURRENCE_REQUEST}.
     */
    private void publishReviewReady(ThreadState state, List<String> reviewers, boolean concurrence) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;
            var metadata = new HashMap<String, Object>();
            metadata.put(FIELD_USER_QUERY, state.userQuery);
            metadata.put("agreeCount", state.agreeSignals.size());
            metadata.put("standAsideCount", state.standAsideSignals.size());
            metadata.put("concernCount", state.concernSignals.size());
            metadata.put("failureCount", state.failureSignals.size());
            metadata.put(FIELD_GPU_LABEL, GpuLabels.fromEndpoint(properties.model().endpoint()));
            metadata.put(FIELD_MODEL_NAME, properties.model().model());
            metadata.put(FIELD_INNER_CIRCLE, reviewers);
            metadata.put(FIELD_REVIEW_MODE, concurrence ? ReviewDecision.Shape.CONCUR.wireName()
                    : ReviewDecision.Shape.FULL.wireName());
            String content = concurrence
                    ? ReviewDecision.CONCURRENCE_REQUEST + "\n\n" + reviewSummary(state)
                    : reviewSummary(state);
            publishBroadcast(state, MSG_REVIEW_READY, content, metadata);
            log.info("Published review_ready ({}) to {} for thread {}",
                    concurrence ? "concurrence" : "full review", reviewers, state.threadId);
        } catch (Exception e) {
            log.warn("Failed to publish review_ready: {}", e.getMessage());
        }
    }

    /** Publish the review decision for the dashboard timeline; the coordinator ignores its own echo. */
    private void publishReviewDecision(ThreadState state, ReviewDecision.Decision decision) {
        try {
            var metadata = new HashMap<String, Object>();
            metadata.put("decision", decision.shape().wireName());
            metadata.put(FIELD_REASON, decision.reason());
            metadata.put("forced", decision.forced());
            metadata.put("tier", TIER_REASONING.equals(reviewDecisionTier) ? TIER_REASONING : TIER_FAST);
            publishBroadcast(state, MSG_REVIEW_DECISION, "Review decision: " + decision.shape().wireName(), metadata);
        } catch (Exception e) {
            log.warn("Failed to publish review_decision: {}", e.getMessage());
        }
    }

    /** Publish one coordinator message of {@code messageType} on the thread's broadcast subject. */
    private void publishBroadcast(ThreadState state, String messageType, String content,
                                  Map<String, Object> metadata) throws Exception {
        var conn = natsProvider.getConnection();
        if (conn == null) return;
        var message = Map.of(
                FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                FIELD_THREAD_ID, state.threadId,
                FIELD_AGENT_NAME, properties.agentName(),
                FIELD_MESSAGE_TYPE, messageType,
                FIELD_CONTENT, content,
                FIELD_CHANNEL, CHANNEL_BROADCAST,
                FIELD_TIMESTAMP, Instant.now().toString(),
                FIELD_METADATA, metadata);
        conn.publish(broadcastSubject(state.crew, state.threadId), mapper.writeValueAsBytes(message));
    }

    /**
     * The coordinator's reasoning selected NO toolers because it can answer the
     * question itself; its brief IS the answer. Publish that brief as the synthesis
     * and close - no toolers are woken (they are not relevant), no eval/review wait,
     * and no second LLM call. Only invoked when reasoning SUCCEEDED with an empty
     * selection and a non-blank brief; a reasoning failure falls back instead.
     * See [[Coordinator Empty Selection Treated As Failure]].
     */
    // Visible for testing
    void answerDirectly(ThreadState state, ReasoningResult reasoning) {
        String channel = classifyChannelFromAdvisory(reasoning.technologies());
        state.primaryChannel = channel;
        state.advisoryContent = reasoning.brief();
        state.phase = Phase.SYNTHESIZING;
        metrics.advisoryCompleted(Duration.ofMillis(reasoning.inferenceMs()));
        log.info("Thread {} - coordinator selected no toolers; answering directly from reasoning ({}ms), no tooler wake",
                state.threadId, reasoning.inferenceMs());
        // Publish the advisory event so the dashboard thread timeline still shows the
        // coordinator's reasoning step - without it, a direct-answer thread looks like a
        // bare synthesis with no deliberation. The real latency lives on this advisory
        // metric/event (reasoning.inferenceMs); the synthesis below is a genuine ~0ms
        // no-op (no second LLM call), so its near-zero synthesisCompleted is expected,
        // not an anomaly.
        publishAdvisory(state.threadId, reasoning.technologies(), List.of(),
                reasoning.brief(), reasoning.inferenceMs());
        completeSynthesisPhase(state, channel, reasoning.brief(), Instant.now());
    }

    /**
     * REVIEW → SYNTHESIZED: LLM synthesizes final answer from all contributions.
     */
    private void transitionToSynthesis(ThreadState state) {
        state.phase = Phase.SYNTHESIZING;
        state.synthesisPromptTooLarge = false;
        Instant synthesisStart = Instant.now();
        log.info("Thread {} → SYNTHESIZING ({} agrees, {} concerns, {} stand-asides)",
                state.threadId, state.agreeSignals.size(), state.concernSignals.size(),
                state.standAsideSignals.size());
        if (closeForCapacity(state, synthesisStart)) {
            return;
        }

        llmExecutor.submit(() -> {
            try {
                // Count tooler agrees (excluding researchers like internet search)
                long synthToolerAgrees = state.agreeSignals.keySet().stream()
                        .filter(name -> !state.researcherAgents.contains(name)).count();

                // Always call LLM for synthesis — ADL rules in coordinator-decision-logic
                // PromptModule guide the LLM to answer general knowledge directly or
                // report gaps for domain-specific questions.
                String signalContext = buildSignalContext(state, synthToolerAgrees);
                // Read each spilled-artifact file and put its content into the synthesis input: toolers spill
                // bulky findings to the object store and leave only a marker on the board,
                // but the synthesizer is tool-free, so it must be handed the actual data.
                String conversation = signalContext + "\n\n" + inlineArtifactContent(formatThread(state));
                var synthesisResult = synthesizeWithCompleteness(loadSystemPrompt(), conversation, state.threadId);
                String response = synthesisResult.text();
                state.synthesisInputTokens = synthesisResult.inputTokens();
                state.synthesisOutputTokens = synthesisResult.outputTokens();

                if (response == null || response.isEmpty()) {
                    handleEmptySynthesisResponse(state);
                    return;
                }

                // ADL-FIRST: Before adding behavioral logic here, consider whether it can
                // be expressed as an ADL rule in a PromptModule CR instead. Hardcoded Java
                // is a safety net for LLM non-compliance — the primary fix belongs in ADL.
                // See: homelab-pilot/charts/.../promptmodule-coordinator.yaml
                response = handleWaitResponse(state, response);
                if (response == null) return;

                String channel = resolveChannel(state);
                response = detectAndAppendGapHint(state, response, synthToolerAgrees, channel);

                // Publish synthesis (with onboarding hint appended if gap detected)
                completeSynthesisPhase(state, channel, response, synthesisStart);

            } catch (Exception e) {
                log.warn("Synthesis failed for thread {}: {}", state.threadId, e.getMessage());
                closeThread(state, resolveChannel(state), fallbackAfterSynthesisFailure(state, e));
            }
        });
    }

    /**
     * Synthesize, then enforce the enumeration completeness contract: when the draft is
     * an inventory of an entity table that the gathered data contains, but OMITS some of
     * those entities, re-prompt with the specific omitted names and ask for the complete
     * list - up to synthesisCompletenessRetries() times. A 32B truncates a long
     * structured generation partway (listed 8 of 28 namespaces); a prompt rule alone does
     * not fix that, so the runtime names the gap and loops. It enforces only the ONE table
     * the draft is ALREADY enumerating (see enumeratedTable), so a count or free-form
     * answer, the workloads table, and prose garbage are all left alone. See B2 in
     * [[Crew Evasion - Answer From Available Data]].
     */
    ChatService.SimpleLlmResult synthesizeWithCompleteness(String systemPrompt,
            String conversation, String threadId) {
        var result = chatService.simpleLlmCallWithTokens(systemPrompt, conversation);
        int maxRetries = properties.discuss().synthesisCompletenessRetries();
        if (maxRetries <= 0) {
            return result;
        }
        var expected = enumeratedTable(expectedItemTables(conversation), result.text());
        if (expected.size() < 5) {
            return result;
        }
        var best = result;
        int bestMissing = missingNames(expected, best.text()).size();
        for (int i = 0; i < maxRetries && bestMissing > 0; i++) {
            var missing = missingNames(expected, best.text());
            log.info("Thread {} - synthesis omitted {} of {} entries, re-prompting: {}",
                    threadId, missing.size(), expected.size(), missing);
            var retry = completenessRetry(systemPrompt, conversation, best.text(), missing, threadId);
            if (retry == null) {
                break;
            }
            int retryMissing = missingNames(expected, retry.text()).size();
            if (retryMissing < bestMissing) {
                best = retry;
                bestMissing = retryMissing;
            }
        }
        return best;
    }

    /**
     * One completeness re-prompt, or null when its prompt (the conversation plus the
     * draft plus the missing names) is larger than every provider's context window:
     * the draft already in hand is kept rather than lost to a refused retry.
     */
    private ChatService.SimpleLlmResult completenessRetry(String systemPrompt, String conversation,
            String draft, List<String> missing, String threadId) {
        try {
            return chatService.simpleLlmCallWithTokens(systemPrompt,
                    completenessReprompt(conversation, draft, missing));
        } catch (ai.kubemoot.agent.provider.NoFitException nfe) {
            if (!ai.kubemoot.agent.provider.NoFitException.REASON_PROMPT_TOO_LARGE.equals(nfe.reason())) {
                throw nfe;
            }
            log.info("Thread {} - completeness re-prompt does not fit any context window, keeping the draft: {}",
                    threadId, nfe.predictorReason());
            return null;
        }
    }

    /**
     * The entity names in each kubectl-style table inlined in the synthesis input, GROUPED
     * per table: find a header row with a NAME column, then read that column from the data
     * rows until the table ends. A blank line, a section marker's following non-table line,
     * or an all-caps header WITHOUT a NAME column ends the current table - so prose after a
     * table is not read as data rows. Extracted tokens are filtered to DNS-name shape, so
     * punctuation fragments ("policies.", "(likely,") are dropped. Grouping lets the caller
     * enforce only the ONE table the draft is enumerating (see enumeratedTable), not the
     * workloads table or a stray prose "table".
     */
    static List<List<String>> expectedItemTables(String content) {
        var scan = new ItemTableScan();
        if (content != null) {
            for (String raw : content.split("\n")) {
                scan.accept(raw.strip());
            }
        }
        return scan.tables;
    }

    /** Line-by-line state for expectedItemTables: the table being read and its NAME column. */
    private static final class ItemTableScan {
        final List<List<String>> tables = new ArrayList<>();
        private List<String> current;
        private int nameIdx = -1;

        void accept(String line) {
            if (isBracketMarker(line)) {
                return; // a "[tool]" section marker precedes the header
            }
            int idx = line.isEmpty() ? -1 : nameColumnIndex(line);
            if (idx >= 0) {
                nameIdx = idx;
                current = new ArrayList<>();
                tables.add(current);
            } else if (line.isEmpty() || isAllCapsLine(line)) {
                // A table is contiguous: a blank line (prose follows) or a header
                // without a NAME column ends the current table.
                nameIdx = -1;
                current = null;
            } else {
                addDataName(current, nameIdx, line);
            }
        }
    }

    /** A line that is a single "[tool]" bracket marker preceding a table. */
    private static boolean isBracketMarker(String line) {
        return line.length() >= 2 && line.charAt(0) == '[' && line.charAt(line.length() - 1) == ']';
    }

    /** Add the NAME-column token of a data row to the current table, if it is name-shaped. */
    private static void addDataName(List<String> current, int nameIdx, String line) {
        if (nameIdx < 0 || current == null) {
            return;
        }
        String[] toks = line.split("\\s+");
        if (nameIdx < toks.length && isNameShaped(toks[nameIdx])) {
            current.add(toks[nameIdx]);
        }
    }

    /** A DNS-name-shaped token (lowercase alnum with interior hyphens/dots) - a k8s name. */
    static boolean isNameShaped(String token) {
        return token.matches("[a-z0-9]([a-z0-9.-]*[a-z0-9])?");
    }

    /** A line whose every whitespace token is all-uppercase (a column-header row). */
    static boolean isAllCapsLine(String line) {
        String[] toks = line.split("\\s+");
        if (toks.length < 2) {
            return false;
        }
        for (String t : toks) {
            if (!isAllCapsToken(t)) {
                return false;
            }
        }
        return true;
    }

    /**
     * The 0-based token index of the NAME column when the line is an all-uppercase header
     * row that carries a NAME column; -1 otherwise (a data row, or a header without NAME).
     */
    static int nameColumnIndex(String line) {
        String[] toks = line.split("\\s+");
        if (toks.length < 2) {
            return -1;
        }
        int nameAt = -1;
        for (int i = 0; i < toks.length; i++) {
            if (!isAllCapsToken(toks[i])) {
                return -1;
            }
            if (toks[i].equals("NAME")) {
                nameAt = i;
            }
        }
        return nameAt;
    }

    /** A token with at least one letter, every letter uppercase (e.g. "APIVERSION"). */
    static boolean isAllCapsToken(String s) {
        boolean hasLetter = false;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (Character.isLetter(c)) {
                hasLetter = true;
                if (!Character.isUpperCase(c)) {
                    return false;
                }
            }
        }
        return hasLetter;
    }

    /** Expected names the draft does not mention as a whole token, in order. */
    static List<String> missingNames(List<String> expected, String draft) {
        if (draft == null) {
            return new ArrayList<>(expected);
        }
        var missing = new ArrayList<String>();
        for (String n : expected) {
            if (!draftMentions(draft, n)) {
                missing.add(n);
            }
        }
        return missing;
    }

    /**
     * Whether the draft mentions the name as a WHOLE token, case-insensitive - not as a
     * substring of a longer hyphenated identifier. So a draft that lists "ollama-rig0"
     * does NOT count as mentioning "ollama", and "crew-x-y" does NOT count as "crew-x".
     * The lookarounds exclude a word char OR a hyphen on either side (a plain \\b would
     * still match at the hyphen and reintroduce the false positive).
     */
    static boolean draftMentions(String draft, String name) {
        return java.util.regex.Pattern
                .compile("(?i)(?<![\\w-])" + java.util.regex.Pattern.quote(name) + "(?![\\w-])")
                .matcher(draft).find();
    }

    /**
     * The one table the draft is ENUMERATING: among the inlined tables (each >= 5 names),
     * the one with the most of its names ALREADY present, requiring an absolute floor of
     * MIN_ENUMERATED_PRESENT present; the max present-ratio breaks toward the table the
     * draft is primarily listing. Empty when no table clears the floor - a count or
     * free-form answer (names 0-2), or the workloads table when the draft lists namespaces.
     *
     * The floor is ABSOLUTE, not a ratio: a badly-truncated inventory (say 10 of 28 listed)
     * is exactly when the contract is most needed, but 10/28 is below any majority, so a
     * ratio gate would skip it (observed 2/10 runs). 10 present clears the absolute floor
     * and is enforced, while a bare count (0-2 names) still does not. See the B2 reliability
     * edge in the Crew Evasion note.
     */
    static List<String> enumeratedTable(List<List<String>> tables, String draft) {
        if (draft == null) {
            return List.of();
        }
        List<String> best = List.of();
        double bestRatio = 0.0;
        for (var table : tables) {
            int present = table.size() - missingNames(table, draft).size();
            if (table.size() < 5 || present < MIN_ENUMERATED_PRESENT) {
                continue;
            }
            double ratio = (double) present / table.size();
            if (ratio > bestRatio) {
                bestRatio = ratio;
                best = table;
            }
        }
        return best;
    }

    /** Build the completeness re-prompt: the input, the draft, and the omitted names. */
    static String completenessReprompt(String conversation, String draft, List<String> missing) {
        return conversation + "\n\nYOUR DRAFT ANSWER:\n" + draft
                + "\n\nThat draft OMITS these entries that ARE present in the gathered data: "
                + String.join(", ", missing)
                + ".\nRe-write the COMPLETE answer including EVERY entry, one compact line each, "
                + "omitting none. Keep the entries you already have and add the missing ones.";
    }

    /**
     * The synthesis input with each spilled-artifact marker replaced by the artifact's
     * content (see {@link DiscussionArtifacts#inlineContent}).
     */
    String inlineArtifactContent(String text) {
        return DiscussionArtifacts.inlineContent(natsProvider::getConnection, text);
    }

    /**
     * Handle empty LLM response during synthesis — publish fallback and close thread.
     */
    private void handleEmptySynthesisResponse(ThreadState state) {
        log.warn("Empty LLM response for thread {} synthesis", state.threadId);
        closeThread(state, resolveChannel(state), buildFallbackResponse(state));
    }

    /**
     * Safety net: the WAIT rule is defined in the synthesis-prompt PromptModule.
     * This Java guard catches LLM non-compliance — if the LLM returns WAIT
     * despite signals existing, override it rather than silently dropping the thread.
     *
     * @return the (possibly overridden) response, or null if the thread was closed with no signals
     */
    private String handleWaitResponse(ThreadState state, String response) {
        if (!response.trim().startsWith("WAIT")) {
            return response;
        }
        boolean hasAnySignals = !state.standAsideSignals.isEmpty()
                || !state.agreeSignals.isEmpty()
                || !state.concernSignals.isEmpty()
                || !state.failureSignals.isEmpty();
        if (!hasAnySignals) {
            log.info("Thread {} synthesis returned WAIT — no signals yet", state.threadId);
            String ch = resolveChannel(state);
            publishThreadClose(state.threadId, ch);
            state.phase = Phase.CLOSED;
            completePending(state.threadId, null);
            return null;
        }
        log.info("Thread {} — WAIT overridden, {} stand-asides present, running gap detection",
                state.threadId, state.standAsideSignals.size());
        return "The toolers could not find information about this topic in the current environment.";
    }

    /**
     * Three-tier gap detection: tool-gap vs tooler-gap vs no gap.
     * Only TOOLER agrees count — researcher agrees don't prevent gap detection.
     * Appends onboarding hints to the response when a gap is detected.
     */
    private String detectAndAppendGapHint(ThreadState state, String response,
                                           long toolerAgrees, String channel) {
        var gapType = classifyGap(state, toolerAgrees, response);

        if (gapType == GapType.TOOL_GAP) {
            metrics.toolGap();
            response += "\n\n---\n"
                      + "To expand coverage for this topic, reply: `onboard <tool-domain>`\n"
                      + "You can optionally include a documentation URL: `onboard <domain> <docs-url>`";
            publishGapDetected(state.threadId, channel, state.userQuery, state.concernSignals, gapType);
        } else if (gapType == GapType.TOOLER_GAP) {
            metrics.toolerGap();
            response += "\n\n---\nTo add a tooler for this domain, reply: `onboard <domain>`\n"
                      + "You can optionally include a documentation URL: `onboard <domain> <docs-url>`";
            publishGapDetected(state.threadId, channel, state.userQuery, Map.of(), gapType);
        } else if (gapType == GapType.INFRASTRUCTURE_GAP) {
            // Toolers tried to evaluate but their tools failed. This is
            // an infrastructure problem (MCP server down, model OOM,
            // tool-loop iterations exhausted), not a missing tooler.
            // Tell the user honestly, and surface the failing tools so the
            // operator can investigate. Do NOT suggest onboarding a new
            // tooler — that would mask the real problem.
            response += "\n\n---\n"
                      + "Toolers for this topic exist but couldn't complete their evaluation"
                      + " (infrastructure failure — likely MCP tool timeout or model issue).\n"
                      + "Check operator logs and the failing MCP servers; the discussion timeline"
                      + " in the dashboard shows each failure's cause.";
            // Reuse the gap-detected publication path so observers (Onboarding,
            // future autonomic responders) see the event, with concernSignals
            // empty since this isn't a missing-coverage gap. Pass gapType so
            // the log and metadata.reason reflect INFRASTRUCTURE_GAP, not
            // mislabel as TOOLER_GAP via the old concerns-vs-empty heuristic.
            publishGapDetected(state.threadId, channel, state.userQuery, Map.of(), gapType);
        }

        return response;
    }

    // Visible for testing
    enum GapType { NONE, TOOL_GAP, TOOLER_GAP, INFRASTRUCTURE_GAP }

    // Visible for testing
    GapType classifyGap(ThreadState state, long toolerAgrees, String response) {
        boolean hasToolGapConcerns = !state.concernSignals.isEmpty();
        boolean hasFailures = !state.failureSignals.isEmpty();

        if (toolerAgrees == 0 && hasToolGapConcerns) {
            return GapType.TOOL_GAP;
        }

        // Infrastructure gap: toolers tried to evaluate but their tools/MCP
        // calls failed (failure signal). Distinct from TOOLER_GAP (no
        // relevant expertise) because the fix is to stabilise the failing
        // infrastructure, NOT to onboard a new tooler. Take precedence
        // over TOOLER_GAP when both apply — failures are a stronger
        // signal than stand-asides about what went wrong.
        if (toolerAgrees == 0 && hasFailures) {
            return GapType.INFRASTRUCTURE_GAP;
        }

        boolean isToolerGap = toolerAgrees == 0 && !hasToolGapConcerns
                && !state.standAsideSignals.isEmpty();

        if (!isToolerGap && state.triageConfidence >= 0 && state.triageConfidence < 0.3) {
            log.info("Thread {} — triage confidence {}, flagging as tooler gap",
                    state.threadId, state.triageConfidence);
            isToolerGap = true;
        }

        if (isToolerGap && !looksLikeGapReport(response)) {
            log.info("Thread {} — LLM answered directly (general knowledge), suppressing gap hint",
                    state.threadId);
            isToolerGap = false;
        }

        if (isToolerGap || suggestsOnboarding(response)) {
            return GapType.TOOLER_GAP;
        }

        return GapType.NONE;
    }

    /**
     * Publish synthesis, close the thread, record metrics, and complete the pending future.
     */
    private void completeSynthesisPhase(ThreadState state, String channel, String response,
                                         Instant synthesisStart) {
        metrics.synthesisCompleted(Duration.between(synthesisStart, Instant.now()));
        log.info("Thread {} → CLOSED (synthesized)", state.threadId);
        closeThread(state, channel, response);
    }

    /**
     * The single terminal-close path. EVERY way a discussion ends - normal synthesis,
     * empty-response fallback, and the hard-ceiling watchdog - funnels through here so
     * the steps (publish the synthesis answer, close the thread, mark CLOSED, record
     * completion, complete the caller's future) cannot drift apart. Drift between two
     * copies of a close path is the analyst-drop class of bug the project warns about.
     * The caller records any phase-specific metric (e.g. synthesis duration) first.
     * Idempotent: completePending no-ops if the future is already completed.
     */
    private void closeThread(ThreadState state, String channel, String response) {
        publishToThread(state.threadId, channel, response, MSG_SYNTHESIS);
        publishThreadClose(state.threadId, channel);
        state.phase = Phase.CLOSED;
        metrics.threadCompleted(Duration.between(state.threadCreated, Instant.now()));
        completePending(state.threadId, response);
    }

    /**
     * The answer when the synthesis call failed: the fallback, naming a synthesis
     * prompt larger than every GPU's context window when that was the cause.
     */
    String fallbackAfterSynthesisFailure(ThreadState state, Exception failure) {
        state.synthesisPromptTooLarge = isPromptTooLarge(failure);
        return buildFallbackResponse(state);
    }

    /**
     * Build a fallback response from tooler agrees, or a generic error message.
     */
    // Visible for testing
    String buildFallbackResponse(ThreadState state) {
        if (!state.agreeSignals.isEmpty()) {
            var fb = new StringBuilder(state.synthesisPromptTooLarge ? SYNTHESIS_TOO_LARGE_PREFIX : "");
            fb.append("Here's what the toolers found:\n\n");
            for (var agree : state.agreeSignals.entrySet()) {
                fb.append("**").append(agree.getKey()).append("**: ")
                        .append(truncate(agree.getValue(), FALLBACK_SNIPPET_CHARS)).append("\n\n");
            }
            return fb.toString();
        }
        String capacity = capacityMessage(state);
        if (capacity != null) {
            return capacity;
        }
        if (state.synthesisPromptTooLarge) {
            return PROMPT_TOO_LARGE_MESSAGE;
        }
        return "No agent contributed an answer and the coordinator could not "
                + "synthesize a response. The crew's agents may not cover this "
                + "topic, or may lack the tools to answer it. Try rephrasing, or add an "
                + "agent or tool for this area.";
    }

    /** Leads the toolers' raw findings when the synthesis prompt fit no GPU's context window. */
    static final String SYNTHESIS_TOO_LARGE_PREFIX = "The findings below are too large together for the "
            + "context window any GPU in this cluster gives the coordinator's model, so they are shown "
            + "without a combined answer.\n\n";

    /** True when {@code e} is a refusal because the prompt fits no provider's context window. */
    static boolean isPromptTooLarge(Throwable e) {
        return e instanceof ai.kubemoot.agent.provider.NoFitException nfe
                && REASON_PROMPT_TOO_LARGE.equals(nfe.reason());
    }

    static final String PROMPT_TOO_LARGE_MESSAGE = "The question and the evidence gathered for it are larger "
            + "than the context window any GPU in this cluster gives the agents' models, so they could not "
            + "answer without losing part of it. Ask a narrower question, or give the Ollama server a larger "
            + "per-request context (OLLAMA_CONTEXT_LENGTH).";

    static final String GPU_BUSY_MESSAGE = "The crew's agents could not get a GPU: every GPU was busy "
            + "with other work, so none of them could answer in time. This is the cluster's capacity, "
            + "not the crew's design; ask again in a moment.";

    /**
     * The answer when no agent contributed because of GPU capacity: at least one
     * agent stood aside with reason gpu-busy, model-too-large or prompt-too-large, or was still
     * waiting for a GPU when the discussion settled. Null when an agent
     * contributed (agree or concern) or no capacity reason was reported, so the
     * ordinary synthesis and fallback text apply.
     */
    // Visible for testing
    static String capacityMessage(ThreadState state) {
        if (!state.agreeSignals.isEmpty() || !state.concernSignals.isEmpty()) {
            return null;
        }
        var parts = new ArrayList<String>();
        if (state.capacityStandAsides.containsValue(REASON_MODEL_TOO_LARGE)) {
            parts.add(tooLargeMessage(state.tooLargeModels));
        }
        if (state.capacityStandAsides.containsValue(REASON_PROMPT_TOO_LARGE)) {
            parts.add(PROMPT_TOO_LARGE_MESSAGE);
        }
        if (state.capacityStandAsides.containsValue(REASON_GPU_BUSY) || !state.waitingAgents.isEmpty()) {
            parts.add(GPU_BUSY_MESSAGE);
        }
        return parts.isEmpty() ? null : String.join(" ", parts);
    }

    private static String tooLargeMessage(Set<String> models) {
        String named = models.isEmpty() ? "the model" : "the model " + String.join(", ", new java.util.TreeSet<>(models));
        return "No GPU in this cluster can hold " + named + " the agents need; add a smaller Model or a larger GPU.";
    }

    /**
     * Close the thread with the capacity answer instead of running the synthesis
     * LLM when no agent contributed because of GPU capacity. Returns true when it
     * closed the thread.
     */
    private boolean closeForCapacity(ThreadState state, Instant synthesisStart) {
        String capacity = capacityMessage(state);
        if (capacity == null) {
            return false;
        }
        log.info("Thread {} - no contribution and agents could not get a GPU; answering with the capacity message",
                state.threadId);
        completeSynthesisPhase(state, resolveChannel(state), capacity, synthesisStart);
        return true;
    }

    /**
     * Resolve the primary channel for a thread, defaulting to "general".
     */
    // Visible for testing
    static String resolveChannel(ThreadState state) {
        return state.primaryChannel != null ? state.primaryChannel : CHANNEL_GENERAL;
    }

    /**
     * Handle waking/ready signals from agents starting from scale-to-zero.
     * These signals have empty threadId — apply to all active threads in EVALUATING or ADVISORY phase.
     */
    private void handleWakeUpSignal(String agentName, String signalType) {
        if (MSG_WAKING.equals(signalType)) {
            applyWakingSignal(agentName);
        } else if (MSG_READY.equals(signalType)) {
            applyReadySignal(agentName);
        }
    }

    /** A waking agent (scaling from zero) gets a generous 60s deadline on its active threads. */
    private void applyWakingSignal(String agentName) {
        long wakingDeadline = System.currentTimeMillis() + WAKING_DEADLINE_MS;
        int affected = 0;
        for (ThreadState state : threads.values()) {
            if (shouldTrackWaking(state, agentName)) {
                state.pendingEvaluations.put(agentName, wakingDeadline);
                affected++;
            }
        }
        log.info("Agent {} is waking from scale-to-zero — added to {} active threads (deadline 60s)",
                agentName, affected);
    }

    /** A ready agent (cold start finished) returns to the normal triage-model deadline. */
    private void applyReadySignal(String agentName) {
        long triageDeadline = System.currentTimeMillis()
                + (properties.triageModel().timeoutSeconds() * 1000L);
        int affected = 0;
        for (ThreadState state : threads.values()) {
            if (state.pendingEvaluations.containsKey(agentName)) {
                state.pendingEvaluations.put(agentName, triageDeadline);
                affected++;
            }
        }
        log.info("Agent {} is ready after cold start — resumed normal timing on {} threads", agentName, affected);
    }

    /**
     * Whether a waking agent should be tracked on this thread. Only EVALUATING/ADVISORY
     * threads count, and when an inner circle is set, only its members - non-members are
     * silently excluded by the subscriber and never publish a terminal signal, so tracking
     * them would make the coordinator wait out their deadline (3+ min wasted).
     */
    static boolean shouldTrackWaking(ThreadState state, String agentName) {
        if (state.phase != Phase.EVALUATING && state.phase != Phase.ADVISORY) return false;
        return state.innerCircle == null || state.innerCircle.isEmpty()
                || state.innerCircle.contains(agentName);
    }

    /**
     * Returns true when no agent has an active (non-expired) evaluating signal for this thread.
     * The settle timer MUST NOT fire while agents are still doing tool-calling work.
     */
    private boolean canSettle(ThreadState state) {
        long now = System.currentTimeMillis();
        boolean anyPending = state.pendingEvaluations.values().stream()
                .anyMatch(deadline -> deadline > now);
        if (anyPending) {
            log.debug("Thread {} settle blocked — {} agents still evaluating",
                    state.threadId, state.pendingEvaluations.size());
            return false;
        }
        return true;
    }

    /**
     * Minimum tooler agrees that justify concluding while stragglers are
     * still pending. Two concurring toolers with no open concern is enough
     * signal to answer; a lone agree still waits for peers (it might be wrong
     * and a peer might raise a concern). Constant for now; could become a
     * {@code kubemoot.discuss.*} property if domains need to tune it.
     */
    static final int SUFFICIENT_AGREES_THRESHOLD = 2;

    /** Count of agreeing agents that are NOT researchers (researchers don't gate settle). */
    // Visible for testing
    long toolerAgreeCount(ThreadState state) {
        return state.agreeSignals.keySet().stream()
                .filter(name -> !state.researcherAgents.contains(name))
                .count();
    }

    /**
     * True when the discussion has enough substantive agreement to conclude
     * even if some agents are still pending (heartbeating stragglers). Requires:
     * <ul>
     *   <li>≥ {@link #SUFFICIENT_AGREES_THRESHOLD} tooler agrees,</li>
     *   <li>no open concerns or blocks (never abandon a concern mid-flight), and</li>
     *   <li>substantively quiet for {@code settleSeconds} — {@code lastSignalReceived}
     *       is updated only by TERMINAL signals, not heartbeats, so a straggler
     *       that keeps heartbeating does not keep this fresh.</li>
     * </ul>
     * Dropped stragglers' late signals are ignored once the phase advances.
     */
    // Visible for testing
    boolean hasSufficientConsensus(ThreadState state, Instant now) {
        if (!state.concernSignals.isEmpty() || !state.blockSignals.isEmpty()) return false;
        if (toolerAgreeCount(state) < SUFFICIENT_AGREES_THRESHOLD) return false;
        return state.lastSignalReceived != null
                && Duration.between(state.lastSignalReceived, now).getSeconds() >= settleSeconds;
    }

    /**
     * Scan for evaluating agents whose deadline has passed without a terminal signal.
     *
     * FIX #3 of three (2026-05-25): synthesize a {@code failure} signal,
     * not a {@code stand_aside}. An agent that runs past its deadline
     * without producing a terminal signal is, by definition, a failure
     * to complete — distinct from a deliberate stand_aside. Surfacing
     * it as failure lets GapDetector classify it as INFRASTRUCTURE_GAP
     * (existing logic) and the dashboard show it with the distinct
     * `failure` pill rather than the misleading `stand_aside` pill.
     *
     * Counts the timeout in {@code failureSignals} (not standAsideSignals)
     * so the existing settle/gap math sees it correctly.
     */
    private void checkEvaluationTimeouts(ThreadState state) {
        long now = System.currentTimeMillis();
        state.pendingEvaluations.entrySet().removeIf(entry -> {
            if (entry.getValue() < now) {
                String timedOutAgent = entry.getKey();
                log.warn("Thread {} — agent {} evaluation timed out, synthesizing failure",
                        state.threadId, timedOutAgent);
                state.failureSignals.put(timedOutAgent,
                        "Evaluation did not complete within estimated window");
                state.lastSignalReceived = Instant.now();
                metrics.evaluationTimedOut();
                publishSyntheticFailure(timedOutAgent, state.threadId,
                        state.primaryChannel != null ? state.primaryChannel : CHANNEL_GENERAL);
                return true;
            }
            return false;
        });
        // Update gauge after all removals are complete
        metrics.setPendingEvaluations(state.pendingEvaluations.size());
    }

    /**
     * Publish a synthetic {@code failure} message on behalf of an agent
     * whose evaluation deadline expired without a terminal signal.
     * Carries failureType=evaluation_timeout so the dashboard and
     * GapDetector can distinguish coordinator-synthesized failures from
     * agent-published ones. Best-effort; never throws.
     */
    private void publishSyntheticFailure(String agentName, String threadId, String channel) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var meta = new HashMap<String, Object>();
            meta.put("failureType", "evaluation_timeout");
            meta.put("synthesizedBy", "coordinator");
            meta.put("lastError",
                    "Evaluation did not complete within the deadline; agent stopped responding");

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, agentName,
                    FIELD_MESSAGE_TYPE, MSG_FAILURE,
                    FIELD_CONTENT, "Evaluation timed out — agent did not respond within the deadline",
                    FIELD_CHANNEL, channel,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, meta
            );

            String crew = threads.containsKey(threadId) ? threads.get(threadId).crew : null;
            String subject = channelSubject(crew, channel, threadId);
            conn.publish(subject, mapper.writeValueAsBytes(message));
            log.info("Published synthetic failure for timed-out agent {} on thread {}", agentName, threadId);

        } catch (Exception e) {
            log.debug("Failed to publish synthetic failure for {}: {}", agentName, e.getMessage());
        }
    }

    /**
     * Extract the gpuLabel from a signal's metadata — used as the provider key for LatencyTracker.
     * Falls back to "unknown" if metadata is absent.
     */
    private static String extractProvider(JsonNode msg) {
        if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has(FIELD_GPU_LABEL)) {
            String label = msg.get(FIELD_METADATA).get(FIELD_GPU_LABEL).asText();
            return label != null && !label.isEmpty() ? label : "unknown";
        }
        return "unknown";
    }

    /**
     * Record observed latency for an agent after a terminal signal arrives.
     * Uses inferenceMs from signal metadata when available; skips when not present.
     *
     * @param deadline the deadline that was set when the evaluating signal arrived,
     *                 or null if this terminal signal had no preceding evaluating signal
     */
    private void recordLatencyFromSignal(String agentName, JsonNode msg, Long deadline) {
        if (deadline == null) return; // No evaluating signal preceded this — nothing to record
        try {
            if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has(FIELD_INFERENCE_MS)) {
                long inferenceMs = msg.get(FIELD_METADATA).get(FIELD_INFERENCE_MS).asLong();
                if (inferenceMs > 0) {
                    String provider = extractProvider(msg);
                    latencyTracker.recordLatency(agentName, provider, inferenceMs);
                }
            }
        } catch (Exception e) {
            log.debug("Failed to record latency for {}: {}", agentName, e.getMessage());
        }
    }

    private void completePending(String threadId, String response) {
        var future = pendingResults.remove(threadId);
        if (future != null) {
            if (response != null) {
                future.complete(response);
            } else {
                future.complete(null);
            }
        }
    }

    private String formatThread(ThreadState state) {
        var sb = new StringBuilder();
        sb.append("Discussion thread ").append(state.threadId).append("\n");
        sb.append("Channel: ").append(state.primaryChannel != null ? state.primaryChannel : CHANNEL_GENERAL).append("\n\n");

        for (var msg : state.messages) {
            String label = switch (msg.messageType) {
                case MSG_THREAD_START -> "[USER QUESTION]";
                case MSG_ADVISORY -> "[ADVISORY from " + msg.agentName + "]";
                case MSG_ADVISORY_READY -> "[ADVISORY READY]";
                case MSG_REVIEW_READY -> "[REVIEW READY]";
                case "agree", "contribution" -> "[RESPONSE from " + msg.agentName + "]";
                case "concern" -> "[CONCERN from " + msg.agentName + "]";
                case "block" -> "[BLOCK from " + msg.agentName + "]";
                case MSG_STAND_ASIDE, "decline" -> "[STAND ASIDE from " + msg.agentName + "]";
                case "proposal" -> "[PROPOSAL from " + msg.agentName + "]";
                case "reply" -> "[USER REPLY]";
                case "follow_up" -> "[FACILITATOR FOLLOW-UP]";
                case MSG_SYNTHESIS -> "[SYNTHESIS]";
                default -> "[" + msg.messageType.toUpperCase() + " from " + msg.agentName + "]";
            };

            sb.append(label).append("\n");
            if (!msg.content.isEmpty()) {
                sb.append(msg.content).append("\n");
            }
            sb.append("\n");
        }

        return sb.toString();
    }

    /**
     * Build a signal context preamble for synthesis. Provides structured data
     * so the LLM can evaluate ADL decision rules (e.g., "WHEN 0 toolers contributed").
     */
    private String buildSignalContext(ThreadState state, long toolerAgrees) {
        var sb = new StringBuilder();
        sb.append("Discussion signals:\n");
        sb.append("- Tooler agrees: ").append(toolerAgrees);
        if (toolerAgrees > 0) {
            var toolers = state.agreeSignals.keySet().stream()
                    .filter(name -> !state.researcherAgents.contains(name))
                    .toList();
            sb.append(" (").append(String.join(", ", toolers)).append(")");
        }
        sb.append("\n");

        long researcherAgrees = state.agreeSignals.keySet().stream()
                .filter(state.researcherAgents::contains).count();
        sb.append("- Researcher agrees: ").append(researcherAgrees);
        if (researcherAgrees > 0) {
            var researchers = state.agreeSignals.keySet().stream()
                    .filter(state.researcherAgents::contains)
                    .toList();
            sb.append(" (").append(String.join(", ", researchers)).append(")");
        }
        sb.append("\n");

        sb.append("- Stand-asides: ").append(state.standAsideSignals.size()).append("\n");
        sb.append("- Concerns: ").append(state.concernSignals.size()).append("\n");
        sb.append("- Blocks: ").append(state.blockSignals.size()).append("\n");
        sb.append("Original question: \"").append(state.userQuery).append("\"");
        return sb.toString();
    }

    /**
     * Load system prompt from file (composed by operator from PromptModules).
     * Cached after first successful read.
     */
    private String loadSystemPrompt() {
        if (systemPromptCache != null) return systemPromptCache;

        var promptFile = properties.systemPromptFile().orElse("");
        if (!promptFile.isEmpty()) {
            try {
                systemPromptCache = Files.readString(Path.of(promptFile));
                log.info("Loaded system prompt from file: {} ({} chars)", promptFile, systemPromptCache.length());
                return systemPromptCache;
            } catch (IOException e) {
                log.warn("Failed to read system prompt file {}: {}", promptFile, e.getMessage());
            }
        }

        // Fallback to inline prompt if no PromptModules configured
        return properties.systemPrompt().orElse("");
    }

    /**
     * Build the USER message for the combined select+brief reasoning call: DATA ONLY.
     *
     * The methodology (how to select the subcommittee, the additive-only framing rule,
     * the Prometheus counterexample, what the brief must contain) lives in the
     * coordinator PromptModule, delivered as the system prompt via loadSystemPrompt().
     * That PromptModule is the ADL-vs-prose experiment variable. Keeping the
     * instructions out of Java honors "all prompt text lives in PromptModules" and
     * keeps the two crews differing ONLY by prompt form, not by hardcoded Java text.
     *
     * This method assembles only what the coordinator reasons over: conversation
     * context, the user question, and the capability catalog, plus a one-line reminder
     * of the JSON shape. Output is parsed via readTree (never records/readValue).
     */
    private String buildReasoningSelectPrompt(String userQuery, String resumes, ThreadState state) {
        var sb = new StringBuilder();

        // Conversation context so the coordinator can resolve follow-up references.
        var context = buildConversationContext(state.conversationId);
        if (!context.isEmpty()) {
            sb.append("Previous conversation turns (for follow-up context):\n");
            for (var turn : context) {
                @SuppressWarnings("unchecked")
                var turnMap = (Map<String, String>) turn;
                sb.append("Q: ").append(turnMap.get(FIELD_QUERY)).append("\n");
                sb.append("A: ").append(truncate(turnMap.get(FIELD_RESPONSE), CONTEXT_RESPONSE_CHARS)).append("\n\n");
            }
        }

        sb.append("User question: \"").append(userQuery).append(QUOTE_BLANK_LINE);
        sb.append("Available crew (capability catalog; toolers gather data, [analyst] agents cross-check in review):\n");
        sb.append(resumes).append("\n\n");
        // Output-shape reminder ONLY. How to select and what the brief must contain are
        // defined in your system prompt (the coordinator PromptModule).
        sb.append("Follow your instructions and respond with one JSON object, no markdown fences:\n");
        // The skills field is offered only when the crew actually has skills, so a
        // crew with zero skills sees a coordinator prompt byte-identical to baseline.
        if (knownSkillNames.get().isEmpty()) {
            sb.append("{\"selected\": [agent names], \"brief\": \"...\", \"technologies\": [...]}");
        } else {
            sb.append("{\"selected\": [agent names], \"brief\": \"...\", \"technologies\": [...], \"skills\": [skill names or empty]}");
        }

        return sb.toString();
    }

    /**
     * Legacy advisory prompt - used only in the fallback path when the primary
     * reasoning select+brief call fails. Produces the old
     * {"technologies": [...], "layers": [...], "wisdom": "..."} contract.
     * Kept to preserve fallback channel classification and runTriage input.
     */
    private String buildLegacyAdvisoryPrompt(String userQuery, ThreadState state) {
        var sb = new StringBuilder();
        sb.append("Given this user query about a homelab/cloud-native environment, provide a brief advisory.\n\n");

        // Include conversation context so advisory understands follow-up references
        var context = buildConversationContext(state.conversationId);
        if (!context.isEmpty()) {
            sb.append("Previous conversation turns (for context on follow-up references):\n");
            for (var turn : context) {
                @SuppressWarnings("unchecked")
                var turnMap = (Map<String, String>) turn;
                sb.append("Q: ").append(turnMap.get(FIELD_QUERY)).append("\n");
                sb.append("A: ").append(truncate(turnMap.get(FIELD_RESPONSE), CONTEXT_RESPONSE_CHARS)).append("\n\n");
            }
        }

        sb.append("User query: \"").append(userQuery).append(QUOTE_BLANK_LINE);
        sb.append("Questions may span multiple layers — this is normal, not ambiguous. ");
        sb.append("Include all relevant layers so every tooler can contribute their piece.\n\n");
        sb.append("If this is a follow-up question that references prior answers, ");
        sb.append("expand the technologies list to include ALL domains touched by the full conversation.\n\n");
        sb.append("Respond in JSON only, no markdown fences:\n");
        sb.append("{\"technologies\": [\"tech1\", \"tech2\"], \"layers\": [\"layer1\", \"layer2\"], ");
        sb.append("\"wisdom\": \"Brief advisory explaining how layers relate\"}");

        return sb.toString();
    }

    // Content as published for a message type. The synthesis is the user-facing
    // terminal answer, so it is delivered WHOLE (no 5000-char bus truncation) with
    // internal artifact-spill markers stripped; every other message type keeps the
    // bus-hygiene cap. See [[Measurement Integrity - Grade Inflation and Fabrication]].
    static String publishedContent(String messageType, String content) {
        if (!MSG_SYNTHESIS.equals(messageType)) {
            return truncate(content, PUBLISHED_CONTENT_CHARS);
        }
        String cleaned = ARTIFACT_SPILL_MARKER.matcher(content == null ? "" : content).replaceAll("");
        return truncate(cleaned, SYNTHESIS_CONTENT_CHARS);
    }

    private void publishToThread(String threadId, String channel, String content, String messageType) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, messageType,
                    FIELD_CONTENT, publishedContent(messageType, content),
                    FIELD_CHANNEL, channel,
                    FIELD_TIMESTAMP, Instant.now().toString()
            );

            byte[] payload = mapper.writeValueAsBytes(message);

            // Synthesis must reach ALL agents (not just the primary channel) so they
            // add the threadId to closedThreads and stop heartbeating.
            String crew = threads.containsKey(threadId) ? threads.get(threadId).crew : null;
            if (MSG_SYNTHESIS.equals(messageType)) {
                conn.publish(broadcastSubject(crew, threadId), payload);
                if (!CHANNEL_BROADCAST.equals(channel)) {
                    conn.publish(channelSubject(crew, channel, threadId), payload);
                }
            } else {
                conn.publish(channelSubject(crew, channel, threadId), payload);
            }
            log.info("Published {} to {} for thread {}", messageType,
                    MSG_SYNTHESIS.equals(messageType) ? "broadcast+" + channel : channel, threadId);

        } catch (Exception e) {
            log.debug("Failed to publish to thread: {}", e.getMessage());
        }
    }

    // Visible for testing
    boolean suggestsOnboarding(String response) {
        if (response == null) return false;
        var lower = response.toLowerCase();
        return lower.contains("onboard") || lower.contains("new tooler")
                || lower.contains("new mcp") || lower.contains("no tooler");
    }

    /**
     * Check if the LLM response looks like a gap report (unable to find info)
     * vs a direct answer (general knowledge). Used to suppress onboarding hints
     * when the coordinator answered directly via ADL decision rules.
     */
    // Visible for testing
    boolean looksLikeGapReport(String response) {
        if (response == null) return false;
        var lower = response.toLowerCase();
        return lower.contains("unable to find") || lower.contains("could not find")
                || lower.contains("no information") || lower.contains("not covered")
                || lower.contains("no tooler") || lower.contains("isn't covered")
                || lower.contains("cannot determine") || lower.contains("don't have access");
    }

    private void publishGapDetected(String threadId, String channel, String userQuery,
                                     Map<String, String> concerns, GapType gapType) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            // Metadata reflects the actual GapType — distinct messages for
            // TOOL_GAP (toolers named the missing tool via concern),
            // INFRASTRUCTURE_GAP (toolers tried but their tools/MCP
            // failed), and TOOLER_GAP (no relevant expertise at the
            // table). Observed 2026-05-26 that the previous binary
            // "concerns vs no concerns" mis-labelled INFRASTRUCTURE_GAP
            // as TOOLER_GAP in both the log AND the gap_detected
            // metadata — confusing dashboards and downstream consumers.
            var metadata = new HashMap<String, Object>();
            metadata.put("gapType", gapType.name());
            switch (gapType) {
                case TOOL_GAP -> {
                    metadata.put(FIELD_REASON, "Toolers exist but lack tools");
                    if (concerns != null && !concerns.isEmpty()) {
                        metadata.put("toolGapConcerns", concerns);
                    }
                }
                case INFRASTRUCTURE_GAP -> {
                    metadata.put(FIELD_REASON,
                            "Toolers exist but their tools/MCP/model failed (infrastructure issue)");
                }
                case TOOLER_GAP -> {
                    metadata.put(FIELD_REASON, "No tooler could answer the query");
                }
                default -> metadata.put(FIELD_REASON, "Gap detected");
            }

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, "gap_detected",
                    FIELD_CONTENT, userQuery != null ? userQuery : "",
                    FIELD_CHANNEL, channel,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, metadata
            );

            String crew = threads.containsKey(threadId) ? threads.get(threadId).crew : null;
            String subject = broadcastSubject(crew, threadId);
            conn.publish(subject, mapper.writeValueAsBytes(message));
            log.info("Published gap_detected ({}) for thread {} to trigger onboarding evaluation",
                    gapType.name().toLowerCase().replace("_", "-"),
                    threadId);

        } catch (Exception e) {
            log.debug("Failed to publish gap_detected: {}", e.getMessage());
        }
    }

    private void publishThreadClose(String threadId, String channel) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            // Build token totals metadata for observability
            var tokenMetadata = new HashMap<String, Object>();
            var state = threads.get(threadId);
            if (state != null) {
                tokenMetadata.put("totalTokens", Map.of(
                        MSG_ADVISORY, Map.of(TOKENS_IN, state.advisoryInputTokens, TOKENS_OUT, state.advisoryOutputTokens),
                        "triage", Map.of(TOKENS_IN, state.triageInputTokens, TOKENS_OUT, state.triageOutputTokens),
                        MSG_SYNTHESIS, Map.of(TOKENS_IN, state.synthesisInputTokens, TOKENS_OUT, state.synthesisOutputTokens)
                ));
            }

            var close = new HashMap<String, Object>();
            close.put(FIELD_MESSAGE_ID, UUID.randomUUID().toString());
            close.put(FIELD_THREAD_ID, threadId);
            close.put(FIELD_AGENT_NAME, properties.agentName());
            close.put(FIELD_MESSAGE_TYPE, MSG_THREAD_CLOSE);
            close.put(FIELD_CONTENT, "");
            close.put(FIELD_CHANNEL, channel);
            close.put(FIELD_TIMESTAMP, Instant.now().toString());
            if (!tokenMetadata.isEmpty()) {
                close.put(FIELD_METADATA, tokenMetadata);
            }

            byte[] payload = mapper.writeValueAsBytes(close);

            // Publish to broadcast so ALL agents see thread_close regardless of channel.
            // thread_start goes to broadcast — thread_close must too, otherwise agents on
            // other channels never learn the thread is closed and heartbeats continue forever.
            String crew = threads.containsKey(threadId) ? threads.get(threadId).crew : null;
            conn.publish(broadcastSubject(crew, threadId), payload);

            // Also publish to the primary channel for channel-specific consumers
            if (!CHANNEL_BROADCAST.equals(channel)) {
                conn.publish(channelSubject(crew, channel, threadId), payload);
            }
        } catch (Exception e) {
            log.debug("Failed to publish thread close: {}", e.getMessage());
        }
    }

    // --- Triage subcommittee selection ---

    /** Parsed result from the triage LLM call. */
    record TriageResult(List<SelectedAgent> agents, double overallConfidence) {
        record SelectedAgent(String name, double confidence, String reason) {}
    }

    /**
     * Run the triage LLM call to select a subcommittee of agents for this thread.
     * On failure, logs a warning and proceeds without inner circle (all agents participate).
     *
     * When a resume search endpoint is configured, attempts semantic pre-filtering first:
     * the user query is embedded and matched against agent resume vectors in pgvector.
     * If semantic search returns results, only those pre-selected resumes are sent to the
     * LLM for final triage, reducing prompt size and improving relevance.
     * Falls back to the full ConfigMap-loaded resumes on any error.
     */
    private void runTriage(ThreadState state, List<String> technologies, String wisdom) {
        var resumeData = loadResumesForTriage(state);
        if (resumeData.resumes == null || resumeData.resumes.isEmpty()) {
            log.info("Thread {} — no crew resumes available, skipping triage (all agents participate)",
                    state.threadId);
            return;
        }

        try {
            String triagePrompt = buildTriagePrompt(state.userQuery, technologies, wisdom,
                    resumeData.resumes, state, resumeData.semanticPreFiltered);
            String systemPrompt = loadSystemPrompt();

            long triageStart = System.currentTimeMillis();
            // Subcommittee selection is classification, not reasoning — run it on the
            // fast triage model (model tiering) when configured; else the default.
            var triageResult = chatService.hasDistinctTriageModel()
                    ? chatService.triageChatWithTokens(systemPrompt, triagePrompt)
                    : chatService.simpleLlmCallWithTokens(systemPrompt, triagePrompt);
            long triageMs = System.currentTimeMillis() - triageStart;

            recordTriageMetrics(state, triageResult, triageMs);
            applyTriageResult(state, triageResult, triageMs);

        } catch (Exception e) {
            log.warn("Thread {} — triage failed, all agents participate: {}", state.threadId, e.getMessage());
        }
    }

    private record ResumeData(String resumes, boolean semanticPreFiltered) {}

    private ResumeData loadResumesForTriage(ThreadState state) {
        if (resumeSearchClient != null) {
            try {
                var searchResults = resumeSearchClient.searchResumes(state.userQuery, 5);
                if (searchResults != null && !searchResults.isEmpty()) {
                    log.info("Thread {} — using {} semantically pre-filtered resumes for triage",
                            state.threadId, searchResults.size());
                    extractAgentNamesFromSearchResults(searchResults);
                    return new ResumeData("[" + String.join(",", searchResults) + "]", true);
                }
            } catch (Exception e) {
                log.debug("Thread {} — semantic resume search failed, falling back to ConfigMap: {}",
                        state.threadId, e.getMessage());
            }
        }
        return new ResumeData(loadCrewResumes(), false);
    }

    private void recordTriageMetrics(ThreadState state, ChatService.SimpleLlmResult triageResult, long triageMs) {
        state.triageInputTokens = triageResult.inputTokens();
        state.triageOutputTokens = triageResult.outputTokens();
        metrics.triageCompleted(Duration.ofMillis(triageMs), 0);
        metrics.recordCoordinatorTokens(
                state.advisoryInputTokens + triageResult.inputTokens(),
                state.advisoryOutputTokens + triageResult.outputTokens());
    }

    private void applyTriageResult(ThreadState state, ChatService.SimpleLlmResult triageResult, long triageMs) {
        var parsed = parseTriageResult(triageResult.text());
        if (parsed == null) {
            log.warn("Thread {} — triage returned unparseable JSON, all agents participate. Response: {}",
                    state.threadId, truncate(triageResult.text(), LOG_PREVIEW_CHARS));
            return;
        }

        var validAgents = validateAgentNames(parsed.agents());
        state.innerCircle = ConcurrentHashMap.newKeySet();
        for (var agent : validAgents) {
            state.innerCircle.add(agent.name());
        }
        state.triageConfidence = parsed.overallConfidence();

        metrics.triageCompleted(Duration.ofMillis(triageMs), validAgents.size());

        log.info("Thread {} — triage selected {} agents (confidence={}) in {}ms: {}",
                state.threadId, validAgents.size(), String.format("%.2f", parsed.overallConfidence()),
                triageMs, state.innerCircle);

        publishTriageResult(state, validAgents, parsed.overallConfidence(), triageMs,
                triageResult.inputTokens(), triageResult.outputTokens());
    }

    /**
     * Extract known agent names from semantic search results for validation.
     */
    private List<String> extractAgentNamesFromSearchResults(List<String> searchResults) {
        var names = new ArrayList<String>();
        for (var content : searchResults) {
            // Content is resume text formatted as "Agent: {name}\nDescription: ..."
            // produced by BuildResumeText() in the operator.
            // Also try JSON parsing as fallback if the content is JSON.
            if (content.startsWith("Agent: ")) {
                var firstLine = content.split("\n")[0];
                var name = firstLine.substring("Agent: ".length()).trim();
                if (!name.isEmpty()) {
                    names.add(name);
                }
            } else {
                try {
                    var node = mapper.readTree(content);
                    if (node.has(FIELD_NAME)) {
                        names.add(node.get(FIELD_NAME).asText());
                    }
                } catch (Exception e) {
                    log.debug("Could not parse resume content: {}", content.substring(0, Math.min(RESUME_PARSE_LOG_CHARS, content.length())));
                }
            }
        }
        if (!names.isEmpty()) {
            knownAgentNames.set(List.copyOf(names));
        }
        return names;
    }

    /**
     * Build the triage prompt that asks the LLM to select a subcommittee.
     * When semanticPreFiltered is true, the prompt notes that resumes were pre-selected
     * by semantic relevance to help the LLM focus on the best candidates.
     */
    private String buildTriagePrompt(String userQuery, List<String> technologies,
                                      String wisdom, String resumes, ThreadState state,
                                      boolean semanticPreFiltered) {
        var sb = new StringBuilder();
        sb.append("Select which tooler agents should form the subcommittee for this discussion.\n\n");

        // Include conversation context for follow-up awareness
        var context = buildConversationContext(state.conversationId);
        if (!context.isEmpty()) {
            sb.append("Previous conversation:\n");
            for (var turn : context) {
                sb.append("Q: ").append(turn.get(FIELD_QUERY)).append("\n");
                sb.append("A: ").append(truncate(turn.get(FIELD_RESPONSE), SELECTION_CONTEXT_RESPONSE_CHARS)).append("\n\n");
            }
        }

        sb.append("User question: \"").append(userQuery).append(QUOTE_BLANK_LINE);
        sb.append("Advisory:\n");
        sb.append("- Technologies: ").append(technologies != null ? String.join(", ", technologies) : "none").append("\n");
        sb.append("- Wisdom: ").append(wisdom != null ? wisdom : "").append("\n\n");
        if (semanticPreFiltered) {
            sb.append("Agent resumes (pre-selected by semantic relevance):\n");
        } else {
            sb.append("Available agent resumes:\n");
        }
        sb.append(resumes).append("\n\n");
        sb.append("Select agents whose tools or expertise match the question. ");
        sb.append("Respond in JSON only, no markdown fences:\n");
        sb.append("{\"agents\": [{\"name\": \"agent-name\", \"confidence\": 0.9, ");
        sb.append("\"reason\": \"Has resources_list for Namespace\"}], \"overallConfidence\": 0.85}");

        return sb.toString();
    }

    /**
     * Parse triage LLM response into a TriageResult.
     * Returns null if the response is not valid JSON.
     */
    TriageResult parseTriageResult(String response) {
        if (response == null || response.isEmpty()) return null;
        try {
            var node = mapper.readTree(stripCodeFence(response));
            double overallConfidence = node.has(FIELD_OVERALL_CONFIDENCE)
                    ? node.get(FIELD_OVERALL_CONFIDENCE).asDouble(0.5) : 0.5;

            var agents = new ArrayList<TriageResult.SelectedAgent>();
            if (node.has(FIELD_AGENTS) && node.get(FIELD_AGENTS).isArray()) {
                for (var agentNode : node.get(FIELD_AGENTS)) {
                    var agent = parseSelectedAgent(agentNode);
                    if (agent != null) agents.add(agent);
                }
            }
            return new TriageResult(agents, overallConfidence);
        } catch (Exception e) {
            log.debug("Failed to parse triage JSON: {}", e.getMessage());
            return null;
        }
    }

    /** Parse one triage agent node; null when it has no usable name. */
    static TriageResult.SelectedAgent parseSelectedAgent(JsonNode agentNode) {
        String name = agentNode.has(FIELD_NAME) ? agentNode.get(FIELD_NAME).asText() : null;
        if (name == null || name.isEmpty()) return null;
        double confidence = agentNode.has(FIELD_CONFIDENCE) ? agentNode.get(FIELD_CONFIDENCE).asDouble(0.5) : 0.5;
        String reason = agentNode.has(FIELD_REASON) ? agentNode.get(FIELD_REASON).asText("") : "";
        return new TriageResult.SelectedAgent(name, confidence, reason);
    }

    /**
     * Validate agent names against known crew resumes.
     * Drops hallucinated names that don't match any real agent.
     */
    private List<TriageResult.SelectedAgent> validateAgentNames(List<TriageResult.SelectedAgent> agents) {
        var known = knownAgentNames.get();
        if (known.isEmpty()) {
            return agents; // Can't validate without known names
        }
        var validated = new ArrayList<TriageResult.SelectedAgent>();
        for (var agent : agents) {
            if (known.contains(agent.name())) {
                validated.add(agent);
            } else {
                log.warn("Triage selected unknown agent '{}' — dropping (hallucinated name)", agent.name());
            }
        }
        return validated;
    }

    /**
     * Load crew resumes from ConfigMap-mounted file.
     * Cached after first successful read. Also extracts known agent names for validation.
     */
    String loadCrewResumes() {
        synchronized (crewResumesLock) {
            return loadCrewResumesLocked();
        }
    }

    private String loadCrewResumesLocked() {
        if (crewResumesCache != null && crewResumesFromFile) return crewResumesCache;
        if (crewResumesCache == null) {
            String fromFile = readCrewResumesFile();
            if (fromFile != null) {
                extractAgentNamesFromResumes(fromFile);
                crewResumesFromFile = true;
                crewResumesCache = fromFile;
                return crewResumesCache;
            }
        }
        return loadCrewResumesFromKv();
    }

    /** The mounted crew resumes file (legacy/optional), or null when absent. */
    private String readCrewResumesFile() {
        try {
            var path = Path.of(crewResumesPath);
            if (Files.exists(path)) {
                return Files.readString(path);
            }
        } catch (IOException e) {
            log.warn("Failed to read crew resumes file {}: {}", crewResumesPath, e.getMessage());
        }
        return null;
    }

    /**
     * The NATS KV capability catalog (operator-maintained), key = <namespace>.<crew>:
     * the grounding source for reasoning-based selection when no file is mounted (the
     * common case). Returns the RAW full catalog; the reasoning path compacts it via
     * compactCatalog(). The entry's revision is checked on every call and the catalog
     * reloaded only when it changed; a failed or empty read keeps the last known one.
     */
    private String loadCrewResumesFromKv() {
        String crew = properties.crew().orElse(null);
        if (crew == null || crew.isEmpty()) {
            log.debug("No crew configured; cannot load capability catalog from KV");
            return crewResumesCache;
        }
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return crewResumesCache;
            var entry = conn.keyValue(CREW_RESUMES_KV_BUCKET).get(natsProvider.scope().resumesKey());
            if (entry == null || entry.getValue() == null) {
                log.info("Crew capability catalog for '{}' not found in KV bucket {}", crew, CREW_RESUMES_KV_BUCKET);
                return crewResumesCache;
            }
            if (crewResumesCache != null && entry.getRevision() == crewResumesRevision) {
                return crewResumesCache;
            }
            String raw = new String(entry.getValue(), java.nio.charset.StandardCharsets.UTF_8);
            extractAgentNamesFromResumes(raw);
            boolean reload = crewResumesCache != null;
            crewResumesCache = raw;
            crewResumesRevision = entry.getRevision();
            log.info("{} crew capability catalog for '{}' from NATS KV ({} agents, revision {})",
                    reload ? "Reloaded" : "Loaded", crew, knownAgentNames.get().size(), crewResumesRevision);
            return crewResumesCache;
        } catch (Exception e) {
            log.warn("Failed to load crew capability catalog for '{}' from KV: {}", crew, e.getMessage());
            return crewResumesCache;
        }
    }

    /**
     * The capability catalog the coordinator reasons over: narrowed to the pre-filter
     * candidate agents when the resume search ranked some, or the full crew when it
     * returned nothing (null). preFilterCandidates only ever returns null or a non-empty
     * list, so a non-null candidates list always narrows.
     */
    String buildAdvisoryCatalog(String rawResumes, List<String> candidates) {
        return candidates != null
                ? compactCatalog(rawResumes, new LinkedHashSet<>(candidates))
                : compactCatalog(rawResumes);
    }

    /**
     * Compact a full crew-resumes JSON array (each entry carries the agent's bulky
     * prompt) into a lean coordinator-reasoning catalog: one line per agent with
     * name, optional role, description, and tools. Dropping the per-agent prompt
     * keeps the catalog small enough to reason over without degrading tool
     * selection. readTree only - never records/readValue (GraalVM native).
     */
    String compactCatalog(String rawJson) {
        return compactCatalog(rawJson, null);
    }

    /**
     * Compact the crew resumes to the capability catalog the coordinator reasons over.
     * When {@code keepAgents} is non-null, only agents whose name is in the set are
     * included (the RAG pre-filter narrows the candidate set before the reasoning call,
     * so the coordinator reasons over the ~top-K relevant agents, not all 23). Skills
     * are ALWAYS kept - they are not agents the pre-filter ranks. A null set keeps
     * every agent (the full catalog, used as the fallback when the resume search is
     * unavailable).
     */
    String compactCatalog(String rawJson, Set<String> keepAgents) {
        if (rawJson == null || rawJson.isBlank()) return null;
        try {
            var arr = mapper.readTree(rawJson);
            if (!arr.isArray() || arr.isEmpty()) return null;
            var agentSb = new StringBuilder();
            var skillSb = new StringBuilder();
            for (var node : arr) {
                appendCatalogNode(agentSb, skillSb, node, keepAgents);
            }
            if (agentSb.length() == 0 && skillSb.length() == 0) return null;
            return buildCatalogString(agentSb, skillSb);
        } catch (Exception e) {
            log.warn("Failed to compact crew capability catalog: {}", e.getMessage());
            return null;
        }
    }

    /** Dispatch one resume node into the agent or skill buffer; skip an agent the
     *  RAG pre-filter excluded. Skills are always kept. */
    private static void appendCatalogNode(StringBuilder agentSb, StringBuilder skillSb,
            JsonNode node, Set<String> keepAgents) {
        if (isSkillNode(node)) {
            appendSkillCatalogEntry(skillSb, node);
        } else if (keepAgentNode(node, keepAgents)) {
            appendCatalogEntry(agentSb, node);
        }
    }

    /** True when the agent is in the pre-filtered candidate set (or no filter applies). */
    private static boolean keepAgentNode(JsonNode node, Set<String> keepAgents) {
        return keepAgents == null || keepAgents.contains(node.path(FIELD_NAME).asText(""));
    }

    /** Append one capability-catalog line: "- name [role]: description Tools: a, b." */
    static void appendCatalogEntry(StringBuilder sb, JsonNode node) {
        String name = node.path(FIELD_NAME).asText("");
        if (name.isEmpty()) return;
        sb.append("- ").append(name);
        String role = node.path(FIELD_ROLE).asText("");
        if (!role.isEmpty() && !ROLE_TOOLER.equals(role)) {
            sb.append(" [").append(role).append("]");
        }
        String desc = node.path(FIELD_DESCRIPTION).asText("");
        if (!desc.isEmpty()) sb.append(": ").append(desc);
        var toolsNode = node.path(FIELD_TOOLS);
        if (toolsNode.isArray() && !toolsNode.isEmpty()) {
            var tools = new ArrayList<String>();
            toolsNode.forEach(t -> tools.add(t.asText()));
            sb.append(" Tools: ").append(String.join(", ", tools)).append(".");
        }
        sb.append("\n");
    }

    /** True when the node represents a skill (kind=skill field present). */
    static boolean isSkillNode(JsonNode node) {
        return KIND_SKILL.equals(node.path(FIELD_KIND).asText(""));
    }

    /** Append one skill line: "- <name>: <description>" to the skills buffer. */
    static void appendSkillCatalogEntry(StringBuilder sb, JsonNode node) {
        String name = node.path(FIELD_NAME).asText("");
        if (name.isEmpty()) return;
        sb.append("- ").append(name);
        String desc = node.path(FIELD_DESCRIPTION).asText("");
        if (!desc.isEmpty()) sb.append(": ").append(desc);
        sb.append("\n");
    }

    /** Combine agent and skill sections into the final catalog string. */
    static String buildCatalogString(StringBuilder agentSb, StringBuilder skillSb) {
        if (skillSb.length() == 0) return agentSb.toString();
        var result = new StringBuilder(agentSb);
        result.append("\n").append(SKILLS_CATALOG_HEADER).append("\n");
        result.append(skillSb);
        return result.toString();
    }

    /**
     * Extract known agent and skill names from the JSON resumes array for validation.
     * Agent entries (no kind field) go to knownAgentNames; skill entries (kind=skill)
     * go to knownSkillNames. Both lists are replaced atomically.
     */
    private void extractAgentNamesFromResumes(String resumesJson) {
        try {
            var resumesNode = mapper.readTree(resumesJson);
            if (!resumesNode.isArray()) return;
            var agentNames = new ArrayList<String>();
            var skillNames = new ArrayList<String>();
            for (var node : resumesNode) {
                if (!node.has(FIELD_NAME)) continue;
                String name = node.get(FIELD_NAME).asText();
                if (isSkillNode(node)) {
                    skillNames.add(name);
                } else {
                    agentNames.add(name);
                }
            }
            knownAgentNames.set(List.copyOf(agentNames));
            knownSkillNames.set(List.copyOf(skillNames));
            log.info("Extracted {} known agent names and {} known skill names from crew capability catalog",
                    agentNames.size(), skillNames.size());
        } catch (Exception e) {
            log.debug("Could not parse agent names from resumes: {}", e.getMessage());
        }
    }

    /**
     * Names of analyst-role agents in the crew capability catalog. Used to scope
     * review_ready to the analyst subset of the coordinator's selected
     * subcommittee (innerCircle), so only domain-relevant analysts review instead
     * of all of them. Researchers and toolers are intentionally excluded - only
     * role == "analyst". See [[Coordinator Domain-Scoped Analyst Subcommittee]].
     */
    Set<String> analystNamesFromResumes() {
        return analystNamesFromResumes(loadCrewResumes());
    }

    /**
     * The analyst subset of the selected subcommittee: the agents present in BOTH
     * innerCircle and analystNames, in iteration order. Empty when no analysts were
     * selected (or the selection was
     * a broadcast / failed), in which case the caller leaves review_ready un-gated so
     * all analysts review (the prior behavior). See [[Coordinator Domain-Scoped Analyst Subcommittee]].
     */
    static List<String> analystCircleFrom(Set<String> innerCircle, Set<String> analystNames) {
        var result = new ArrayList<String>();
        if (innerCircle == null || analystNames == null || analystNames.isEmpty()) return result;
        for (String name : innerCircle) {
            if (analystNames.contains(name)) result.add(name);
        }
        return result;
    }

    /** Parse analyst-role agent names from a crew resumes JSON array. */
    Set<String> analystNamesFromResumes(String raw) {
        var analysts = new HashSet<String>();
        try {
            if (raw == null || raw.isBlank()) return analysts;
            var arr = mapper.readTree(raw);
            if (arr.isArray()) {
                for (var node : arr) {
                    if (ROLE_ANALYST.equals(node.path(FIELD_ROLE).asText(""))) {
                        String name = node.path(FIELD_NAME).asText("");
                        if (!name.isEmpty()) analysts.add(name);
                    }
                }
            }
        } catch (Exception e) {
            log.debug("Could not parse analyst names from resumes: {}", e.getMessage());
        }
        return analysts;
    }

    /**
     * Publish triage_result to NATS for dashboard observability.
     */
    private void publishTriageResult(ThreadState state, List<TriageResult.SelectedAgent> agents,
                                      double overallConfidence, long inferenceMs,
                                      long inputTokens, long outputTokens) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var agentSummaries = new ArrayList<Map<String, Object>>();
            var agentNames = new ArrayList<String>();
            for (var agent : agents) {
                agentSummaries.add(Map.of(
                        FIELD_NAME, agent.name(),
                        FIELD_CONFIDENCE, agent.confidence(),
                        FIELD_REASON, agent.reason()
                ));
                agentNames.add(agent.name() + " (" + String.format("%.2f", agent.confidence()) + ")");
            }

            var metadata = new HashMap<String, Object>();
            metadata.put(FIELD_AGENTS, agentSummaries);
            metadata.put(FIELD_OVERALL_CONFIDENCE, overallConfidence);
            metadata.put(FIELD_INPUT_TOKENS, inputTokens);
            metadata.put(FIELD_OUTPUT_TOKENS, outputTokens);
            metadata.put(FIELD_INFERENCE_MS, inferenceMs);

            var content = "Selected " + agents.size() + " agents: " + String.join(", ", agentNames);

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, state.threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, "triage_result",
                    FIELD_CONTENT, content,
                    FIELD_CHANNEL, CHANNEL_BROADCAST,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, metadata
            );

            String subject = broadcastSubject(state.crew, state.threadId);
            conn.publish(subject, mapper.writeValueAsBytes(message));
            log.info("Published triage_result for thread {}: {}", state.threadId, content);

        } catch (Exception e) {
            log.debug("Failed to publish triage_result: {}", e.getMessage());
        }
    }

    /**
     * Classify channel from advisory technologies. Matches technology names against
     * configured channel names (from the Agent's discussChannels). Falls back to "general".
     * No hardcoded domain knowledge — channel names and technologies are both externalized.
     */
    String classifyChannelFromAdvisory(List<String> technologies) {
        if (technologies == null || technologies.isEmpty()) return CHANNEL_GENERAL;

        for (var tech : technologies) {
            var techLower = tech.toLowerCase();
            for (var channel : channels) {
                if (techLower.contains(channel) || channel.contains(techLower)) {
                    return channel;
                }
            }
        }
        return CHANNEL_GENERAL;
    }

    /** The user's question a thread_start message carries: metadata.userQuery, else its content. */
    static String extractUserQuery(JsonNode msg) {
        if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has(FIELD_USER_QUERY)) {
            return msg.get(FIELD_METADATA).get(FIELD_USER_QUERY).asText();
        }
        return msg.has(FIELD_CONTENT) ? msg.get(FIELD_CONTENT).asText() : "";
    }

    private void cleanupOldThreads() {
        var evicted = new ArrayList<String>();
        threads.entrySet().removeIf(e -> {
            var state = e.getValue();
            long age = Duration.between(state.threadCreated, Instant.now()).getSeconds();
            if (state.phase == Phase.CLOSED && age > CLOSED_THREAD_EVICT_SECONDS) {
                evicted.add(state.threadId);
                return true;
            }
            // Final safety net for zombies older than 30 minutes - normally the
            // hard-ceiling watchdog closes active threads far sooner; this also reaps
            // abandoned PAUSED threads (which the watchdog deliberately exempts).
            if (state.phase != Phase.CLOSED && age > ZOMBIE_THREAD_EVICT_SECONDS) {
                return forceCloseZombie(state, age, evicted);
            }
            return false;
        });

        // Artifact GC Layer 1 (lifecycle-tied): a thread leaving memory means its
        // discussion is done, so prefix-delete its spilled artifacts from the object
        // store. Done at eviction (600s after close) rather than at closeThread so a
        // late consumer (e.g. the materializer sidecar still serving the compute
        // tooler) is never cut off mid-read. The bucket's 48h TTL is the backstop if
        // this misses; the orphan reaper (Layer 3) repairs leaks. Batch-delete lists
        // the bucket once for all threads evicted this pass.
        // Guard the connection here (not an early return) so a null connection still
        // lets the conversation-cache cleanup below run. deleteThreadArtifacts also
        // null-guards defensively.
        var conn = natsProvider.getConnection();
        if (!evicted.isEmpty() && conn != null) {
            DiscussionArtifacts.deleteThreadArtifacts(conn, natsProvider.scope(), evicted);
        }

        // Clean stale conversation cache entries (KV TTL handles durability eviction)
        conversationCache.entrySet().removeIf(e -> {
            var history = e.getValue();
            return Duration.between(history.lastAccessed, Instant.now()).toMinutes() > conversationTtlMinutes;
        });
    }

    /**
     * Force-close a zombie thread (non-CLOSED and older than
     * {@code ZOMBIE_THREAD_EVICT_SECONDS}) under the {@code transitioning} CAS so it
     * cannot race the 1s phase sweep or a thread_resume - the same CAS protocol the
     * watchdog follows. Records the eviction and returns {@code true} when the entry
     * should be removed from the thread map; {@code false} leaves it for a later pass
     * (a concurrent sweep owns it now). Extracted from {@link #cleanupOldThreads}'s
     * removeIf predicate to keep that method's cognitive complexity in bounds;
     * package-private so its three paths are unit-testable, like
     * {@link #forceCloseIfOverCeiling}.
     *
     * <p>Unlike {@link #forceCloseIfOverCeiling}, this issues the lighter
     * {@code publishThreadClose} + {@code completePending} directly instead of routing
     * through {@code closeThread}: a 30-minute zombie has nothing left to synthesize,
     * so the full close path would only add cost. This deliberate bypass is unchanged
     * from the inline form this method replaced.
     */
    boolean forceCloseZombie(ThreadState state, long age, List<String> evicted) {
        if (!state.transitioning.compareAndSet(false, true)) {
            return false; // a sweep owns it; revisit next pass
        }
        try {
            if (state.phase == Phase.CLOSED) {
                // Another sweep already closed it. `return true` exits the caller's
                // removeIf predicate entirely, so the post-try evicted.add is NOT
                // reached - this is the single eviction record for this branch, not a
                // double-add.
                evicted.add(state.threadId);
                return true;
            }
            log.warn("Force-closing zombie thread {} (phase={}, age={}s)",
                    state.threadId, state.phase, age);
            String channel = state.primaryChannel != null ? state.primaryChannel : CHANNEL_GENERAL;
            publishThreadClose(state.threadId, channel);
            completePending(state.threadId, null);
            state.phase = Phase.CLOSED;
        } finally {
            state.transitioning.set(false);
        }
        evicted.add(state.threadId);
        return true;
    }

    /**
     * Reap orphaned artifacts (GC Layer 3): objects whose owning thread is no longer in
     * the live thread set and which are older than the grace window. {@code threads}
     * (this coordinator's in-memory map) is the authoritative liveness source - more
     * precise than any persisted KV, and the artifact key is thread-scoped so threadId
     * is exactly the segment the reaper checks. Crew-scoped inside reapOrphans.
     */
    private void reapOrphanArtifacts() {
        var conn = natsProvider.getConnection();
        if (conn == null) {
            return;
        }
        DiscussionArtifacts.reapOrphans(conn, natsProvider.scope(),
                threads::containsKey, ORPHAN_GRACE, Instant.now());
    }

    private static String truncate(String text, int maxLen) {
        if (text == null) return "";
        return text.length() > maxLen ? text.substring(0, maxLen) + "..." : text;
    }

    // Phase state machine
    enum Phase {
        SUBMITTED,      // Initial state
        ADVISORY,       // Generating advisory inline via LLM (~3s)
        EVALUATING,     // Toolers gather; settles when the selected toolers have all signalled
        DECIDING,       // Shaping the review (the crew's review decision call, when declared)
        CONCURRING,     // One analyst is asked whether it concurs with the results
        REVIEW,         // The full review by the selected analysts
        PAUSED,         // External pause — settle timer suspended, no phase transitions
        SYNTHESIZING,   // LLM synthesizing final answer
        CLOSED          // Thread complete
    }

    private record MessageRecord(String agentName, String messageType, String content,
                                  String channel, Instant timestamp) {}

    /**
     * Broadcast subject for a thread of {@code crew} in this agent's namespace:
     * kubemoot.discuss.{ns}.{crew}.broadcast.{threadId} (crew token omitted when null/empty).
     */
    private String broadcastSubject(String crew, String threadId) {
        return natsProvider.scope().forCrew(crew).broadcastSubject(threadId);
    }

    /**
     * Channel subject for a thread of {@code crew} in this agent's namespace:
     * kubemoot.discuss.{ns}.{crew}.{channel}.{threadId} (crew token omitted when null/empty).
     */
    private String channelSubject(String crew, String channel, String threadId) {
        return natsProvider.scope().forCrew(crew).discussSubject(channel, threadId);
    }

    // Visible for testing
    static class ThreadState {
        final String threadId;
        final Instant threadCreated = Instant.now();
        final List<MessageRecord> messages = new CopyOnWriteArrayList<>();
        final Set<String> seenMessageIds = ConcurrentHashMap.newKeySet();

        // Phase tracking
        volatile Phase phase = Phase.SUBMITTED;
        volatile Instant phaseStarted = Instant.now();
        // Saved phase across PAUSED — restored on thread_resume.
        volatile Phase phaseBeforePause;
        final AtomicBoolean transitioning = new AtomicBoolean(false);
        // True while the advisory LLM call is running asynchronously.
        // Evaluation phase must not transition while advisory is pending —
        // agents won't respond until advisory_ready is published.
        final AtomicBoolean advisoryPending = new AtomicBoolean(false);

        // Thread metadata
        volatile String userQuery;
        volatile String primaryChannel;
        volatile String conversationId;
        volatile String crew;

        // Advisory tracking
        volatile String advisoryContent;
        List<String> advisoryTechnologies = new CopyOnWriteArrayList<>();
        // Tracks when the last agent signal arrived (for settle-window early transitions)
        volatile Instant lastSignalReceived;

        // Signal tracking (agent name → content)
        final ConcurrentHashMap<String, String> agreeSignals = new ConcurrentHashMap<>();
        final ConcurrentHashMap<String, String> concernSignals = new ConcurrentHashMap<>();
        final ConcurrentHashMap<String, String> blockSignals = new ConcurrentHashMap<>();
        final Set<String> standAsideSignals = ConcurrentHashMap.newKeySet();
        /**
         * Agents that published a `failure` signal — tried to evaluate but
         * couldn't complete due to infrastructure failure (MCP tool timeout,
         * model OOM, exhausted tool iterations). Tracked SEPARATELY from
         * standAsideSignals because the semantics differ: stand_aside means
         * "I chose not to weigh in" (typically followed by a real text
         * response or NOTHING_TO_ADD), failure means "I tried but
         * couldn't" (typically due to broken tools/MCP/model). Both count
         * as "done" for settle math (the coordinator doesn't wait), but
         * gap detection treats them differently: many stand-asides + zero
         * agrees suggests a tooler gap (no relevant expertise);
         * many failures + zero agrees suggests an infrastructure gap (the
         * MCP servers or model providers need attention, not new toolers).
         */
        final ConcurrentHashMap<String, String> failureSignals = new ConcurrentHashMap<>();
        /** Agents that stood aside for GPU capacity: agent name to gpu-busy or model-too-large. */
        final ConcurrentHashMap<String, String> capacityStandAsides = new ConcurrentHashMap<>();
        /** Models a model-too-large stand-aside named. */
        final Set<String> tooLargeModels = ConcurrentHashMap.newKeySet();
        /** The coordinator's own synthesis prompt was larger than every GPU's context window. */
        volatile boolean synthesisPromptTooLarge;
        /** Agents currently waiting for GPU capacity: agent name to the model they wait for. */
        final ConcurrentHashMap<String, String> waitingAgents = new ConcurrentHashMap<>();

        void clearCapacitySignals() {
            capacityStandAsides.clear();
            tooLargeModels.clear();
            waitingAgents.clear();
        }
        // Agents with role=researcher (e.g., internet search) — excluded from
        // settle triggers, single-agree skip, and gap detection so toolers get full
        // evaluation time and gaps still fire when only researchers answer.
        final Set<String> researcherAgents = ConcurrentHashMap.newKeySet();

        // Token tracking for coordinator LLM calls
        volatile long advisoryInputTokens;
        volatile long advisoryOutputTokens;
        volatile long triageInputTokens;
        volatile long triageOutputTokens;
        volatile long synthesisInputTokens;
        volatile long synthesisOutputTokens;

        // Triage subcommittee — set of agent names selected by triage LLM call.
        // When non-null and non-empty, only these agents should evaluate.
        // volatile: assigned on llmExecutor threads, read on the phase-transition
        // checker thread (advisory_ready and review_ready publishes).
        volatile Set<String> innerCircle; // NOSONAR S3077: volatile reference + only ever assigned ConcurrentHashMap.newKeySet(); thread-safe by construction (ref visibility + concurrent contents)
        volatile double triageConfidence = -1.0;
        // Skills selected by the coordinator for this discussion. Set when the
        // coordinator's reasoning JSON includes a "skills" array. Empty set means
        // no skills were selected (behavior unchanged from pre-skills baseline).
        // volatile: written on llmExecutor thread, read on phase-transition thread.
        volatile Set<String> selectedSkills; // NOSONAR S3077: same pattern as innerCircle

        // Agents that have published an evaluating signal but not yet a terminal signal.
        // Map: agentName → deadline epoch ms (P90 + grace). The settle timer must not fire
        // while any non-expired entry exists here.
        final ConcurrentHashMap<String, Long> pendingEvaluations = new ConcurrentHashMap<>();

        // The agents the current phase waits for (the selected toolers in EVALUATING,
        // the woken analysts in CONCURRING and REVIEW). Empty when unknown.
        volatile Set<String> phaseRoster = Set.of(); // NOSONAR S3077: immutable set, reference swapped whole
        // The analyst asked to concur, while and after CONCURRING.
        volatile String concurrer;
        // The crew's resume ranking for this question, looked up once when a review is shaped.
        volatile List<String> resumeRanking; // NOSONAR S3077: immutable list, reference swapped whole

        ThreadState(String threadId) {
            this.threadId = threadId;
        }
    }

    // --- Conversation history for cross-thread context (NATS KV-backed) ---

    record ConversationTurn(String query, String response, String threadId, String timestamp) {}

    static class ConversationHistory {
        List<ConversationTurn> turns = new ArrayList<>();
        volatile Instant lastAccessed = Instant.now();

        // Jackson needs these
        public List<ConversationTurn> getTurns() { return turns; }
        public void setTurns(List<ConversationTurn> turns) { this.turns = turns; }

        void addTurn(ConversationTurn turn, int maxTurns) {
            turns.add(turn);
            lastAccessed = Instant.now();
            while (turns.size() > maxTurns) {
                turns.removeFirst();
            }
        }

        void touch() {
            lastAccessed = Instant.now();
        }
    }

    private void recordConversationTurn(String conversationId, String query, String response, String threadId) {
        var history = loadConversationHistory(conversationId);
        history.addTurn(new ConversationTurn(query, response, threadId, Instant.now().toString()), maxConversationTurns);
        conversationCache.put(conversationId, history);
        persistConversationHistory(conversationId, history);
        log.info("Recorded conversation turn for {} (now {} turns)", truncate(conversationId, LOG_ID_PREVIEW_CHARS), history.turns.size());
    }

    /**
     * Build conversation context as a list of Q&A maps for serialization into NATS messages.
     * Returns empty list if no prior turns exist.
     */
    private List<Map<String, String>> buildConversationContext(String conversationId) {
        if (conversationId == null) return List.of();
        var history = loadConversationHistory(conversationId);
        if (history.turns.isEmpty()) return List.of();

        history.touch();
        var context = new ArrayList<Map<String, String>>();
        for (var turn : history.turns) {
            context.add(Map.of(
                    FIELD_QUERY, turn.query,
                    FIELD_RESPONSE, truncate(turn.response, PERSISTED_RESPONSE_CHARS)
            ));
        }
        return context;
    }

    /**
     * Load conversation history: check in-memory cache first, then NATS KV.
     * Returns empty history if nothing found (never null).
     */
    private ConversationHistory loadConversationHistory(String conversationId) {
        // Check in-memory cache first
        var cached = conversationCache.get(conversationId);
        if (cached != null) {
            cached.touch();
            return cached;
        }

        // Try NATS KV
        try {
            var conn = natsProvider.getConnection();
            if (conn != null) {
                var kv = conn.keyValue(conversationKvBucket);
                var entry = kv.get("conv." + conversationId);
                if (entry != null && entry.getValue() != null) {
                    var history = deserializeHistory(entry.getValue());
                    history.lastAccessed = Instant.now();
                    conversationCache.put(conversationId, history);
                    log.info("Restored conversation {} from NATS KV ({} turns)",
                            truncate(conversationId, LOG_ID_PREVIEW_CHARS), history.turns.size());
                    return history;
                }
            }
        } catch (Exception e) {
            log.debug("Could not load conversation {} from KV: {}", truncate(conversationId, LOG_ID_PREVIEW_CHARS), e.getMessage());
        }

        // Nothing found — return empty and cache it
        var empty = new ConversationHistory();
        conversationCache.put(conversationId, empty);
        return empty;
    }

    /**
     * Persist conversation history to NATS KV. Degrades gracefully — logs warning on failure.
     */
    private void persistConversationHistory(String conversationId, ConversationHistory history) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) {
                log.debug("NATS unavailable — conversation {} is in-memory only", truncate(conversationId, LOG_ID_PREVIEW_CHARS));
                return;
            }
            var kv = conn.keyValue(conversationKvBucket);
            kv.put("conv." + conversationId, serializeHistory(history));
        } catch (Exception e) {
            log.warn("Failed to persist conversation {} to KV: {}", truncate(conversationId, LOG_ID_PREVIEW_CHARS), e.getMessage());
        }
    }

    // Manual JSON (de)serialization for ConversationHistory.
    //
    // Jackson bean and record (de)serialization fail silently in GraalVM native
    // because reflection metadata isn't generated for these types — the bean
    // serializer emits an empty object and reflection-based readValue produces
    // an instance with no fields populated. The discussion still completes, so
    // the bug presents as multi-turn history quietly disappearing across pod
    // restarts under native, while passing under JVM. Manual node navigation
    // avoids reflection entirely.
    static byte[] serializeHistory(ConversationHistory history) throws IOException {
        var root = mapper.createObjectNode();
        root.put("lastAccessed", history.lastAccessed.toString());
        var turnsArr = root.putArray("turns");
        for (var turn : history.turns) {
            var node = turnsArr.addObject();
            node.put(FIELD_QUERY, turn.query());
            node.put(FIELD_RESPONSE, turn.response());
            node.put(FIELD_THREAD_ID, turn.threadId());
            node.put(FIELD_TIMESTAMP, turn.timestamp());
        }
        return mapper.writeValueAsBytes(root);
    }

    static ConversationHistory deserializeHistory(byte[] bytes) throws IOException {
        var root = mapper.readTree(bytes);
        var history = new ConversationHistory();
        var lastAccessedNode = root.get("lastAccessed");
        if (lastAccessedNode != null && !lastAccessedNode.isNull()) {
            try {
                history.lastAccessed = Instant.parse(lastAccessedNode.asText());
            } catch (Exception e) {
                history.lastAccessed = Instant.now();
            }
        }
        var turnsNode = root.get("turns");
        if (turnsNode != null && turnsNode.isArray()) {
            for (var t : turnsNode) {
                history.turns.add(new ConversationTurn(
                        textOrEmpty(t, FIELD_QUERY),
                        textOrEmpty(t, FIELD_RESPONSE),
                        textOrEmpty(t, FIELD_THREAD_ID),
                        textOrEmpty(t, FIELD_TIMESTAMP)
                ));
            }
        }
        return history;
    }

    private static String textOrEmpty(JsonNode node, String field) {
        var n = node.get(field);
        return n == null || n.isNull() ? "" : n.asText();
    }

    /**
     * Format conversation context as readable text for LLM prompts.
     */
    static String formatConversationContext(List<?> context) {
        if (context == null || context.isEmpty()) return "";
        var sb = new StringBuilder();
        sb.append("--- Previous conversation ---\n");
        sb.append("The user is continuing a conversation. Previous exchanges:\n\n");
        for (var item : context) {
            if (item instanceof Map<?, ?> turn) {
                sb.append("Q: ").append(turn.get(FIELD_QUERY)).append("\n");
                sb.append("A: ").append(turn.get(FIELD_RESPONSE)).append("\n\n");
            }
        }
        sb.append("--- Current question ---\n\n");
        return sb.toString();
    }
}
