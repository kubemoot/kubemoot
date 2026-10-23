package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.requester.Requester;
import ai.kubemoot.agent.requester.RequesterScope;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.modelserver.ChatModelPool;
import ai.kubemoot.agent.modelserver.OllamaDirectProber;
import ai.kubemoot.agent.modelserver.CallObservation;
import ai.kubemoot.agent.modelserver.CallPlacement;
import ai.kubemoot.agent.modelserver.CallPlanner;
import ai.kubemoot.agent.modelserver.PlacementFeasibility;
import ai.kubemoot.agent.modelserver.PlacementUnavailableException;
import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.ServerState;
import ai.kubemoot.agent.modelserver.StreamedChat;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.StreamingChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.service.tool.ToolExecutor;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicReference;

/**
 * Chat service that orchestrates LLM interactions with RAG, MCP tools,
 * and async NATS discussion-based multi-agent collaboration.
 */
@ApplicationScoped
public class ChatService {

    private static final Logger log = LoggerFactory.getLogger(ChatService.class);

    private static final String STATIC_FALLBACK = "static fallback"; // scheduler-outcome reason label

    private final ChatModel chatModel;
    private final RagClient ragClient;
    private final DiscussionOrchestrator discussionOrchestrator;
    private final AgentProperties properties;
    private final AgentHeartbeatService heartbeatService;
    private final ObjectMapper objectMapper;
    private final Map<String, Conversation> conversations = new ConcurrentHashMap<>();

    /**
     * JIT server selection (optional — null in unit tests). When present,
     * the mulling path asks the selector for the freest server at call
     * time and uses {@link #chatModelPool} to materialise a ChatModel for
     * that endpoint. When null (tests, or when NATS state isn't available
     * yet), mulling falls back to the Quarkus-injected static {@link
     * #chatModel}. See [[Epic - JIT GPU Scheduling]] / Card #2.
     */
    private final ServerSelector serverSelector;
    private final ChatModelPool chatModelPool;
    /**
     * NATS-independent fallback prober. When the KV bucket yields no footprint
     * for a model (NATS stalled, operator restart window), this prober self-fetches
     * /api/tags and /api/ps directly from each server's HTTP endpoint so the
     * VRAM fit-gate can still refuse oversized models. Null in unit tests that
     * supply null for serverSelector. See [[JIT Fit-Gate Degraded Mode Can Spill]].
     */
    private final OllamaDirectProber directProber;
    /** v2 JIT-scheduling: atomic VRAM-footprint claim/release per call. See docs/scheduler.md. */
    private final ai.kubemoot.agent.modelserver.TicketManager ticketManager;
    /**
     * Waits for GPU capacity when every GPU that could hold the model is busy.
     * Null in unit tests that do not exercise waiting: a busy cluster then stands
     * aside at once with reason gpu-busy.
     */
    private final ai.kubemoot.agent.modelserver.GpuCapacityWaiter capacityWaiter;
    /** Other models a mulling call may use when one is warm with room. Null: bound model only. */
    private final ai.kubemoot.agent.modelserver.CandidatePolicy candidatePolicy;
    /** Places mulling calls and plans an agent's first call at selection. */
    private final CallPlacement callPlanner;
    /** Records each model request for the scheduler's learned history; null in tests that do not need it. */
    private final ai.kubemoot.agent.modelserver.PerformanceHistory performanceHistory;

    /**
     * Crew working memory. Recalled facts are auto-injected into the system
     * context every query (so agents start knowing what the crew learned), and
     * {@code REMEMBER:} directives in responses are persisted. Null in unit
     * tests; guarded at every use. See [[Crew Working Memory]].
     */
    private final ai.kubemoot.agent.memory.CrewMemoryClient crewMemory;

    // The triage model: its static endpoint (used when the scheduler cannot place the call) and its name.
    private final String triageEndpoint;
    private final String triageModelId;

    // Tool specs + executors cached after init
    private final McpClientService mcpClient;
    /** The agent's MCP tools, swapped as one value so readers never see a half-updated set. */
    private final AtomicReference<ToolSet> toolSet = new AtomicReference<>();

    /** The current tool set; one read, so a caller works with a single consistent snapshot. */
    private ToolSet tools() {
        return toolSet.get();
    }
    /** This agent's characters per prompt token, learned from the engine's reported prompt sizes. */
    private final CharsPerToken charsPerToken = new CharsPerToken();

    /** Tool specifications in the order offered to the model, and the executor for each. */
    record ToolSet(List<ToolSpecification> specs, Map<ToolSpecification, ToolExecutor> executors) {
        static ToolSet of(Map<ToolSpecification, ToolExecutor> executors) {
            return new ToolSet(List.copyOf(executors.keySet()), Map.copyOf(executors));
        }
    }

    /**
     * The most recent simple call's wait for its first token, and whether its model had
     * to load first (then the wait includes the load). One value, so both always
     * describe the same call; {@link #NONE} before any call and after a failed one.
     */
    public record LastSimpleCall(long firstTokenMs, boolean coldStart) {
        public static final LastSimpleCall NONE = new LastSimpleCall(0L, false);
    }

    private volatile LastSimpleCall lastSimpleCall = LastSimpleCall.NONE;

    /** How long a simple (advisory or synthesis) call may take. */
    private static final Duration SIMPLE_CALL_TIMEOUT = Duration.ofMinutes(5);
    /** The triage call's sampling settings: classification, so low temperature and a bounded reply. */
    private static final double TRIAGE_TEMPERATURE = 0.3;
    private static final int TRIAGE_MAX_TOKENS = 2048;

    @Inject
    @SuppressWarnings("java:S107") // CDI constructor: one parameter per collaborator
    public ChatService(
            ChatModel chatModel,
            RagClient ragClient,
            McpClientService mcpClient,
            DiscussionOrchestrator discussionOrchestrator,
            AgentProperties properties,
            AgentHeartbeatService heartbeatService,
            ObjectMapper objectMapper,
            ServerSelector serverSelector,
            ChatModelPool chatModelPool,
            ai.kubemoot.agent.memory.CrewMemoryClient crewMemory,
            ai.kubemoot.agent.modelserver.TicketManager ticketManager,
            OllamaDirectProber directProber,
            ai.kubemoot.agent.modelserver.GpuCapacityWaiter capacityWaiter,
            ai.kubemoot.agent.modelserver.CandidatePolicy candidatePolicy,
            CallPlacement callPlanner,
            ai.kubemoot.agent.modelserver.PerformanceHistory performanceHistory
    ) {
        this.chatModel = chatModel;
        this.ragClient = ragClient;
        this.discussionOrchestrator = discussionOrchestrator;
        this.properties = properties;
        this.heartbeatService = heartbeatService;
        this.objectMapper = objectMapper;
        this.serverSelector = serverSelector;
        this.chatModelPool = chatModelPool;
        this.crewMemory = crewMemory;
        this.ticketManager = ticketManager;
        this.directProber = directProber;
        this.capacityWaiter = capacityWaiter;
        this.candidatePolicy = candidatePolicy;
        this.callPlanner = callPlanner != null ? callPlanner
                : new CallPlanner(serverSelector, ticketManager, null);
        this.performanceHistory = performanceHistory;

        // Triage model: use dedicated endpoint/model if configured, else fall back to primary
        var triage = properties.triageModel();
        this.triageEndpoint = triage.endpoint().orElse(properties.model().endpoint());
        this.triageModelId = triage.modelId().orElse(properties.model().model());
        log.info("Triage model: {} @ {}", triageModelId, triageEndpoint);

        // Register MCP tools. Tools are discovered via the MCPGateway/McpClientService
        // and exposed uniformly to the agent — scheduling, kubernetes, proxmox, etc.
        // all use the same MCP path. No built-in tools live in agent-runtime.
        this.mcpClient = mcpClient;
        toolSet.set(ToolSet.of(mcpClient.getToolSpecifications()));

        boolean discussionEnabled = discussionOrchestrator != null && discussionOrchestrator.isEnabled();
        log.info("Chat service initialized: {} MCP tools, discussion={} for agent: {}",
                tools().specs().size(), discussionEnabled, properties.agentName());
    }

    public ChatResult chat(ChatRequest request) {
        // For coordinators with discussion enabled, try async collaboration first
        if (discussionOrchestrator != null && discussionOrchestrator.isEnabled()) {
            var orchestrateResult = discussionOrchestrator.orchestrate(request.message(), request.conversationId(), request.crew(),
                    request.requester());
            if (orchestrateResult != null && orchestrateResult.response() != null) {
                return new ChatResult(request.conversationId(), orchestrateResult.response(),
                        properties.model().model(), orchestrateResult.threadId());
            }
            // Timeout or no synthesis — fall through to async notice
            String threadId = orchestrateResult != null ? orchestrateResult.threadId() : null;
            return directAnswer(request, threadId);
        }

        // Direct answer path (toolers via HTTP, coordinators without discussion,
        // and the fitness judge). This is NOT a tooler board contribution, so
        // raw tool output is never returned here - a tool-calling agent on this
        // path (e.g. the judge calling collect_scenario) MUST reason to its own
        // answer/verdict, never dump the raw tool result. Only the discussion
        // contribution path (directChat below) emits raw output, and only for
        // role=tooler. See [[Tooler Raw Output Must Not Leak To Judge]].
        return directLlmCall(request, false, false);
    }

    /**
     * Direct inference path — skips discussion orchestration.
     * Used by RtfmSubscriber. Not a tooler board contribution, so no raw output.
     */
    public ChatResult directChat(ChatRequest request) {
        return directLlmCall(request, false, false);
    }

    /**
     * Direct inference path with optional RAG skip — the discussion contribution
     * path (DiscussionSubscriber.runMullingInference).
     * When skipRag=true, skips RAG context query — used in discussion evaluation
     * where the thread already provides full context via formatThread().
     *
     * Raw tool output is the tooler contract: a role=tooler agent posts its raw
     * tool results to the board for an Analyst/Coordinator to process. Any other
     * role (analyst, coordinator, observer, or a roleless agent defaulting to
     * tooler) reasons to its own text instead - raw output only makes sense when
     * a downstream board participant will consume it.
     */
    public ChatResult directChat(ChatRequest request, boolean skipRag) {
        return directChat(request, skipRag, ai.kubemoot.agent.modelserver.CapacityWait.NONE);
    }

    /**
     * The discussion contribution path with a capacity wait: when every GPU that
     * could hold the model is busy, the call waits for capacity (announcing it
     * through {@code wait}) instead of standing aside at once.
     */
    public ChatResult directChat(ChatRequest request, boolean skipRag,
                                 ai.kubemoot.agent.modelserver.CapacityWait wait) {
        boolean toolerRawOutput = "tooler".equals(properties.discuss().role());
        requireDeclaredTools(toolerRawOutput);
        return directLlmCall(request, skipRag, toolerRawOutput, wait);
    }

    /**
     * Fail before any model call when the tools this agent's spec declares are not on the tool
     * list the runtime fetched: a model asked to use tools it was not given answers from nothing.
     * The failure reaches the board as a failure signal naming the missing tools and their source.
     * A tooler needs every declared tool; any other role needs at least one (see
     * {@link ToolAvailability}). An agent that declares no tools is not checked.
     */
    private void requireDeclaredTools(boolean tooler) {
        var enabled = properties.enabledTools().orElse(List.of());
        if (enabled.isEmpty()) {
            return;
        }
        refreshTools();
        var availability = ToolAvailability.of(enabled, properties.disabledTools().orElse(List.of()),
                tools().specs().stream().map(ToolSpecification::name).toList());
        if (!availability.unmet(tooler)) {
            if (availability.anyMissing()) {
                log.warn("Agent {} continues without declared tools {}", properties.agentName(),
                        availability.missing());
            }
            return;
        }
        String source = mcpClient == null ? "its MCP tool source" : mcpClient.describeToolSource();
        log.warn("Agent {} lacks declared tools {}: tool list from {}", properties.agentName(),
                availability.missing(), source);
        // The source address goes to the log and the failure's lastError, not the board message.
        throw new ToolCallFailure(ToolCallFailure.FailureType.TOOLS_UNAVAILABLE,
                String.join(",", availability.missing()), source, availability.missing().size(),
                "Tools unavailable: agent " + properties.agentName() + " declares "
                        + String.join(", ", availability.missing()) + " but the MCP tool source does not offer "
                        + (availability.missing().size() == 1 ? "it" : "them") + ". The model was not called.");
    }

    private ChatResult directAnswer(ChatRequest request, String threadId) {
        log.info("Discussion timed out for coordinator {}, returning async notice", properties.agentName());
        return new ChatResult(request.conversationId(),
            "The tooler agents are still working on your question but haven't finished yet. " +
            "This can happen when many agents are queued on shared GPU resources. " +
            "Please try asking again in a moment.",
            properties.model().model(), threadId);
    }

    private ChatResult directLlmCall(ChatRequest request, boolean skipRag, boolean toolerRawOutput) {
        return directLlmCall(request, skipRag, toolerRawOutput, ai.kubemoot.agent.modelserver.CapacityWait.NONE);
    }

    private ChatResult directLlmCall(ChatRequest request, boolean skipRag, boolean toolerRawOutput,
                                     ai.kubemoot.agent.modelserver.CapacityWait wait) {
        var conversation = conversations.computeIfAbsent(request.conversationId(), Conversation::new);

        // Query RAG for context (skip in discussion path where thread provides context)
        String ragContext = skipRag ? null : ragClient.queryForContext(request.retrievalText());

        // Build message list
        List<ChatMessage> messages = new ArrayList<>();
        String systemPromptText = buildSystemMessage(ragContext, request.discussionThreadId(), request.retrievalText());
        if (!systemPromptText.isEmpty()) {
            messages.add(new SystemMessage(systemPromptText));
        }
        messages.addAll(conversation.getMessages());
        var userMessage = new UserMessage(request.message());
        messages.add(userMessage);
        conversation.addMessage(userMessage);

        // JIT server selection is hoisted OUT of callWithToolLoop so
        // observed latency can be recorded in a finally block — including
        // for the failure path. Previous version only recorded on success,
        // so slow-and-failed calls (the most important signal to capture)
        // were never observed, defeating the self-correcting purpose of
        // the feedback loop. Observed concretely 2026-05-26 in discussion
        // 506ed2d1 where nvidia-gpu landed on rig1 a SECOND time despite
        // rig1 having failed a 7-minute LOOP_TIME_EXCEEDED on the
        // previous test — the prior failure had not been recorded.
        // Phase D: estimate this call's KV-cache need from the prompt's
        // char count so the predictor can gate against in-flight + this-call
        // KV pressure. Crude chars/4 with a safety pad; tighter accuracy
        // via real tokenizer comes in Phase D2 (operator /api/show probe).
        int promptCharCount = PromptSize.chars(messages, tools().specs());
        MullingPick pick = pickMullingChatModel(promptCharCount, wait, request.discussionThreadId(),
                CallObservation.KIND_TOOL_TURN);
        String serverName = pick.serverName();
        // Outbound wire trace: the exact model + endpoint this call will send to
        // Ollama. Pair with OLLAMA_DEBUG on the server to confirm the wire model
        // matches what the scheduler intended (the 2026-06-11 spill: an 8B agent's
        // request to the 4090 loaded 32B - this line records what we believe we sent).
        log.info("OUTBOUND inference (tool-loop): agent={} endpoint={} model={} server={} reason={}",
                properties.agentName(), pick.endpoint(), pick.modelName(),
                serverName.isEmpty() ? "static" : serverName, pick.pickReason());
        long callStartMs = System.currentTimeMillis();
        ToolLoopResult loopResult;
        // Stays true for any Throwable escaping the loop (timeouts, 5xx, ToolCallFailure
        // with an infrastructure cause, or an Error): the circuit-breaker input.
        boolean callFailed = true;
        try (var requesterScope = RequesterScope.bind(request.requester())) {
            loopResult = callWithToolLoop(messages,
                    observed(pick, CallObservation.KIND_TOOL_TURN),
                    serverName, pick.pickReason(), toolerRawOutput, contextLengthOf(pick));
            callFailed = false;
        } finally {
            // ALWAYS record observed latency, even on ToolCallFailure -
            // the failed-call duration is exactly the signal we need to
            // steer subsequent picks away from the bad server.
            long callDurationMs = System.currentTimeMillis() - callStartMs;
            recordSchedulerOutcome(pick, serverName, callDurationMs, callFailed);
        }
        heartbeatService.recordInference();

        // Persist any REMEMBER: directives the agent emitted (durable facts it
        // learned this turn) to crew working memory, and strip them from the
        // user-facing text — they are control directives, not content.
        String text = crewMemory != null
                ? crewMemory.persistFromResponse(loopResult.text(), properties.agentName())
                : loopResult.text();

        conversation.addMessage(new AiMessage(text));
        return new ChatResult(request.conversationId(), text, pick.modelName(), null,
                loopResult.inputTokens(), loopResult.outputTokens(),
                serverName, loopResult.pickReason(), pick.evicted(),
                List.of(new ModelUse(pick.modelName(), serverName, pick.call())));
    }

    /**
     * Result of the tool loop including accumulated token counts AND the
     * server name the JIT-selector chose for this call. serverName is
     * empty when the call fell back to the static Quarkus-injected
     * ChatModel (selector unwired or NATS unavailable). Used by the
     * caller to attribute the inference to a specific server in the
     * agent's outbound signal metadata ([[Per-Call Server Attribution]]).
     */
    record ToolLoopResult(String text, long inputTokens, long outputTokens,
                                   String serverName, String pickReason) {}

    // Bounded-retry thresholds for tool failures inside callWithToolLoop.
    // Tunable; revisit when more diverse tool failure modes appear.
    /** Per-tool failure cap — same tool failing this many times aborts the loop. */
    static final int MAX_SAME_TOOL_FAILURES = 2;
    /** Cluster-wide failure cap — total tool errors across all tools in this loop. */
    static final int MAX_TOTAL_TOOL_FAILURES = 4;
    /**
     * Wall-clock cap on the entire mulling loop. Bounds total elapsed
     * time across successful-but-slow iterations — the case that
     * iteration-count and per-call-timeout caps miss. Observed
     * 2026-05-26 in discussion dccdb33f: nvidia-gpu's ServerSelector
     * landed on a cold-model rig1 because rig0 was momentarily
     * saturated; each iteration took ~30-60s; the user waited 10
     * minutes before the final per-call timeout fired. With this cap,
     * the same case fails cleanly at 5 minutes with a LOOP_TIME_EXCEEDED
     * failure signal — coordinator settles fast, synthesis is honest.
     */
    static final long MAX_LOOP_WALL_CLOCK_MS = 5L * 60L * 1000L; // 5 minutes

    // Max retries for an empty-AND-no-tools turn before the forced-answer
    // fallback. A flaky small model (qwen3:8b) intermittently returns an empty
    // turn WITHOUT calling any tool - it never engaged. One retry (the old
    // behaviour) was not enough: reproduced 2026-06-11, nvidia-gpu-now emptied
    // twice and stood aside, producing a non-answer the judge scored 0. More
    // attempts give the model more chances to actually call its tools.
    // See [[Tooler Empty-Mulling and Single-Agent Selection Cause Non-Answers]].
    static final int EMPTY_NO_TOOLS_MAX_RETRIES = 3;

    // Compute-role contract: how many times to re-prompt a compute agent that
    // answered without running execute_code before failing (never emit an
    // in-head number). See AgentProperties.Discuss.computeContract().
    static final int MAX_COMPUTE_RETRIES = 2;
    // The sidecar artifact mount and the marker the spill code appends to a
    // contribution whose full data was materialized there (DiscussionSubscriber
    // resolveContentField). The contract uses these to require that the compute
    // agent reads the FILE, not the truncated inline preview above the marker.
    private static final String ARTIFACT_MOUNT = "/artifacts/";
    private static final String ARTIFACT_MARKER = "[ARTIFACT key=";
    // The artifact read-ops tools (artifact-readops-mcp) read a spilled artifact
    // SERVER-SIDE from its object key - the compute agent passes key=<key> and the
    // server reads the file, rather than the model authoring open('/artifacts/<key>')
    // in sandbox code (which qwen3:32b flakily skips). A call to any of these over an
    // artifact present in the input satisfies the read-the-file half of the contract
    // exactly as an execute_code that opens /artifacts/ does.
    private static final java.util.Set<String> ARTIFACT_READ_TOOLS = java.util.Set.of(
            "artifact_head", "artifact_tail", "artifact_grep", "artifact_count",
            "artifact_rows", "artifact_select", "artifact_jq");
    // Captures the exact object key from a spill marker "[ARTIFACT key=<key> bytes=...]"
    // so the compute re-prompt can name the precise file at /artifacts/<key> rather
    // than a generic "<key>" placeholder.
    private static final java.util.regex.Pattern ARTIFACT_KEY_PATTERN =
            java.util.regex.Pattern.compile("\\[ARTIFACT key=([^\\]\\s]+)");
    // Strips a reasoning model's <think>...</think> block so the no-data sentinel
    // check sees only the actual answer. See isNoDataDeclaration.
    private static final java.util.regex.Pattern THINK_BLOCK =
            java.util.regex.Pattern.compile("(?is)<think>.*?</think>");
    private static final String COMPUTE_CONTRACT_REPROMPT =
            "You produced an answer without running execute_code. You MUST compute the result by "
            + "calling execute_code over the data. If a contribution references an artifact file "
            + "(/artifacts/<key>), read that FILE in your code - do not answer from the inline "
            + "preview. Call execute_code now. If there is genuinely no data to compute over, "
            + "reply with exactly NO_DATA.";

    // Metrics drill contract: how many times to re-prompt a metrics tooler that
    // queried a metric, got no series back, and concluded WITHOUT a discovery
    // pass. An empty result vector is almost always a wrong metric NAME, so the
    // fix is to make it call list_metrics before declaring "unavailable". See
    // AgentProperties.Discuss.metricsDrillContract() and [[Metrics Tooler Drills
    // Before Declaring Unavailable]].
    static final int MAX_METRICS_DRILL_RETRIES = 2;
    // Prometheus MCP tool names: the query tools whose empty result triggers the
    // drill, and the discovery tools that satisfy it. Kept separate because a
    // query-only specialist (has execute_query but no discovery tool) cannot drill
    // and must be exempt - discovery lives in a different agent for those crews.
    private static final java.util.Set<String> METRIC_QUERY_TOOLS = java.util.Set.of(
            "execute_query", "execute_range_query");
    private static final java.util.Set<String> METRIC_DISCOVERY_TOOLS = java.util.Set.of(
            "list_metrics", "get_metric_metadata");
    private static final String METRICS_DRILL_REPROMPT =
            "Your metric query returned NO series (an empty result). This almost always means the "
            + "metric NAME is wrong, not that the data is missing. Before concluding the metric is "
            + "unavailable you MUST discover the real name with list_metrics (or get_metric_metadata). "
            + "Search with a SHORT, BROAD substring - a single core word (e.g. \"apiserver\", \"request\") "
            + "- NOT your full guessed name and NOT a prefix you assumed (drop guesses like a leading "
            + "\"kube_\"). If a filtered list comes back empty or clearly unrelated, broaden the filter or "
            + "drop it entirely and scan the results. Then re-run your query with the real name you found. "
            + "Only report the metric as genuinely unavailable if a broad discovery search still shows no "
            + "such series exists.";

    /**
     * Backwards-compat overload — pickMullingChatModel call is hoisted to
     * the caller (directLlmCall) so observed latency can be recorded in a
     * finally block. Tests + callers that don't need observed-latency
     * still get JIT selection via this overload.
     */
    ToolLoopResult callWithToolLoop(List<ChatMessage> messages) {
        MullingPick pick = pickMullingChatModel(PromptSize.chars(messages, tools().specs()), CallObservation.KIND_TOOL_TURN);
        try {
            // Legacy/test entry defaults to reasoning behavior, with no raw output;
            // the tooler raw-output contract is opt-in via the 5-arg overload.
            return callWithToolLoop(messages,
                    observed(pick, CallObservation.KIND_TOOL_TURN),
                    pick.serverName(), pick.pickReason(), false, contextLengthOf(pick));
        } finally {
            // v2 release safety: this overload is the test/legacy entry
            // point (the primary directLlmCall hoists pickMullingChatModel
            // for latency capture). Still drop the ticket on every path
            // so callers via this overload don't leak VRAM reservations.
            pick.ticket().ifPresent(callPlanner::releaseTicket);
        }
    }

    /**
     * Call LLM with tool specifications, automatically executing tool calls
     * and feeding results back until the model produces a final text response.
     *
     * Throws {@link ToolCallFailure} when bounded tool retries are exhausted —
     * either the same tool failed {@link #MAX_SAME_TOOL_FAILURES} times or the
     * loop accumulated {@link #MAX_TOTAL_TOOL_FAILURES} errors total. Bounded
     * retries are essential because the McpClientService.createToolExecutor
     * catches tool exceptions and returns them as {@code {"error": "..."}}
     * JSON; without this bound, the LLM keeps retrying failing tools across
     * all {@code maxIterations} (default 15), producing the "10 minutes of
     * silent heartbeats" failure mode that motivated the failure-signal
     * architecture.
     */
    ToolLoopResult callWithToolLoop(List<ChatMessage> messages,
                                     ChatModel modelForThisCall,
                                     String serverName,
                                     String pickReason,
                                     boolean toolerRawOutput) {
        return callWithToolLoop(messages, modelForThisCall, serverName, pickReason, toolerRawOutput, 0L);
    }

    /**
     * {@link #callWithToolLoop(List, ChatModel, String, String, boolean)} against a
     * server that gives the model {@code contextLength} tokens per request (zero
     * when unknown). Each turn fails with CONTEXT_EXCEEDED instead of sending a
     * prompt the engine would cut.
     */
    ToolLoopResult callWithToolLoop(List<ChatMessage> messages,
                                     ChatModel modelForThisCall,
                                     String serverName,
                                     String pickReason,
                                     boolean toolerRawOutput,
                                     long contextLength) {
        refreshTools();
        ToolLoopState state = new ToolLoopState(messages);
        int maxIterations = properties.model().maxToolIterations();

        // Wall-clock deadline for the entire loop — bounds elapsed time
        // across slow iterations. See MAX_LOOP_WALL_CLOCK_MS for rationale.
        long loopStartMs = System.currentTimeMillis();
        long loopDeadlineMs = loopStartMs + MAX_LOOP_WALL_CLOCK_MS;

        for (int i = 0; i < maxIterations; i++) {
            // Wall-clock check FIRST in the iteration so the bound is
            // enforced even when LLM/tool calls are slow but non-erroring.
            // The check is BEFORE the LLM call so we never start a fresh
            // 120s-timeout call that would push past the deadline.
            checkLoopDeadline(loopStartMs, loopDeadlineMs, i);
            var gathered = contributionWhenContextIsFull(state, contextLength, i, toolerRawOutput,
                    serverName, pickReason);
            if (gathered.isPresent()) {
                return gathered.get();
            }

            var aiMessage = invokeModelForLoop(modelForThisCall, state);

            // If no tool calls, decide the final text (or retry an empty turn).
            if (!aiMessage.hasToolExecutionRequests()) {
                var done = resolveNoToolResult(aiMessage, state, toolerRawOutput,
                        serverName, pickReason);
                if (done.isPresent()) {
                    return done.get();
                }
                continue; // empty turn: retry budget consumed, loop again
            }

            executeToolCalls(aiMessage, state, contextLength);
        }

        return exhaustedIterationsResult(state, maxIterations, toolerRawOutput,
                serverName, pickReason);
    }

    /** Throw LOOP_TIME_EXCEEDED when the wall-clock deadline has passed. */
    private static void checkLoopDeadline(long loopStartMs, long loopDeadlineMs, int iteration) {
        if (System.currentTimeMillis() > loopDeadlineMs) {
            long elapsed = System.currentTimeMillis() - loopStartMs;
            throw new ToolCallFailure(
                    ToolCallFailure.FailureType.LOOP_TIME_EXCEEDED,
                    null, null, iteration,
                    "Tool loop exceeded wall-clock deadline at iteration "
                            + iteration + " (elapsed " + elapsed + "ms, cap "
                            + MAX_LOOP_WALL_CLOCK_MS + "ms)");
        }
    }

    /**
     * Decide what happens when the next prompt would not fit the server's
     * context; empty when it fits (or the context is unknown) and the loop goes on.
     * The engine cuts an oversized prompt without an error, dropping the oldest
     * messages (the question among them), so the prompt is never sent. A tooler
     * whose tools already ran stops here with the raw output it gathered: that
     * output is its contribution, and another model turn would add nothing to it;
     * when every tool call failed, the gather failed (GATHER_FAILED) rather than
     * posting error text as data. The metrics drill re-prompt needs another turn,
     * so it does not run here; an empty metric result is small, so a full window
     * almost always holds real data. Any other agent needs the question in the
     * next turn, so it fails visibly with CONTEXT_EXCEEDED.
     */
    private java.util.Optional<ToolLoopResult> contributionWhenContextIsFull(
            ToolLoopState state, long contextLength, int iteration, boolean toolerRawOutput,
            String serverName, String pickReason) {
        if (contextLength <= 0) {
            return java.util.Optional.empty();
        }
        long next = PromptSize.nextPromptTokens(state.allMessages, tools().specs(),
                state.lastPromptTokens, state.lastReplyTokens, state.messagesAtLastCall, charsPerToken);
        if (next <= contextLength) {
            return java.util.Optional.empty();
        }
        if (toolerRawOutput && state.toolsExecuted && state.toolOutput.length() > 0) {
            if (state.totalToolFailures + state.oversizedResults >= state.toolCalls) {
                throw new ToolCallFailure(ToolCallFailure.FailureType.GATHER_FAILED, null, null, iteration,
                        "every tool call failed and the next prompt (~" + next + " tokens) exceeds the "
                                + contextLength + "-token context window");
            }
            log.info("agent {}: the next prompt (~{} tokens) exceeds the {}-token context; "
                    + "contributing the tool output gathered in {} iterations", properties.agentName(),
                    next, contextLength, iteration);
            return java.util.Optional.of(rawOutputResult(state, serverName, pickReason));
        }
        throw new ToolCallFailure(ToolCallFailure.FailureType.CONTEXT_EXCEEDED,
                null, null, iteration,
                "The next prompt (~" + next + " tokens) exceeds the " + contextLength
                        + "-token context window of the chosen server at iteration " + iteration);
    }

    /**
     * The context window, in tokens, the picked server gives the picked model;
     * zero when the pick is static or the server publishes no context.
     */
    private long contextLengthOf(MullingPick pick) {
        if (serverSelector == null || pick.serverName().isEmpty()) {
            return 0L;
        }
        return serverSelector.readState().stream()
                .filter(p -> p.name().equals(pick.serverName()))
                .mapToLong(p -> p.contextLengthFor(pick.modelName()))
                .findFirst().orElse(0L);
    }

    /**
     * Run one model turn: send the accumulated messages (with tool specs when
     * any exist), accumulate token usage, append the reply to the message list,
     * and return it.
     */
    private AiMessage invokeModelForLoop(ChatModel modelForThisCall, ToolLoopState state) {
        ChatResponse response;
        var toolSpecList = tools().specs();
        int sentChars = PromptSize.chars(state.allMessages, toolSpecList);
        if (toolSpecList.isEmpty()) {
            response = modelForThisCall.chat(dev.langchain4j.model.chat.request.ChatRequest.builder()
                    .messages(state.allMessages)
                    .build());
        } else {
            response = modelForThisCall.chat(dev.langchain4j.model.chat.request.ChatRequest.builder()
                    .messages(state.allMessages)
                    .toolSpecifications(toolSpecList)
                    .build());
        }
        var usage = response.tokenUsage();
        if (usage != null) {
            state.totalInput += usage.inputTokenCount() == null ? 0 : usage.inputTokenCount();
            state.totalOutput += usage.outputTokenCount() == null ? 0 : usage.outputTokenCount();
            charsPerToken.observe(sentChars, usage.inputTokenCount() == null ? 0 : usage.inputTokenCount());
        }
        var aiMessage = response.aiMessage();
        state.allMessages.add(aiMessage);
        state.recordTurn(usage);
        return aiMessage;
    }

    /**
     * Decide the result of a no-tool-call turn. Returns a present result to
     * terminate the loop, or empty to signal a retry (an empty turn within the
     * retry budget — the caller continues the loop).
     *
     * Tooler contract (ONLY when toolerRawOutput): if tools ran, the contribution
     * IS the raw tool output regardless of any prose, because inspecting it is the
     * Analyst's job and synthesis the Coordinator's. For any non-tooler that called
     * tools (fitness judge, analyst, coordinator-direct) we return its reasoned
     * TEXT instead - dumping the raw tool result here is the regression that zeroed
     * the deferred judge.
     */
    private java.util.Optional<ToolLoopResult> resolveNoToolResult(
            AiMessage aiMessage, ToolLoopState state, boolean toolerRawOutput,
            String serverName, String pickReason) {
        // Metrics drill contract runs BEFORE the tooler raw-output return: the
        // metrics toolers post raw output, so their empty-query "unavailable" would
        // otherwise ship without ever trying a discovery pass. A re-prompt returns
        // empty (caller continues the loop); a satisfied/inapplicable contract falls
        // through. Honest escapes (NO_DATA / TOOL_GAP) are handled just below.
        if (metricsDrillNeedsRetry(state)) {
            return java.util.Optional.empty();
        }
        if (toolerRawOutput && state.toolsExecuted) {
            return java.util.Optional.of(toolerRawOutputResult(aiMessage, state, serverName, pickReason));
        }
        // Compute contract: a compute agent must produce its result by RUNNING
        // execute_code, and when the data was spilled to an artifact it must read
        // the materialized FILE - never tally the truncated inline preview. The
        // helper re-prompts (returns true) or, once the budget is spent, throws.
        if (computeContractNeedsRetry(aiMessage, state)) {
            return java.util.Optional.empty();
        }
        // No raw-output contract (or no tools ran): this agent reasons to its own answer.
        return resolveReasonedText(aiMessage, state, serverName, pickReason);
    }

    /**
     * The contribution of a tooler whose tools ran (raw-output contract). The
     * tooler's NO_DATA declaration raises GATHER_FAILED, a TOOL_GAP declaration is
     * kept as the contribution, and anything else returns the raw tool output.
     */
    private ToolLoopResult toolerRawOutputResult(AiMessage aiMessage, ToolLoopState state,
                                                 String serverName, String pickReason) {
        // The tooler's OWN judgment of its gather overrides the raw-output
        // contract, so a tool error is never laundered into findings:
        //  - NO_DATA  -> its tools errored / returned nothing usable: a FAILED
        //               gather, raised as a first-class failure signal (not
        //               error-text-as-data). Distinct from an honest empty
        //               result, which the tooler reports as a real "none".
        //  - TOOL_GAP -> it lacks the needed tool TYPE: keep it as the
        //               contribution so the coordinator routes it to concern /
        //               gap detection / onboarding (load-bearing, do not reroute).
        // Otherwise the raw tool output IS the contribution (the data path).
        String finalText = aiMessage.text() == null ? "" : aiMessage.text().strip();
        if (declaresGatherFailed(finalText)) {
            throw new ToolCallFailure(ToolCallFailure.FailureType.GATHER_FAILED,
                    null, null, 0, gatherFailedReason(finalText));
        }
        // Case-sensitive on purpose: the contribution flows verbatim to
        // DiscussionSubscriber's classifier, which also matches "TOOL_GAP:"
        // exactly - so the two ends stay in lockstep.
        if (finalText.startsWith("TOOL_GAP:")) {
            return new ToolLoopResult(finalText,
                    state.totalInput, state.totalOutput, serverName, pickReason);
        }
        return rawOutputResult(state, serverName, pickReason);
    }

    /**
     * The contribution of an agent that reasons to its own answer: its prose, with
     * one retry (empty Optional) for a flaky empty turn and an echo guard so it never
     * publishes its own instruction prompt as the answer.
     */
    private java.util.Optional<ToolLoopResult> resolveReasonedText(AiMessage aiMessage, ToolLoopState state,
                                                                   String serverName, String pickReason) {
        String text = aiMessage.text() != null ? aiMessage.text() : "";
        if (text.isEmpty() && state.emptyNoToolsRetries < EMPTY_NO_TOOLS_MAX_RETRIES) {
            state.emptyNoToolsRetries++;
            state.allMessages.remove(state.allMessages.size() - 1); // discard the empty aiMessage
            log.info("retry-on-empty (no tools) for agent {}: empty turn, retry {}/{}",
                    properties.agentName(), state.emptyNoToolsRetries, EMPTY_NO_TOOLS_MAX_RETRIES);
            return java.util.Optional.empty();
        }
        if (isInstructionEcho(text, loadSystemPrompt())) {
            log.info("agent {} echoed an instruction instead of answering - dropping to stand_aside",
                    properties.agentName());
            text = "";
        }
        return java.util.Optional.of(new ToolLoopResult(text, state.totalInput, state.totalOutput,
                serverName, pickReason));
    }

    /**
     * Enforce the compute contract for a no-tool answer turn. Returns true when a
     * re-prompt was queued (caller continues the loop), false when the contract is
     * satisfied or not applicable, and throws COMPUTE_CONTRACT_UNSATISFIED once the
     * retry budget is spent. Two violations are policed: (1) answering with no
     * tool call at all, and (2) running a tool but never reading the spilled
     * artifact - the read must be either an artifact read-ops tool over the key
     * or execute_code that opens /artifacts/... An exact NO_DATA / TOOL_GAP answer
     * is the honest escape.
     */
    private boolean computeContractNeedsRetry(AiMessage aiMessage, ToolLoopState state) {
        if (!properties.discuss().computeContract() || isNoDataDeclaration(aiMessage.text())) {
            return false;
        }
        boolean artifactOk = !inputReferencesArtifact(state) || state.codeReferencedArtifact;
        if (state.toolsExecuted && artifactOk) {
            return false; // ran code AND (no artifact present OR read the file) -> satisfied
        }
        String why = !state.toolsExecuted
                ? "answered without running a tool"
                : "ran a tool but did not read the artifact (no read-ops call, no execute_code over /artifacts)";
        if (state.computeRetries < MAX_COMPUTE_RETRIES) {
            state.computeRetries++;
            state.allMessages.add(new UserMessage(buildComputeReprompt(state.allMessages)));
            log.info("compute-contract: agent {} {} - re-prompt {}/{}",
                    properties.agentName(), why, state.computeRetries, MAX_COMPUTE_RETRIES);
            return true;
        }
        throw new ToolCallFailure(ToolCallFailure.FailureType.COMPUTE_CONTRACT_UNSATISFIED,
                null, null, state.computeRetries,
                "compute agent " + why + " after " + MAX_COMPUTE_RETRIES + " prompts");
    }

    /**
     * Enforce the metrics drill contract on a concluding no-tool turn. Returns true
     * when a re-prompt was queued (caller continues the loop), false when the
     * contract is satisfied or inapplicable. NEVER throws: once the re-prompt budget
     * is spent the honest empty result is let through as the contribution, because a
     * model that still will not drill is a tier problem, not a hard failure. Fires
     * only when the last metric query returned no series, no discovery pass has run,
     * and a discovery tool is in this agent's set (a query-only specialist cannot
     * drill and is exempt). Placed before the honest NO_DATA escape on purpose: an
     * empty-query "unavailable" is exactly the mis-query this contract corrects, so
     * discovery must be attempted before the agent is allowed to declare a gap.
     */
    private boolean metricsDrillNeedsRetry(ToolLoopState state) {
        if (!properties.discuss().metricsDrillContract()) {
            return false;
        }
        // Short-circuits on the empty-query flag first, so discoveryToolAvailable()
        // (a small linear scan) only runs for an agent that actually made an empty
        // metric query - never on a generic no-tool turn.
        if (!state.metricQueryReturnedEmpty || state.discoveredMetrics || !discoveryToolAvailable()) {
            return false;
        }
        // Same guard shape as computeContractNeedsRetry (< MAX re-prompts, else stop),
        // but on exhaustion this contract lets the honest empty result through rather
        // than throwing: a model that still will not drill is a tier problem.
        if (state.metricsDrillRetries < MAX_METRICS_DRILL_RETRIES) {
            state.metricsDrillRetries++;
            state.allMessages.add(new UserMessage(METRICS_DRILL_REPROMPT));
            log.info("metrics-drill: agent {} queried a metric that returned no series and did not discover - re-prompt {}/{}",
                    properties.agentName(), state.metricsDrillRetries, MAX_METRICS_DRILL_RETRIES);
            return true;
        }
        log.info("metrics-drill: agent {} did not discover after {} prompts - accepting the empty result",
                properties.agentName(), MAX_METRICS_DRILL_RETRIES);
        return false;
    }

    /** True when a Prometheus discovery tool (list_metrics / get_metric_metadata) is
     *  in this agent's tool set, so it CAN resolve a wrong metric name. A query-only
     *  specialist without one is exempt from the drill contract. */
    private boolean discoveryToolAvailable() {
        for (var spec : tools().specs()) {
            if (METRIC_DISCOVERY_TOOLS.contains(spec.name())) {
                return true;
            }
        }
        return false;
    }

    /** True when a Prometheus query tool result carries no series - a tool-error
     *  wrapper or a success response with an empty result set ("result":[]). An empty
     *  result almost always means a wrong metric name rather than a real gap, which is
     *  what the drill contract exists to correct. Relies on the Prometheus API
     *  invariant that data.result is the only "result" key and a populated response
     *  always has an object between the brackets, so "result":[] cannot appear in a
     *  non-empty response. Package-private for unit testing. */
    static boolean isEmptyMetricResult(String result) {
        if (result == null || isToolErrorResult(result)) {
            return true;
        }
        return result.replaceAll("\\s", "").contains("\"result\":[]");
    }

    // Empty-discovery markers across the shapes list_metrics/get_metric_metadata may
    // return: a zero count, or an empty names/metrics/data array, whitespace-stripped.
    private static final java.util.regex.Pattern EMPTY_DISCOVERY = java.util.regex.Pattern.compile(
            "\"total_count\":0|\"returned_count\":0|\"(metrics|metric_names|names|data|result)\":\\[\\]");

    /** True when a discovery tool (list_metrics / get_metric_metadata) returned NO
     *  metrics - a tool error, a bare empty array, a zero count, or an empty
     *  names/metrics array. A wrong-stem filter yields an empty discovery, which must
     *  NOT satisfy the drill contract (the agent has to broaden and try again).
     *  Package-private for unit testing. */
    static boolean isEmptyDiscoveryResult(String result) {
        if (result == null || isToolErrorResult(result)) {
            return true;
        }
        String compact = result.replaceAll("\\s", "");
        return compact.equals("[]") || EMPTY_DISCOVERY.matcher(compact).find();
    }

    /** True when any input message carries the spill marker, i.e. the data the
     *  compute agent must compute over lives in a materialized artifact file. */
    private static boolean inputReferencesArtifact(ToolLoopState state) {
        for (var msg : state.allMessages) {
            String text = messageText(msg);
            if (text != null && text.contains(ARTIFACT_MARKER)) {
                return true;
            }
        }
        return false;
    }

    /** Exact /artifacts/&lt;key&gt; paths named by spill markers in the input, so the
     *  compute re-prompt can point the model at the precise file rather than a
     *  generic "&lt;key&gt;" placeholder. Order-preserving and de-duplicated. */
    //  Package-private for unit testing.
    static java.util.List<String> artifactPathsInInput(java.util.List<ChatMessage> messages) {
        var paths = new java.util.LinkedHashSet<String>();
        for (var msg : messages) {
            String text = messageText(msg);
            if (text == null) {
                continue;
            }
            var m = ARTIFACT_KEY_PATTERN.matcher(text);
            while (m.find()) {
                paths.add(ARTIFACT_MOUNT + m.group(1));
            }
        }
        return new java.util.ArrayList<>(paths);
    }

    /** The compute re-prompt. Names the EXACT artifact file(s) to read when the
     *  input carries spill markers - the flaky failure was qwen3:32b counting the
     *  truncated inline preview instead of reading the file. Falls back to the
     *  generic re-prompt when no artifact is referenced (the no-execute_code case).
     *  Package-private for unit testing. */
    static String buildComputeReprompt(java.util.List<ChatMessage> messages) {
        var paths = artifactPathsInInput(messages);
        if (paths.isEmpty()) {
            return COMPUTE_CONTRACT_REPROMPT;
        }
        String firstKey = paths.get(0).substring(ARTIFACT_MOUNT.length());
        return "You answered without reading the data FILE. The full data lives in: "
                + String.join(", ", paths) + ". You MUST read it before answering. PREFERRED: call an "
                + "artifact read-ops tool with the object key - the server reads the file for you, so "
                + "you never open a path. Use artifact_count / artifact_grep for counts and membership, "
                + "artifact_rows / artifact_select / artifact_jq to pull the rows you need, e.g. "
                + "artifact_count(key=\"" + firstKey + "\", ...); then run execute_code over the returned "
                + "rows only if more math is needed. ALTERNATIVELY, execute_code that opens the file, "
                + "e.g. open('" + paths.get(0) + "'). Do NOT answer from the inline marker - it carries no "
                + "data. If there is genuinely no data to compute over, reply with exactly NO_DATA.";
    }

    /**
     * A tooler declares a FAILED gather when its final answer is the NO_DATA
     * sentinel (optionally with a ": reason"): its tools errored or returned no
     * usable data, so it has no findings. The prompt tells toolers to emit this
     * instead of posting tool-error text as data. Distinct from an honest empty
     * result (reported as a real "none") and from TOOL_GAP (a missing tool). Think
     * blocks are stripped first so a reasoning model's mention does not false-match.
     */
    private static boolean declaresGatherFailed(String text) {
        if (text == null) {
            return false;
        }
        // Exact sentinel only (bare NO_DATA, or "NO_DATA: reason") - never a
        // "NO_DATA ..." prefix, which would swallow a real finding that merely
        // opens with the token (e.g. "NO_DATA was the only clean signal, so ...").
        String answer = normalizeAnswer(text);
        return answer.equals("NO_DATA") || answer.startsWith("NO_DATA:");
    }

    /** Strip a reasoning model's &lt;think&gt; block and normalize, so a sentinel check
     *  sees only the agent's actual answer (shared by the NO_DATA / gap checks). */
    private static String normalizeAnswer(String text) {
        return THINK_BLOCK.matcher(text == null ? "" : text).replaceAll(" ").strip()
                .toUpperCase(java.util.Locale.ROOT);
    }

    /** The one-line reason a tooler gave after the NO_DATA sentinel, for the failure
     *  signal's message; a generic note when it gave none. */
    private static String gatherFailedReason(String text) {
        String t = THINK_BLOCK.matcher(text == null ? "" : text).replaceAll(" ").strip();
        int colon = t.indexOf(':');
        String reason = colon >= 0 ? t.substring(colon + 1).strip() : "";
        return reason.isEmpty()
                ? "tooler could not gather usable data (its tools errored or returned nothing)"
                : reason;
    }

    /**
     * A compute agent may legitimately answer without running code ONLY to report
     * that there was nothing to compute. Recognize that honest gap (the prompt is
     * told to reply NO_DATA; TOOL_GAP is the existing gap signal) so the contract
     * does not force code when there is genuinely no data.
     */
    private static boolean isNoDataDeclaration(String text) {
        if (text == null) {
            return false;
        }
        // A reasoning model (think=true) emits a <think> block that often MENTIONS
        // the NO_DATA / TOOL_GAP sentinel while still answering with a number, and
        // the compute prompt itself is saturated with those tokens. The no-data
        // escape applies ONLY when the agent's actual ANSWER is the sentinel, so
        // strip think blocks and require the cleaned answer to BE the token - never
        // a loose substring of prose that happens to name it.
        // Exact-match only: the prompt instructs the agent to reply with EXACTLY the
        // sentinel, so anything more (a number, or prose that merely names the token)
        // is a real answer the contract must police - never a no-data escape.
        String answer = normalizeAnswer(text);
        return answer.equals("NO_DATA") || answer.equals("TOOL_GAP");
    }

    /**
     * Execute every tool call in the turn, accumulating output and enforcing failure caps.
     * A result too large for the room left in the context window is replaced by a tool error
     * telling the model to ask for less; the model then corrects itself in the same loop.
     */
    private void executeToolCalls(AiMessage aiMessage, ToolLoopState state, long contextLength) {
        long promptTokens = contextLength <= 0 ? 0 : PromptSize.nextPromptTokens(state.allMessages,
                tools().specs(), state.lastPromptTokens, state.lastReplyTokens, state.messagesAtLastCall,
                charsPerToken);
        var budget = new ToolResultBudget(contextLength, promptTokens, charsPerToken);
        for (var toolRequest : aiMessage.toolExecutionRequests()) {
            var executor = findExecutor(toolRequest.name());
            String raw = executor != null
                    ? executor.execute(toolRequest, null)
                    : "{\"error\": \"Unknown tool: " + toolRequest.name() + "\"}";
            var admitted = budget.admit(toolRequest.name(), raw);
            String result = admitted.text();
            state.allMessages.add(new ToolExecutionResultMessage(
                    toolRequest.id(), toolRequest.name(), result));
            state.toolsExecuted = true;
            state.toolCalls++;
            recordReadAndMetricSignals(toolRequest.name(), toolRequest.arguments(), result, state);
            // Accumulate the raw output as the tooler's contribution. Cap each
            // result so a pathologically large tool response cannot blow the
            // downstream discussion/synthesis context.
            state.toolOutput.append("[").append(toolRequest.name()).append("]\n")
                    .append(truncate(result, MAX_TOOL_RESULT_CHARS)).append("\n\n");
            if (admitted.replaced()) {
                recordOversizedResult(toolRequest.name(), admitted, contextLength, state);
            } else {
                recordToolFailure(toolRequest.name(), result, state);
            }
        }
    }

    /**
     * Log a result replaced for its size. It counts as a tool call that returned an error
     * (so a gather where every call failed still fails) but not toward the same-tool and
     * total failure caps: several large lists in one turn are the model's first try and
     * must leave it a turn to ask for less.
     */
    private void recordOversizedResult(String toolName, ToolResultBudget.Admission admitted,
                                       long contextLength, ToolLoopState state) {
        state.oversizedResults++;
        log.info("agent {}: tool '{}' result (~{} tokens) does not fit the {} tokens left of the {}-token "
                + "context; replaced with an error asking for less", properties.agentName(), toolName,
                admitted.resultTokens(), admitted.roomTokens(), contextLength);
    }

    /**
     * Update the compute and metrics-drill contract flags from one tool call: a
     * successful read of the spilled artifact, an empty metric query, and a metric
     * discovery that returned metrics.
     */
    private void recordReadAndMetricSignals(String toolName, String arguments, String result,
                                            ToolLoopState state) {
        // Two ways the compute agent legitimately reads the spilled FILE:
        //   (1) execute_code whose code opens /artifacts/<key>, or
        //   (2) an artifact read-ops tool (artifact_count/rows/grep/jq/...) that
        //       reads the artifact server-side from its key.
        // A validate_code or a call that merely names /artifacts/ in unrelated args
        // does NOT count - it must be one of these two real reads.
        boolean readViaCode = "execute_code".equals(toolName)
                && arguments != null
                && arguments.contains(ARTIFACT_MOUNT);
        boolean readViaReadOps = ARTIFACT_READ_TOOLS.contains(toolName);
        // Only a SUCCESSFUL read satisfies the contract - a key-not-found / errored
        // read-ops or execute_code call must not let the agent answer from nothing.
        if ((readViaCode || readViaReadOps) && !isToolErrorResult(result)) {
            state.codeReferencedArtifact = true;
        }
        // Metrics drill contract signals: a metric query that returns no series
        // (empty vector) or errors arms the drill; a discovery call that actually
        // RETURNED metrics satisfies it.
        if (METRIC_QUERY_TOOLS.contains(toolName)) {
            state.metricQueryReturnedEmpty = isEmptyMetricResult(result);
        }
        // Only a discovery that RETURNED metrics counts. A zero-result list_metrics
        // (the model filtered on a wrong stem, e.g. "kube_apiserver_request_total")
        // must keep re-prompting so the agent broadens the filter, not conclude
        // "unavailable" off an empty discovery.
        if (METRIC_DISCOVERY_TOOLS.contains(toolName) && !isEmptyDiscoveryResult(result)) {
            state.discoveredMetrics = true;
        }
    }

    /**
     * Track a tool failure when the result is a structured error wrapper, and
     * throw ToolCallFailure once a per-tool or cluster-wide cap is reached.
     * McpClientService wraps every exception as {"error": "..."} so failures are
     * detected by content, not checked exceptions.
     */
    private void recordToolFailure(String toolName, String result, ToolLoopState state) {
        if (!isToolErrorResult(result)) {
            return;
        }
        int count = state.perToolFailures.merge(toolName, 1, Integer::sum);
        state.totalToolFailures++;
        log.info("Tool '{}' returned error ({} times in this loop, {} total): {}",
                toolName, count, state.totalToolFailures, truncateErrorForLog(result));
        if (count >= MAX_SAME_TOOL_FAILURES) {
            throw new ToolCallFailure(
                    ToolCallFailure.FailureType.SAME_TOOL_REPEATED,
                    toolName, result, count,
                    "Tool '" + toolName + "' failed " + count + " times in this evaluation");
        }
        if (state.totalToolFailures >= MAX_TOTAL_TOOL_FAILURES) {
            throw new ToolCallFailure(
                    ToolCallFailure.FailureType.TOO_MANY_TOOL_FAILURES,
                    toolName, result, state.totalToolFailures,
                    "Tool loop accumulated " + state.totalToolFailures
                            + " tool failures across "
                            + state.perToolFailures.size() + " distinct tools");
        }
    }

    /** A tooler's contribution: the raw output of the tools it ran, with the loop's token totals. */
    private static ToolLoopResult rawOutputResult(ToolLoopState state, String serverName, String pickReason) {
        return new ToolLoopResult(state.toolOutput.toString().strip(),
                state.totalInput, state.totalOutput, serverName, pickReason);
    }

    /**
     * Build the result when the loop exhausts its iteration budget. If a tooler
     * gathered raw output, post it as the contribution (the Analyst/Coordinator
     * turn it into an answer) rather than failing outright; otherwise throw
     * ITERATIONS_EXHAUSTED.
     */
    private ToolLoopResult exhaustedIterationsResult(ToolLoopState state, int maxIterations,
            boolean toolerRawOutput, String serverName, String pickReason) {
        if (toolerRawOutput && state.toolsExecuted && state.toolOutput.length() > 0) {
            log.info("iterations-exhausted for tooler {}: posting {} chars of raw tool output as the contribution",
                    properties.agentName(), state.toolOutput.length());
            return rawOutputResult(state, serverName, pickReason);
        }
        log.warn("Tool loop exceeded max iterations for agent {} (no tool output gathered)", properties.agentName());
        throw new ToolCallFailure(
                ToolCallFailure.FailureType.ITERATIONS_EXHAUSTED,
                null, null, maxIterations,
                "Tool loop did not converge to a final answer within "
                        + maxIterations + " iterations");
    }

    /**
     * Mutable accumulator for one callWithToolLoop run: the growing message list,
     * token totals, per-tool/total failure counts, whether any tool ran, the raw
     * tool output, and the empty-turn retry counter. See callWithToolLoop's
     * inline rationale for why a tooler's raw output is its contribution and why
     * empty turns are retried.
     */
    private static final class ToolLoopState {
        final List<ChatMessage> allMessages;
        long totalInput = 0;
        long totalOutput = 0;
        final Map<String, Integer> perToolFailures = new java.util.HashMap<>();
        int totalToolFailures = 0;
        int oversizedResults = 0;
        boolean toolsExecuted = false;
        int toolCalls = 0;
        final StringBuilder toolOutput = new StringBuilder();
        int emptyNoToolsRetries = 0;
        int computeRetries = 0;
        // True once an execute_code call's arguments referenced the artifact mount
        // (/artifacts/...), i.e. the agent read the materialized FILE rather than
        // computing over the inline preview. Drives the compute contract.
        boolean codeReferencedArtifact = false;
        // Metrics drill contract signals. metricQueryReturnedEmpty tracks the LAST
        // metric query's emptiness (reset when a later query returns series), so a
        // successful re-query clears the trigger. It is last-writer-wins within a
        // single multi-tool batch turn: a batch mixing a populated and an empty query
        // arms the drill on the empty one even though partial data was gathered - an
        // accepted edge, since a re-prompt to discover the missing metric is harmless.
        // discoveredMetrics latches once a discovery tool returns METRICS (a non-empty
        // list), satisfying the contract even if the re-query is still empty (a
        // genuinely absent metric, honestly reported). A zero-result discovery (wrong
        // filter stem) does NOT latch, so the drill keeps re-prompting to broaden.
        boolean metricQueryReturnedEmpty = false;
        boolean discoveredMetrics = false;
        int metricsDrillRetries = 0;

        // The engine's reported prompt and reply sizes for the last turn, and the
        // message count right after its reply, so the next prompt's size can be
        // estimated from them plus the tool results added since.
        long lastPromptTokens = 0;
        long lastReplyTokens = 0;
        int messagesAtLastCall = 0;

        ToolLoopState(List<ChatMessage> messages) {
            this.allMessages = new ArrayList<>(messages);
        }

        void recordTurn(dev.langchain4j.model.output.TokenUsage usage) {
            lastPromptTokens = usage == null || usage.inputTokenCount() == null ? 0 : usage.inputTokenCount();
            lastReplyTokens = usage == null || usage.outputTokenCount() == null ? 0 : usage.outputTokenCount();
            messagesAtLastCall = allMessages.size();
        }
    }

    /** Per-tool-result cap, large enough to hold a full cluster resource list (a
     *  namespace or deployment listing runs well past the old 6000-char value, which
     *  silently dropped the tail of such lists). The spill writes the whole contribution
     *  to an object-store file and leaves only a marker on the bus, and the synthesizer
     *  reads that file back under its own cap, so the context is bounded downstream;
     *  this cap only needs to be large enough not to lose real tool output before it is
     *  written. A read failure / pathological response is still bounded by this value. */
    private static final int MAX_TOOL_RESULT_CHARS = 50_000;

    /** Truncate s to max chars, appending an ellipsis marker when cut. */
    static String truncate(String s, int max) {
        if (s == null) return "";
        if (s.length() <= max) return s;
        return s.substring(0, max) + " ...[truncated]";
    }

    /**
     * True when an agent's tool-free answer is an echo of its instructions rather than a
     * real answer: it repeats a fixed instruction phrase, or half or more of its lines
     * are lines of the agent's own system prompt. Small models sometimes parrot their
     * prompt instead of answering, and publishing that corrupts the discussion. An answer
     * that quotes another document, such as the lines of a specification it reviews, is
     * not an echo even when those lines are written in ADL.
     */
    static boolean isInstructionEcho(String text, String ownPrompt) {
        if (text == null || text.isBlank()) return false;
        String t = text.toLowerCase();
        return t.contains("write the final answer to the user")
                || t.contains("using only the tool results shown above")
                || mostlyCopiedFrom(text, ownPrompt);
    }

    /** Lines shorter than this are too generic to count as copied. */
    private static final int MIN_COPIED_LINE_CHARS = 12;

    /** True when at least half of the text's substantial lines appear verbatim in the source. */
    static boolean mostlyCopiedFrom(String text, String source) {
        if (source == null || source.isBlank()) return false;
        java.util.Set<String> sourceLines = normalizedLines(source).collect(java.util.stream.Collectors.toSet());
        List<String> lines = normalizedLines(text).filter(l -> l.length() >= MIN_COPIED_LINE_CHARS).toList();
        if (lines.isEmpty()) return false;
        long copied = lines.stream().filter(sourceLines::contains).count();
        return copied * 2 >= lines.size();
    }

    private static java.util.stream.Stream<String> normalizedLines(String s) {
        return s.lines().map(l -> l.strip().toLowerCase()).filter(l -> !l.isEmpty());
    }

    /**
     * Returns true when a tool result string represents a failure
     * (McpClientService wraps tool exceptions as {@code {"error": "..."}}).
     * Conservative: only matches the documented wrapper shape, not arbitrary
     * JSON that happens to contain the word "error" (e.g., a successful
     * tool returning a Prometheus error description).
     */
    private static boolean isToolErrorResult(String result) {
        if (result == null) return false;
        String trimmed = result.stripLeading();
        return trimmed.startsWith("{\"error\":") || trimmed.startsWith("{ \"error\":");
    }

    private static String truncateErrorForLog(String result) {
        if (result == null) return "";
        int max = 200;
        return result.length() > max ? result.substring(0, max) + "..." : result;
    }

    /**
     * Result of pickMullingChatModel: the ChatModel to invoke, the name
     * of the server it routes to (empty string when falling back to
     * the static Quarkus-injected chatModel). Carries the serverName
     * up the call chain so DiscussionSubscriber can include it in the
     * agent's signal metadata for per-call attribution in the dashboard.
     * See [[Per-Call Server Attribution]] (Epic - JIT GPU Scheduling).
     * Tool-loop calls use {@code model}; simple (no-tool) calls stream from
     * {@code endpoint} with the agent's model settings instead.
     */
    private record MullingPick(ChatModel model, String serverName,
                                java.util.Optional<ai.kubemoot.agent.modelserver.Ticket> ticket,
                                String pickReason, String modelName, String endpoint,
                                long occupancyMiB, java.util.List<String> evicted, boolean coldLoad,
                                ai.kubemoot.agent.modelserver.PlacedCall call) {
        MullingPick(ChatModel model, String serverName,
                    java.util.Optional<ai.kubemoot.agent.modelserver.Ticket> ticket,
                    String pickReason, String modelName, String endpoint, long occupancyMiB) {
            this(model, serverName, ticket, pickReason, modelName, endpoint, occupancyMiB, java.util.List.of(),
                    false, null);
        }

        MullingPick withEvicted(java.util.List<String> models) {
            return new MullingPick(model, serverName, ticket, pickReason, modelName, endpoint, occupancyMiB, models,
                    coldLoad, call);
        }

        /** This pick, marked as placed where the model was not resident. */
        MullingPick withColdLoad(boolean cold) {
            return new MullingPick(model, serverName, ticket, pickReason, modelName, endpoint, occupancyMiB, evicted,
                    cold, call);
        }

        /** This pick, following {@code placed} from its placement decision to the call's measurements. */
        MullingPick withCall(ai.kubemoot.agent.modelserver.PlacedCall placed) {
            return new MullingPick(model, serverName, ticket, pickReason, modelName, endpoint, occupancyMiB, evicted,
                    coldLoad, placed);
        }
    }

    /**
     * Resolve a model's cold-load occupancy (MiB) for JIT placement.
     *
     * Resolution order:
     * <ol>
     *   <li>NATS KV {@code kubemoot_server_state}: the operator publishes
     *       per-model footprints from /api/ps (loaded) and /api/tags (on-disk).
     *       This is the normal, fast path.</li>
     *   <li>Direct HTTP self-fetch ({@link OllamaDirectProber}): when NATS returns
     *       nothing (stalled, operator restart window), probe each server's
     *       /api/ps and /api/tags directly at decision time. This makes the
     *       fit-gate NATS-INDEPENDENT — the 2026-06-14 failure mode (NATS stalled
     *       -> degraded gate -> 32b spilled onto 4090) cannot recur. Prefers an
     *       observed warm footprint (/api/ps) over the on-disk proxy (/api/tags).
     *       See [[JIT Fit-Gate Degraded Mode Can Spill]].</li>
     *   <li>Parameter-count estimate (KvCacheEstimator): when even direct probes
     *       yield nothing (model not downloaded on any server), fall back to a
     *       rough bytes-per-parameter estimate so a ballpark fit check is possible.</li>
     * </ol>
     *
     * Shared by the mulling and triage paths so BOTH place via the same scheduler.
     * Returns 0 only when the model is genuinely unknown on every server AND the
     * name contains no parseable parameter count.
     */
    private long resolveOccupancyMiB(java.util.List<ServerState> states, String modelName) {
        long fp = states.stream()
                .mapToLong(p -> p.coldLoadFootprintMiB(modelName))
                .filter(f -> f > 0L)
                .max()
                .orElse(0L);
        if (fp > 0) return fp;

        // KV returned nothing - self-fetch directly from server endpoints.
        // This is the fix for the NATS-stall degraded path.
        if (directProber != null) {
            String staticEp = properties.model().endpoint();
            long direct = directProber.resolveFootprintMiB(states, staticEp, modelName);
            if (direct > 0) {
                log.info("Self-fetch footprint for model {}: {}MiB ({})", modelName, direct, noStateReason());
                return direct;
            }
        }

        // Last resort: parameter-count estimate from model name.
        return ai.kubemoot.agent.modelserver.KvCacheEstimator.estimateColdOccupancyMiB(modelName);
    }

    /**
     * Pick the ChatModel for THIS mulling call via JIT server selection and
     * claim a VRAM ticket on the chosen server. Falls back to the static
     * Quarkus-injected {@link #chatModel} when the scheduler cannot be consulted
     * (selector/pool/ticket manager unwired, or the model footprint is unknown).
     *
     * <p>Model choice per call: the bound model when it is warm with a free slot;
     * otherwise a candidate model within the quality tolerance that is warm with a
     * free slot (no load, no eviction); otherwise the bound model's normal cost
     * ranking (queue at a warm copy, cold-load onto free room). See
     * {@link #placeCall}.</p>
     *
     * <p>When nothing has room: a model no GPU can ever hold stands aside with
     * reason model-too-large; a busy cluster waits for capacity through
     * {@code wait} and stands aside with reason gpu-busy only when the thread ends
     * or the wait's safety limit passes.</p>
     *
     * <p>Ticket release is the caller's responsibility (try/finally around the
     * call). See {@code TicketManager} for the two-layer release guarantee.</p>
     */
    private MullingPick pickMullingChatModel(int promptCharCount, String kind) {
        return pickMullingChatModel(promptCharCount, ai.kubemoot.agent.modelserver.CapacityWait.NONE, null, kind);
    }

    private MullingPick pickMullingChatModel(int promptCharCount, ai.kubemoot.agent.modelserver.CapacityWait wait,
                                             String threadId, String kind) {
        var staticModel = properties.model().model();
        var staticEndpoint = properties.model().endpoint();

        // Static-fallback guards (selector/pool unwired, ticket manager unwired,
        // or footprint unknown/unestimable). Resolution order for the footprint
        // is documented on resolveOccupancyMiB; zero means we cannot gate it.
        var states = (serverSelector != null) ? serverSelector.readState() : null;
        long coldLoadFootprintMiB = (states != null) ? placementFootprintMiB(states, staticModel, staticModel) : 0L;
        var fallback = staticFallbackPick(staticModel, staticEndpoint, staticModel, coldLoadFootprintMiB);
        if (fallback.isPresent()) {
            return fallback.get();
        }
        // staticFallbackPick is always present when serverSelector is null, and
        // this explicit guard keeps the dereferences below locally provable.
        if (serverSelector == null) {
            return new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK, staticModel, staticEndpoint, 0L);
        }
        MullingPick pick;
        try {
            var placed = heldOrPlaced(threadId, states, promptCharCount, kind);
            pick = placed.isPresent() ? placed.get()
                    : waitForCapacity(staticModel, coldLoadFootprintMiB, promptCharCount, wait, kind);
        } catch (PlacementUnavailableException e) {
            pick = placementUnavailablePick(e, staticModel, staticEndpoint, states);
        }
        callPlanner.callStarted(threadId, pick.modelName());
        return pick;
    }

    /**
     * The call's placement when the placement service cannot answer: the last server it chose for the
     * model when that server is still ready with the model, else the crew's declared endpoint. The call
     * holds no ticket, and its {@code pickReason} says it was placed without the service, so the
     * agent's signal reports the degraded placement and it is never silent.
     */
    private MullingPick placementUnavailablePick(PlacementUnavailableException e, String model,
                                                 String declaredEndpoint, java.util.List<ServerState> states) {
        String why = "placement service unavailable (degraded): " + e.getMessage();
        var lastKnown = chatModelPool == null ? java.util.Optional.<ServerState>empty()
                : callPlanner.lastKnownServer(model).flatMap(name -> states.stream()
                        .filter(st -> st.name().equals(name) && st.ready() && st.hasModelLoaded(model)).findFirst());
        if (lastKnown.isPresent()) {
            ServerState ps = lastKnown.get();
            log.warn("{}; placing {} on the last-known server {}", why, model, ps.name());
            ChatModel built = chatModelPool.forEndpoint(ps.chatEngine(), ps.endpointFor(model), model,
                    properties.model().temperature(), properties.model().maxTokens(),
                    java.time.Duration.ofMinutes(5), properties.model().think().orElse(null));
            return new MullingPick(built, ps.name(), java.util.Optional.empty(),
                    why + "; used the last-known server " + ps.name(), model, ps.endpointFor(model), 0L);
        }
        log.warn("{}; placing {} on the declared endpoint {}", why, model, declaredEndpoint);
        return new MullingPick(chatModel, "", java.util.Optional.empty(),
                why + "; used the declared server", model, declaredEndpoint, 0L);
    }

    /**
     * The placement planned for this thread when the coordinator selected the
     * agent, when it still serves the call ({@link #heldStillServes}); otherwise its
     * claim is released and the call is placed against the state read now.
     */
    private java.util.Optional<MullingPick> heldOrPlaced(String threadId, java.util.List<ServerState> states,
                                                         int promptCharCount, String kind) {
        var held = callPlanner.takeHeld(threadId);
        if (held.isPresent() && heldStillServes(held.get(), states, promptTokens(promptCharCount))) {
            log.info("Using the placement planned at selection: {} on {}", held.get().model(),
                    held.get().pick().server().name());
            var planned = fromPlacement(held.get());
            return java.util.Optional.of(planned.call() == null ? planned
                    : planned.withCall(planned.call().plannedAtSelection()));
        }
        held.ifPresent(h -> callPlanner.releaseTicket(h.pick().ticket()));
        var placed = placeCall(states, promptCharCount, kind);
        held.filter(CallPlanner.Placement::queued).ifPresent(h -> log.info(
                "Re-planned the queue planned at selection ({} on {}): {}", h.model(), h.pick().server().name(),
                placed.map(p -> p.modelName() + " on " + p.serverName()).orElse("no room now, waiting for capacity")));
        return placed;
    }

    /**
     * A held plan serves the call when its server is still ready, its context
     * holds the prompt (the plan was made before the prompt existed), and it owns
     * a call slot or a load it started. A plan that only queued on a busy copy is
     * placed again: the calls that held that copy's slots at selection may have
     * finished, or another model may have room now.
     */
    private static boolean heldStillServes(CallPlanner.Placement held, java.util.List<ServerState> states,
                                           long promptTokens) {
        String server = held.pick().server().name();
        return !held.queued() && states.stream().anyMatch(p -> p.name().equals(server) && p.ready()
                && ai.kubemoot.agent.modelserver.ContextFit.holds(p, held.model(), promptTokens));
    }

    /**
     * Nothing has room for the bound model now. Stand aside with reason
     * model-too-large when no known GPU can ever hold it; otherwise wait for a
     * capacity change and retry {@link #placeCall} on each one. The wait is
     * published to the shared demand view while it lasts.
     */
    private MullingPick waitForCapacity(String model, long footprintMiB, int promptCharCount,
                                        ai.kubemoot.agent.modelserver.CapacityWait wait, String kind) {
        requireFeasible(model, footprintMiB, promptTokens(promptCharCount));
        String busy = "model=" + model + " coldLoad=" + footprintMiB + "MiB: every GPU that can hold it is busy";
        if (capacityWaiter == null || wait.threadEnded()) {
            throw ai.kubemoot.agent.modelserver.NoFitException.gpuBusy(model, busy);
        }
        callPlanner.waitStarted(model);
        long waitStartMs = System.currentTimeMillis();
        try {
            MullingPick placed = capacityWaiter.await(model, () -> {
                serverSelector.invalidateCache();
                return placeCall(serverSelector.readState(), promptCharCount, kind);
            }, wait);
            long waitedMs = System.currentTimeMillis() - waitStartMs;
            if (placed.call() != null) {
                placed.call().waited(waitedMs);
            }
            if (performanceHistory != null) {
                performanceHistory.recordWait(placed.serverName(), placed.modelName(), waitedMs);
            }
            return placed;
        } catch (RuntimeException e) {
            if (performanceHistory != null) {
                performanceHistory.recordUnplacedWait(model, System.currentTimeMillis() - waitStartMs);
            }
            throw e;
        } finally {
            callPlanner.waitEnded(model);
        }
    }

    /**
     * Refuses a call that no wait can place: a model no accelerator can ever hold or a prompt no
     * context window holds stands aside, and a model no ready server has downloaded fails the
     * call (an engine loads only what it has). See {@link PlacementFeasibility}.
     */
    private void requireFeasible(String model, long footprintMiB, long promptTokens) {
        var states = serverSelector.readState();
        java.util.function.ToLongFunction<String> footprint = m -> placementFootprintMiB(states, model, m);
        PlacementFeasibility.refusal(states, model, footprintMiB, mullingRanking(states).acceptable(),
                promptTokens, footprint, serverSelector::activeModelsOn).ifPresent(refusal -> {
                    log.info("Placement refused ({}): {}", refusal.code(), refusal.detail());
                    throw refusal.toException();
                });
    }

    /**
     * The estimated prompt size, in tokens, of a prompt of {@code promptCharCount}
     * characters, from this agent's learned characters per token. Unpadded: it
     * decides whether a prompt fits a context, where over-estimating refuses calls
     * that fit. The padded {@code KvCacheEstimator} estimate sizes VRAM instead.
     */
    private long promptTokens(int promptCharCount) {
        return charsPerToken.tokens(promptCharCount);
    }

    /**
     * One placement attempt against the given server state; empty when no
     * server has room right now. {@link CallPlanner#place} decides; with no
     * server state (NATS degraded), the direct-probe path decides.
     */
    private java.util.Optional<MullingPick> placeCall(java.util.List<ServerState> states, int promptCharCount,
                                                      String kind) {
        String preferred = properties.model().model();
        if (states.isEmpty()) {
            return degradedMullingPick(preferred, properties.model().endpoint(),
                    resolveOccupancyMiB(states, preferred));
        }
        return callPlanner.place(placementRequest(states, promptCharCount, kind)).map(this::fromPlacement);
    }

    private CallPlanner.PlacementRequest placementRequest(java.util.List<ServerState> states, int promptCharCount,
                                                          String kind) {
        String bound = properties.model().model();
        // Phase D: each model's KV-cache need for this call, so the predictor's gate
        // factors in in-flight context-size pressure. Conservative (overestimate).
        return requestFor(mullingRanking(states), states, m -> placementFootprintMiB(states, bound, m),
                promptCharCount, properties.model().maxTokens(), true, kind);
    }

    private MullingPick fromPlacement(CallPlanner.Placement p) {
        MullingPick pick = buildScheduledMullingPick(p.pick(), p.model(), p.footprintMiB())
                .withCall(ai.kubemoot.agent.modelserver.PlacedCall.of(p));
        return p.evicted().isEmpty() ? pick : pick.withEvicted(p.evicted());
    }

    /**
     * The models this mulling call may use: the bound model and the operator's
     * in-tolerance candidates, re-ranked by the rule's preferFaster against the speeds
     * the servers report. Without a candidate policy, or without preferFaster, it is
     * the bound model and its in-tolerance alternatives.
     */
    private ai.kubemoot.agent.modelserver.ModelCandidates.Ranking mullingRanking(java.util.List<ServerState> states) {
        return rankPhase(properties.model().model(), states,
                candidatePolicy == null ? null : candidatePolicy::mullingRanking);
    }

    /**
     * One phase's ranking of {@code bound} and its candidates against the speeds in
     * {@code states}; the bound model alone when the phase has no policy ({@code phase} null).
     */
    private ai.kubemoot.agent.modelserver.ModelCandidates.Ranking rankPhase(String bound,
            java.util.List<ServerState> states,
            java.util.function.BiFunction<String, java.util.function.ToLongFunction<String>,
                    ai.kubemoot.agent.modelserver.ModelCandidates.Ranking> phase) {
        if (phase == null) {
            return new ai.kubemoot.agent.modelserver.ModelCandidates.Ranking(bound, java.util.List.of());
        }
        return phase.apply(bound, m -> measuredSpeed(states, m));
    }

    /**
     * A placement request for a ranking: the preferred model and its alternatives, with
     * each model's KV-cache need for a prompt of {@code promptChars} and
     * {@code outputTokens} of output.
     */
    private CallPlanner.PlacementRequest requestFor(ai.kubemoot.agent.modelserver.ModelCandidates.Ranking ranking,
            java.util.List<ServerState> states, java.util.function.ToLongFunction<String> footprint,
            int promptChars, int outputTokens, boolean mayUnload, String kind) {
        long kvTokens = ai.kubemoot.agent.modelserver.KvCacheEstimator.estimateTokensFromChars(promptChars);
        return new CallPlanner.PlacementRequest(ranking.preferred(), ranking.alternatives(), states, footprint,
                m -> ai.kubemoot.agent.modelserver.KvCacheEstimator.estimateMiB(m, kvTokens, outputTokens),
                promptTokens(promptChars), mayUnload, expectedOutput(kind));
    }

    /**
     * What this agent's calls of {@code kind} on a model have generated lately, by model
     * name, for the placement to plan the generation with; 0 for a model it has no
     * history of.
     */
    private java.util.function.ToLongFunction<String> expectedOutput(String kind) {
        return m -> performanceHistory == null ? 0L : performanceHistory.expectedOutputTokens(m, kind);
    }

    /**
     * The best generation speed, in tokens per second, any ready server that has the
     * model reports for it: what calibration measured, or what solo crew calls observed
     * once there are enough. Zero when no server has measured it, which preferFaster
     * treats as unknown, never as slow.
     */
    static long measuredSpeed(java.util.List<ServerState> states, String model) {
        return states.stream()
                .filter(ServerState::ready)
                .filter(p -> p.hasModel(model))
                .map(p -> p.speedOf(model))
                .flatMap(java.util.Optional::stream)
                .mapToLong(ai.kubemoot.agent.modelserver.ModelSpeed::generateSpeed)
                .max().orElse(0L);
    }

    /**
     * The footprint a placement request carries for {@code model}: the bound model's
     * resolved occupancy, else what servers report. A model only a server whose
     * engine reserved its memory at start hosts (vLLM) has no footprint to report and
     * needs none, so it carries the ticket marker instead of zero, which would leave
     * that server out of every placement.
     */
    private long placementFootprintMiB(java.util.List<ServerState> states, String preferred, String model) {
        long fp = model.equals(preferred) ? resolveOccupancyMiB(states, model) : observedFootprintMiB(states, model);
        return ai.kubemoot.agent.modelserver.EngineFits.placementFootprintMiB(states, model, fp);
    }

    /** A model's KV-published footprint; zero when no server reports it. */
    private static long observedFootprintMiB(java.util.List<ServerState> states, String model) {
        return states.stream().mapToLong(p -> p.coldLoadFootprintMiB(model)).max().orElse(0L);
    }

    /** Prompt size assumed when planning a first call whose conversation is not known. */
    private static final int PLAN_PROMPT_CHARS = 16_000;

    /**
     * {@link #commitToThread(String, long, String)} when the conversation is not
     * known: the plan assumes a prompt of {@link #PLAN_PROMPT_CHARS}.
     */
    public void commitToThread(String threadId, long selectedAtMs) {
        commitToThread(threadId, selectedAtMs, null);
    }

    /**
     * The coordinator selected this agent for {@code threadId}: publish its intent
     * for its candidate models and plan its first mulling call now, claiming the
     * capacity and starting a needed load so it overlaps triage. The plan is sized
     * from what is known at selection (the system prompt, the tool specs and the
     * conversation), so it lands on a server whose context holds the first call
     * instead of starting a load the real prompt then abandons. Runs on the
     * caller's thread; the discussion subscriber calls it from a worker.
     */
    public void commitToThread(String threadId, long selectedAtMs, String conversation) {
        if (serverSelector == null || ticketManager == null || chatModelPool == null) {
            return;
        }
        java.util.List<String> candidates = mullingRanking(serverSelector.readState()).acceptable();
        int planChars = planPromptChars(conversation);
        callPlanner.commitPlan(threadId, selectedAtMs, candidates, () -> {
            var states = serverSelector.readState();
            return states.isEmpty() ? java.util.Optional.empty()
                    : java.util.Optional.of(placementRequest(states, planChars, CallObservation.KIND_TOOL_TURN));
        });
    }

    /**
     * Characters the first mulling prompt will hold, from what is known at
     * selection. A lower bound: messages that arrive on the thread after selection
     * are not counted, and the tool loop's results come later still.
     */
    private int planPromptChars(String conversation) {
        if (conversation == null) {
            return PLAN_PROMPT_CHARS;
        }
        String system = loadSystemPrompt();
        long chars = (system == null ? 0 : system.length()) + PromptSize.chars(List.of(), tools().specs())
                + (long) conversation.length();
        return (int) Math.min(chars, Integer.MAX_VALUE);
    }

    /** The agent stood aside or the thread ended: release the plan made at selection. */
    public void releasePlan(String threadId) {
        if (callPlanner != null) {
            callPlanner.release(threadId);
        }
    }

    /**
     * The static-endpoint fallbacks that precede JIT scheduling. Returns a present
     * MullingPick when the scheduler cannot be consulted: selector/pool unwired
     * (unit tests), ticket manager unwired, or the cold-load footprint is neither
     * observed on any server nor estimable from the model name. Empty means the
     * caller should proceed to pickAndClaim with the (positive) footprint.
     */
    private java.util.Optional<MullingPick> staticFallbackPick(String staticModel,
            String staticEndpoint, String modelName, long coldLoadFootprintMiB) {
        if (serverSelector == null || chatModelPool == null) {
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK, staticModel, staticEndpoint, 0L));
        }
        if (ticketManager == null) {
            log.debug("TicketManager unwired - fallback to static endpoint (tests/degraded)");
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK, staticModel, staticEndpoint, 0L));
        }
        if (coldLoadFootprintMiB <= 0) {
            // Footprint neither observed on any server NOR estimable from the
            // model name (no parseable parameter count). Only now is the static
            // endpoint the genuine last resort.
            log.warn("Cold-load footprint unknown and unestimable for model {} - {} (degraded).",
                    modelName, STATIC_FALLBACK);
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK + " (footprint unknown)", staticModel, staticEndpoint, 0L));
        }
        return java.util.Optional.empty();
    }

    /**
     * Why there is no server state: the NATS read failed (with its error), or the
     * bucket had no current entries.
     */
    private String noStateReason() {
        return serverSelector == null ? "no scheduler"
                : serverSelector.lastReadError().map(e -> "server state read failed: " + e)
                        .orElse("no server state published");
    }

    /**
     * Placement with no server state in NATS (degraded window). The direct
     * prober checks each endpoint for one that passes the VRAM fit gate; it is
     * used when found. Empty when none fits: without server VRAM data the
     * runtime cannot tell a busy cluster from a model that never fits, so the
     * caller treats it as busy and waits for server state to return. With no
     * prober, the static endpoint is the last resort. See [[JIT Fit-Gate Degraded
     * Mode Can Spill]].
     */
    private java.util.Optional<MullingPick> degradedMullingPick(String modelName, String staticEndpoint,
                                                                long coldLoadFootprintMiB) {
        if (directProber == null) {
            log.debug("No NATS server state and no directProber - last-resort static endpoint");
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK + " (nats degraded)", modelName, staticEndpoint, 0L));
        }
        String fittingEndpoint = directProber.pickFittingEndpoint(
                java.util.List.of(), staticEndpoint, modelName, coldLoadFootprintMiB);
        if (fittingEndpoint == null) {
            log.info("No server state ({}): directProber found no fitting endpoint for {} (coldLoad {}MiB) - waiting for capacity",
                    noStateReason(), modelName, coldLoadFootprintMiB);
            return java.util.Optional.empty();
        }
        log.info("No server state ({}): fit pick endpoint={} model={} coldLoad={}MiB (direct probe)",
                noStateReason(), fittingEndpoint, modelName, coldLoadFootprintMiB);
        // Without server state the engine type is unknown; direct probing finds
        // only Ollama servers.
        ChatModel degradedModel = chatModelPool.forEndpoint(
                ai.kubemoot.agent.modelserver.ChatEngines.OLLAMA,
                fittingEndpoint,
                modelName,
                properties.model().temperature(),
                properties.model().maxTokens(),
                java.time.Duration.ofMinutes(5),
                properties.model().think().orElse(null));
        return java.util.Optional.of(new MullingPick(degradedModel, "", java.util.Optional.empty(),
                STATIC_FALLBACK + " (nats degraded, fit-gated)", modelName, fittingEndpoint, coldLoadFootprintMiB));
    }

    /** Build the MullingPick for a server the scheduler chose and a ticket it claimed. */
    private MullingPick buildScheduledMullingPick(
            ai.kubemoot.agent.modelserver.Pick p,
            String modelName, long coldLoadFootprintMiB) {
        var ps = p.server();
        ai.kubemoot.agent.modelserver.ModelReadiness.ensure(serverSelector, callPlanner, p, modelName);
        // INFO (placement visibility): which server the scheduler chose, the
        // model, and the cold-load footprint estimate that drove the fit gate.
        // This is the trace needed to verify the scheduler keeps oversized models
        // off too-small GPUs; promote from debug so a live run shows every pick.
        log.info("JIT pick: server={} endpoint={} model={} coldLoadFootprintEstimate={}MiB totalVram={}MiB",
                ps.name(), ps.endpointFor(modelName), modelName, coldLoadFootprintMiB, ps.totalVramMiB());
        ChatModel built = chatModelPool.forEndpoint(
                ps.chatEngine(),
                ps.endpointFor(modelName),
                modelName,
                properties.model().temperature(),
                properties.model().maxTokens(),
                // Generous: a tool loop chains several calls on one model, and
                // individual tool failures are bounded by ToolCallFailure
                // thresholds in callWithToolLoop.
                java.time.Duration.ofMinutes(5),
                // Per-agent thinking control; empty leaves the model default.
                properties.model().think().orElse(null)
        );
        return new MullingPick(built, ps.name(), java.util.Optional.of(p.ticket()),
                p.score() != null ? p.score().reasoning() : "", modelName, ps.endpointFor(modelName), coldLoadFootprintMiB)
                .withColdLoad(!ps.hasModelLoaded(modelName));
    }

    private ToolExecutor findExecutor(String toolName) {
        for (var entry : tools().executors().entrySet()) {
            if (entry.getKey().name().equals(toolName)) {
                return entry.getValue();
            }
        }
        return null;
    }

    /**
     * The text and token counts of a no-tool call, and the model and server it ran
     * on ({@code modelUse}, null when the call's placement is not known).
     */
    public record SimpleLlmResult(String text, long inputTokens, long outputTokens, ModelUse modelUse) {
        public SimpleLlmResult(String text, long inputTokens, long outputTokens) {
            this(text, inputTokens, outputTokens, null);
        }

        /** This result, ran on {@code use}. */
        public SimpleLlmResult ranOn(ModelUse use) {
            return new SimpleLlmResult(text, inputTokens, outputTokens, use);
        }
    }

    /**
     * Simple LLM call without tools — returns response text plus token counts.
     * Used by advisory generation, triage, and synthesis for token tracking.
     */
    public SimpleLlmResult simpleLlmCallWithTokens(String systemPrompt, String userPrompt) {
        List<ChatMessage> messages = buildSimpleMessages(systemPrompt, userPrompt);

        // Place this call through the JIT scheduler, same as the tool-loop path -
        // the scheduler must be the ONLY thing that picks a GPU. This is a
        // no-tools call (coordinator advisory/synthesis), so it does not use
        // callWithToolLoop, but it still asks pickMullingChatModel for the
        // server and claims/releases a VRAM ticket so concurrent calls account
        // for each other. See [[JIT is the sole scheduler]].
        int promptChars = (systemPrompt == null ? 0 : systemPrompt.length())
                + (userPrompt == null ? 0 : userPrompt.length());
        MullingPick pick = pickForSimpleCall(promptChars);
        String serverName = pick.serverName();
        // Outbound wire trace (no-tools path: advisory/synthesis/triage). Pair with
        // OLLAMA_DEBUG on the server to confirm the wire model matches intent.
        log.info("OUTBOUND inference (simple): agent={} endpoint={} model={} server={} reason={}",
                properties.agentName(), pick.endpoint(), pick.modelName(),
                serverName.isEmpty() ? "static" : serverName, pick.pickReason());
        long callStartMs = System.currentTimeMillis();
        // Assume failure and flip to success only after the call returns normally.
        // This records a failed scheduler outcome for ANY throwable (including Error)
        // without catching Error (S1181) - the throwable propagates untouched.
        boolean callFailed = true;
        try {
            var result = invokeSimpleCall(pick, messages);
            callFailed = false;
            return result;
        } finally {
            long callDurationMs = System.currentTimeMillis() - callStartMs;
            recordSchedulerOutcome(pick, serverName, callDurationMs, callFailed);
        }
    }

    /** Build the system+user message list for a no-tool call (system optional). */
    private static List<ChatMessage> buildSimpleMessages(String systemPrompt, String userPrompt) {
        List<ChatMessage> messages = new ArrayList<>();
        if (systemPrompt != null && !systemPrompt.isEmpty()) {
            messages.add(new SystemMessage(systemPrompt));
        }
        messages.add(new UserMessage(userPrompt));
        return messages;
    }

    /**
     * JIT-pick a server for a no-tool coordinator call. When every GPU is busy or
     * none can hold the model, the coordinator MUST still complete its
     * synthesis/advisory, so it falls back to the static endpoint the operator
     * assigned: a last resort, never a routine bypass. A prompt larger than every
     * context is rethrown instead, because the static endpoint would cut it without
     * an error; every caller handles a failed call visibly.
     */
    private MullingPick pickForSimpleCall(int promptChars) {
        try {
            return pickMullingChatModel(promptChars, CallObservation.KIND_SIMPLE);
        } catch (ai.kubemoot.agent.modelserver.NoFitException nfe) {
            if (ai.kubemoot.agent.modelserver.NoFitException.REASON_PROMPT_TOO_LARGE.equals(nfe.reason())) {
                throw nfe;
            }
            log.info("simpleLlmCall: no server fit ({}) - falling back to static endpoint", nfe.predictorReason());
            return new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK + " (no-fit)", properties.model().model(), properties.model().endpoint(), 0L);
        }
    }

    /** Send a no-tool chat request as a timed stream, record it, and return text + token counts. */
    private SimpleLlmResult invokeSimpleCall(MullingPick pick, List<ChatMessage> messages) {
        var model = chatModelPool.streamingForEndpoint(engineFor(pick.serverName()), pick.endpoint(),
                pick.modelName(), properties.model().temperature(), properties.model().maxTokens(),
                SIMPLE_CALL_TIMEOUT, properties.model().think().orElse(null));
        var context = callContext(pick.serverName(), pick.modelName(),
                CallObservation.KIND_SIMPLE, pick.coldLoad(), pick.call());
        lastSimpleCall = LastSimpleCall.NONE;
        var timed = sendTimed(model, messages, SIMPLE_CALL_TIMEOUT, context);
        lastSimpleCall = new LastSimpleCall(timed.firstTokenMs(), pick.coldLoad());
        var usage = timed.response().tokenUsage();
        return new SimpleLlmResult(timed.response().aiMessage().text(), inputTokens(usage), outputTokens(usage),
                new ModelUse(pick.modelName(), pick.serverName(), pick.call()));
    }

    /**
     * Sends {@code messages} as one timed stream and records it in the performance
     * history when {@code context} is set, whether it succeeds or fails.
     */
    private StreamedChat.Timed sendTimed(StreamingChatModel model, List<ChatMessage> messages, Duration timeout,
                                         ai.kubemoot.agent.modelserver.CallContext context) {
        long start = System.currentTimeMillis();
        StreamedChat.Timed timed = null;
        try {
            timed = StreamedChat.send(model, dev.langchain4j.model.chat.request.ChatRequest.builder().messages(messages).build(), timeout);
            return timed;
        } finally {
            if (context != null) {
                performanceHistory.record(timed == null
                        ? context.measured(false, start, System.currentTimeMillis() - start, 0L, 0L)
                        : context.streamed(true, start, timed, inputTokens(timed.response().tokenUsage()),
                                outputTokens(timed.response().tokenUsage())), context.call());
            }
        }
    }

    private static long inputTokens(dev.langchain4j.model.output.TokenUsage usage) {
        return usage == null || usage.inputTokenCount() == null ? 0L : usage.inputTokenCount();
    }

    private static long outputTokens(dev.langchain4j.model.output.TokenUsage usage) {
        return usage == null || usage.outputTokenCount() == null ? 0L : usage.outputTokenCount();
    }

    /**
     * How calls reach the server's model server, from its engine type in the server
     * state; Ollama when no server was chosen (the static endpoints are Ollama) or its
     * state is not readable.
     */
    private ai.kubemoot.agent.modelserver.ChatEngine engineFor(String serverName) {
        if (serverSelector == null || serverName == null || serverName.isEmpty()) {
            return ai.kubemoot.agent.modelserver.ChatEngines.OLLAMA;
        }
        return serverSelector.readState().stream()
                .filter(p -> serverName.equals(p.name()))
                .findFirst()
                .map(ai.kubemoot.agent.modelserver.ServerState::chatEngine)
                .orElse(ai.kubemoot.agent.modelserver.ChatEngines.OLLAMA);
    }

    /**
     * The pick's model, wrapped so each request it sends is recorded in the scheduler's
     * performance history; the bare model when nothing records or the pick is static.
     */
    private ChatModel observed(MullingPick pick, String kind) {
        var context = callContext(pick.serverName(), pick.modelName(), kind, pick.coldLoad(), pick.call());
        return context == null ? pick.model()
                : new ai.kubemoot.agent.modelserver.ObservedChatModel(pick.model(), performanceHistory, context);
    }

    /**
     * Where a call is placed, read once as it starts; null when nothing records or no
     * server was chosen (the static fallback).
     */
    private ai.kubemoot.agent.modelserver.CallContext callContext(String server, String model, String kind,
                                                                boolean coldLoad,
                                                                ai.kubemoot.agent.modelserver.PlacedCall call) {
        if (performanceHistory == null || server == null || server.isEmpty()) {
            return null;
        }
        int active = callPlanner.activeCountFor(server);
        return new ai.kubemoot.agent.modelserver.CallContext(server, model, kind, coldLoad, active, call);
    }

    /**
     * Shared post-call scheduler bookkeeping for the JIT-selected server:
     * record observed latency, success/failure for the circuit breaker, the
     * per-(server, model) outcome EMA, the residency overlay on success, and
     * finally release the held VRAM ticket. No-op for the static-fallback path
     * (empty serverName). Extracted from directLlmCall and
     * simpleLlmCallWithTokens, which performed the identical sequence.
     */
    private void recordSchedulerOutcome(MullingPick pick, String serverName,
                                        long callDurationMs, boolean callFailed) {
        if (serverName != null && !serverName.isEmpty()) {
            // Observed latency, the circuit breaker (a wedged server is excluded from future picks
            // for a cooldown, faster than the operator's 30s probe) and the per-(server, model)
            // outcome history the ranking weighs.
            callPlanner.recordOutcome(serverName, pick.modelName(), callDurationMs, !callFailed);
            // Residency overlay (anti-thrash): on success the model just ran on this server, so it
            // is resident now. Record it immediately so other agents see it without waiting for
            // the ~30s probe.
            if (!callFailed && pick.occupancyMiB() > 0) {
                callPlanner.recordResidency(serverName, pick.modelName(), pick.occupancyMiB());
            }
        }
        // Drop the held ticket so the next call sees the freed budget. Fires on EVERY path because
        // the store's expiry is only the safety net; the release is the fast reclaim. See
        // docs/architecture/scheduler.md "two-layer release guarantee".
        pick.ticket().ifPresent(callPlanner::releaseTicket);
    }

    /** Extract the text payload of a chat message, or null when it carries none. */
    private static String messageText(ChatMessage m) {
        if (m == null) return null;
        if (m instanceof dev.langchain4j.data.message.SystemMessage sm) return sm.text();
        if (m instanceof dev.langchain4j.data.message.UserMessage um) return um.singleText();
        if (m instanceof dev.langchain4j.data.message.AiMessage am) return am.text();
        return null;
    }

    /** The most recent simple call's first-token wait; {@link LastSimpleCall#NONE} when there is none. */
    public LastSimpleCall lastSimpleCall() {
        return lastSimpleCall;
    }

    /**
     * Simple LLM call without tools — used by advisory generation and synthesis.
     * Delegates to simpleLlmCallWithTokens and discards token counts.
     */
    public String simpleLlmCall(String systemPrompt, String userPrompt) {
        return simpleLlmCallWithTokens(systemPrompt, userPrompt).text();
    }

    /**
     * Triage inference — lightweight "should I contribute?" check using the triage model.
     * No tools, no RAG, just a quick assessment. Returns the raw response text.
     * Uses direct HTTP to Ollama API to avoid LangChain4j/Quarkus named model issues in native builds.
     * The triage model runs on the smaller GPU (e.g., 4090 with qwen3:8b).
     */
    public String triageChat(String systemPrompt, String conversation) {
        return triageChatWithTokens(systemPrompt, conversation).text();
    }

    /**
     * Triage-model variant of {@link #simpleLlmCallWithTokens} — runs the call on
     * the fast triage model (e.g. qwen3:8b on the 4090) instead of the coordinator's
     * reasoning-tier model, and returns the token counts. Used for the coordinator's
     * CLASSIFICATION sub-tasks (advisory generation, subcommittee triage selection),
     * which are JSON-classification, not reasoning — keeping them off the 32B model
     * cuts the per-call prefill cost. Synthesis stays on the reasoning model.
     * See the "Coordinator Model Tiering" card / agentic-consensus.md.
     */
    public SimpleLlmResult triageChatWithTokens(String systemPrompt, String conversation) {
        // WHERE the triage call runs is decided by the configured placement policy,
        // like every other call, with a claimed ticket; the call itself stays a
        // native-safe direct-HTTP Ollama call. It falls back to the static
        // triageEndpoint only when the scheduler cannot decide (NATS down, no fit).
        TriagePlacement placement = pickTriagePlacement(systemPrompt, conversation);
        String endpoint = placement.endpoint();
        var observedCall = callContext(placement.pickedServer(), placement.model(),
                CallObservation.KIND_TRIAGE, placement.coldLoad(), placement.call());

        Duration timeout = Duration.ofSeconds(properties.triageModel().timeoutSeconds());
        var model = chatModelPool.streamingForEndpoint(engineFor(placement.pickedServer()), endpoint, placement.model(),
                TRIAGE_TEMPERATURE, TRIAGE_MAX_TOKENS, timeout, null);
        var messages = new ArrayList<ChatMessage>();
        if (systemPrompt != null && !systemPrompt.isEmpty()) {
            messages.add(SystemMessage.from(systemPrompt));
        }
        messages.add(UserMessage.from(conversation));

        long startMs = System.currentTimeMillis();
        boolean ok = false;
        try {
            var timed = sendTimed(model, messages, timeout, observedCall);
            var usage = timed.response().tokenUsage();
            ok = true;
            return new SimpleLlmResult(timed.response().aiMessage().text(), inputTokens(usage), outputTokens(usage))
                    .ranOn(new ModelUse(placement.model(), placement.pickedServer(), placement.call()));
        } catch (RuntimeException e) {
            throw new java.io.UncheckedIOException("Triage call failed: " + e.getMessage(), new IOException(e));
        } finally {
            recordTriageOutcome(placement, System.currentTimeMillis() - startMs, ok);
        }
    }

    /**
     * JIT placement decision for a triage call: the endpoint to send to, the
     * chosen server name (empty when falling back to the static triageEndpoint),
     * the model occupancy used to claim VRAM, and the held ticket. Extracted from
     * triageChatWithTokens so the call body stays under the complexity gate.
     */
    private record TriagePlacement(String endpoint, String pickedServer, String model, long occupancy,
                                   java.util.Optional<ai.kubemoot.agent.modelserver.Ticket> ticket, boolean coldLoad,
                                   ai.kubemoot.agent.modelserver.PlacedCall call) {
        /** The static triage endpoint and the bound triage model, with no server claimed. */
        static TriagePlacement unscheduled(String endpoint, String model, long occupancy) {
            return new TriagePlacement(endpoint, "", model, occupancy, java.util.Optional.empty(), false, null);
        }
    }

    /**
     * Places the triage call through the configured placement policy, as mulling
     * calls are placed. Returns the static triageEndpoint (no server claimed) when
     * the scheduler is unwired, NATS is down, the footprint is unknown, or no
     * server fits.
     */
    private TriagePlacement pickTriagePlacement(String systemPrompt, String conversation) {
        if (serverSelector == null || ticketManager == null) {
            return TriagePlacement.unscheduled(triageEndpoint, triageModelId, 0L);
        }
        var states = serverSelector.readState();
        if (states.isEmpty()) {
            return TriagePlacement.unscheduled(triageEndpoint, triageModelId, 0L);
        }
        var ranking = triageRanking(states);
        long occupancy = resolveOccupancyMiB(states, triageModelId);
        if (occupancy <= 0 && ranking.alternatives().isEmpty()) {
            return TriagePlacement.unscheduled(triageEndpoint, triageModelId, 0L);
        }
        int promptChars = lengthOrZero(systemPrompt) + lengthOrZero(conversation);
        long promptTokens = promptTokens(promptChars);
        requireTriageContextFits(states, ranking.acceptable(), promptTokens);
        return claimTriagePlacement(states, ranking, occupancy, promptChars);
    }

    /**
     * The models this triage call may use: the bound triage model and the operator's
     * in-tolerance triage candidates, re-ranked by the rule's preferFaster. Without a
     * candidate policy, or without a candidate list, it is the bound triage model alone.
     */
    private ai.kubemoot.agent.modelserver.ModelCandidates.Ranking triageRanking(java.util.List<ServerState> states) {
        return rankPhase(triageModelId, states, candidatePolicy == null ? null : candidatePolicy::triageRanking);
    }

    private static int lengthOrZero(String text) {
        return text == null ? 0 : text.length();
    }

    /** Throws NoFitException when no server's context window can hold the triage prompt. */
    private void requireTriageContextFits(List<ServerState> states, List<String> models, long promptTokens) {
        if (!ai.kubemoot.agent.modelserver.ContextFit.anyCanHold(states, models, promptTokens)) {
            throw ai.kubemoot.agent.modelserver.NoFitException.promptTooLarge(triageModelId,
                    "triage prompt ~" + promptTokens + " tokens exceeds the largest context window ("
                            + ai.kubemoot.agent.modelserver.ContextFit.largestContext(states, models)
                            + " tokens) any server gives " + models);
        }
    }

    /**
     * Claim a server for the triage call through the configured placement policy,
     * as every other call is placed, or keep the static triageEndpoint when no
     * server fits. Triage is short and waits on nothing, so it never unloads idle
     * models to make room: it takes the preferred triage model warm with room, else an
     * in-tolerance candidate that is warm or fits now. A queued placement keeps its
     * ticket like any other; recordTriageOutcome releases it.
     */
    private TriagePlacement claimTriagePlacement(List<ServerState> states,
                                                 ai.kubemoot.agent.modelserver.ModelCandidates.Ranking ranking,
                                                 long occupancy, int promptChars) {
        try {
            var placed = callPlanner.place(requestFor(ranking, states,
                    m -> m.equals(triageModelId) ? occupancy : placementFootprintMiB(states, triageModelId, m),
                    promptChars, 2048, false, CallObservation.KIND_TRIAGE));
            if (placed.isPresent()) {
                return scheduledTriage(placed.get());
            }
            // pick empty (NoFit/degraded) -> keep the static triageEndpoint
        } catch (ai.kubemoot.agent.modelserver.NoFitException nfe) {
            log.info("triage: no server fit ({}) - static endpoint", nfe.predictorReason());
        } catch (PlacementUnavailableException e) {
            log.warn("triage: placement service unavailable ({}) - declared endpoint", e.getMessage());
        }
        return TriagePlacement.unscheduled(triageEndpoint, triageModelId, occupancy);
    }

    /** The triage placement for a claimed pick: the model it chose, its endpoint, ticket and readiness. */
    private TriagePlacement scheduledTriage(CallPlanner.Placement p) {
        // Same readiness step as every other call: a model asleep on its server is
        // woken by the operator before the request is sent.
        ai.kubemoot.agent.modelserver.ModelReadiness.ensure(serverSelector, callPlanner, p.pick(), p.model());
        String endpoint = p.pick().server().endpointFor(p.model());
        String pickedServer = p.pick().server().name();
        log.info("OUTBOUND inference (triage): agent={} endpoint={} model={} server={}",
                properties.agentName(), endpoint, p.model(), pickedServer);
        return new TriagePlacement(endpoint, pickedServer, p.model(), p.footprintMiB(),
                java.util.Optional.of(p.pick().ticket()), p.coldLoad(),
                ai.kubemoot.agent.modelserver.PlacedCall.of(p));
    }

    /**
     * Post-triage-call scheduler bookkeeping for a JIT-selected server: record
     * latency, success/failure for the circuit breaker, residency on success, then
     * release the VRAM ticket. No-op when the static fallback was used (empty
     * server name). Extracted from triageChatWithTokens's finally block.
     */
    private void recordTriageOutcome(TriagePlacement placement, long durMs, boolean ok) {
        if (!placement.pickedServer().isEmpty()) {
            // Triage feeds the server's latency and circuit breaker, not the per-model outcome history.
            callPlanner.recordOutcome(placement.pickedServer(), null, durMs, ok);
            if (ok && placement.occupancy() > 0) {
                callPlanner.recordResidency(placement.pickedServer(), placement.model(), placement.occupancy());
            }
        }
        placement.ticket().ifPresent(callPlanner::releaseTicket);
    }

    /** True when a triage model endpoint distinct from the primary model is configured. */
    public boolean hasDistinctTriageModel() {
        return properties.triageModel().endpoint().isPresent();
    }

    private String buildSystemMessage(String ragContext, String discussionThreadId, String query) {
        var sb = new StringBuilder();
        var systemPrompt = loadSystemPrompt();
        if (systemPrompt != null && !systemPrompt.isEmpty()) {
            sb.append(systemPrompt).append("\n\n");
        }
        if (ragContext != null && !ragContext.isEmpty()) {
            sb.append(ragContext).append("\n\n");
        }
        // Crew working memory: auto-inject what the crew has learned on this
        // cluster (e.g. discovered label/topology mappings) so the agent starts
        // already knowing it — RAG-style, every query. See [[Crew Working Memory]].
        if (crewMemory != null) {
            String mem = crewMemory.recallForContext(query);
            if (mem != null && !mem.isEmpty()) {
                sb.append(mem).append("\n");
            }
        }
        // Discussion context. When chatting inside a discussion thread, expose the
        // threadId so prompt modules (e.g. scheduler-advisor) can reference it as
        // sourceThreadId for tools that need conversational continuity.
        if (discussionThreadId != null && !discussionThreadId.isEmpty()) {
            sb.append("Discussion context: threadId=").append(discussionThreadId).append("\n\n");
        }
        return sb.toString();
    }

    private String loadSystemPrompt() {
        var promptFile = properties.systemPromptFile().orElse("");
        if (!promptFile.isEmpty()) {
            try {
                return Files.readString(Path.of(promptFile));
            } catch (IOException e) {
                log.warn("Failed to read system prompt file {}: {}", promptFile, e.getMessage());
            }
        }
        return properties.systemPrompt().orElse("");
    }

    /** Returns the model triage calls run on. */
    public String getTriageModelId() {
        return triageModelId;
    }

    /** Returns the triage model endpoint URL (for GPU label derivation). */
    public String getTriageEndpoint() {
        return triageEndpoint;
    }

    /**
     * Pick up tools the gateway gained or lost since this agent last looked, so a tool
     * server registered after the agent started is used without restarting the pod.
     */
    void refreshTools() {
        if (mcpClient == null || !mcpClient.refreshGatewayToolsIfStale()) {
            return;
        }
        var refreshed = ToolSet.of(mcpClient.getToolSpecifications());
        toolSet.set(refreshed);
        log.info("Tools refreshed for agent {}: {} MCP tools", properties.agentName(), refreshed.specs().size());
    }

    /** Returns the list of available tool names (for triage prompt context). */
    public List<String> getToolNames() {
        refreshTools();
        return tools().specs().stream().map(dev.langchain4j.agent.tool.ToolSpecification::name).toList();
    }

    public void clearConversation(String conversationId) {
        conversations.remove(conversationId);
    }

    // Request/Response records (framework-agnostic)
    /**
     * A chat turn. retrievalQuery, when set, is what knowledge retrieval and crew memory
     * search with instead of the whole message: inside a discussion the message is the
     * formatted thread (brief, other agents' responses), while the user's question is
     * what the documents should match. requester, when set, is the person's time zone and
     * local date-time; every tool call of the turn carries it.
     */
    public record ChatRequest(String conversationId, String message, String crew, String discussionThreadId,
                              String retrievalQuery, Requester requester) {
        public ChatRequest(String conversationId, String message, String crew, String discussionThreadId,
                           String retrievalQuery) {
            this(conversationId, message, crew, discussionThreadId, retrievalQuery, null);
        }
        public ChatRequest(String conversationId, String message, String crew, String discussionThreadId) {
            this(conversationId, message, crew, discussionThreadId, null, null);
        }
        public ChatRequest(String conversationId, String message, String crew) {
            this(conversationId, message, crew, null, null, null);
        }
        public ChatRequest(String conversationId, String message) {
            this(conversationId, message, null, null, null, null);
        }
        public ChatRequest {
            if (conversationId == null || conversationId.isEmpty()) {
                conversationId = java.util.UUID.randomUUID().toString();
            }
        }

        /** The text retrieval searches with: retrievalQuery when given, else the message. */
        public String retrievalText() {
            return retrievalQuery != null && !retrievalQuery.isBlank() ? retrievalQuery : message;
        }
    }

    /**
     * The outcome of a chat turn. {@code model} and {@code serverName} name where
     * the final inference call ran; {@code modelsUsed} lists the model and server of
     * each call behind the response, in call order.
     */
    public record ChatResult(String conversationId, String response, String model, String threadId,
                              long inputTokens, long outputTokens,
                              String serverName, String pickReason, List<String> evicted,
                              List<ModelUse> modelsUsed) {
        public ChatResult {
            evicted = evicted == null ? List.of() : evicted;
            modelsUsed = modelsUsed == null ? List.of() : List.copyOf(modelsUsed);
        }
        /**
         * Where the final call ran: the last entry of {@code modelsUsed}, or the
         * result's {@code model} and {@code serverName} when no call is listed.
         */
        public ModelUse lastModelUse() {
            return modelsUsed.isEmpty() ? new ModelUse(model, serverName) : modelsUsed.get(modelsUsed.size() - 1);
        }
        /** One call: the model and server the result names. */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens,
                          String serverName, String pickReason, List<String> evicted) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, serverName, pickReason,
                    evicted, model == null || model.isEmpty() ? List.of() : List.of(new ModelUse(model, serverName)));
        }
        /** Without evictions: the call needed no model unloaded. */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens, String serverName, String pickReason) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, serverName, pickReason,
                    List.of());
        }
        /** Convenience constructor without token counts or server attribution. */
        public ChatResult(String conversationId, String response, String model, String threadId) {
            this(conversationId, response, model, threadId, 0, 0, "", "");
        }
        /** Convenience constructor without server attribution (legacy callers; pre-JIT). */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, "", "");
        }
        /** Convenience constructor without pickReason (callers pre-FitPredictor v2). */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens, String serverName) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, serverName, "");
        }
    }

    private static class Conversation {
        private final List<ChatMessage> messages = new ArrayList<>();
        private static final int MAX_HISTORY = 20;

        Conversation(String id) {}

        synchronized void addMessage(ChatMessage message) {
            messages.add(message);
            while (messages.size() > MAX_HISTORY) messages.removeFirst();
        }

        synchronized List<ChatMessage> getMessages() {
            return new ArrayList<>(messages);
        }
    }
}
