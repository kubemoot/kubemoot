package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.provider.ChatModelPool;
import ai.kubemoot.agent.provider.OllamaDirectProber;
import ai.kubemoot.agent.provider.CallPlanner;
import ai.kubemoot.agent.provider.ProviderSelector;
import ai.kubemoot.agent.provider.ProviderState;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.service.tool.ToolExecutor;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
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

    // Ollama API field constants
    private static final String OLLAMA_ROLE = "role";
    private static final String OLLAMA_CONTENT = "content";
    private static final String STATIC_FALLBACK = "static fallback"; // scheduler-outcome reason label

    private final ChatModel chatModel;
    private final RagClient ragClient;
    private final DiscussionOrchestrator discussionOrchestrator;
    private final AgentProperties properties;
    private final AgentHeartbeatService heartbeatService;
    private final ObjectMapper objectMapper;
    private final Map<String, Conversation> conversations = new ConcurrentHashMap<>();

    /**
     * JIT provider selection (optional — null in unit tests). When present,
     * the mulling path asks the selector for the freest provider at call
     * time and uses {@link #chatModelPool} to materialise a ChatModel for
     * that endpoint. When null (tests, or when NATS state isn't available
     * yet), mulling falls back to the Quarkus-injected static {@link
     * #chatModel}. See [[Epic - JIT GPU Scheduling]] / Card #2.
     */
    private final ProviderSelector providerSelector;
    private final ChatModelPool chatModelPool;
    /**
     * NATS-independent fallback prober. When the KV bucket yields no footprint
     * for a model (NATS stalled, operator restart window), this prober self-fetches
     * /api/tags and /api/ps directly from each provider's HTTP endpoint so the
     * VRAM fit-gate can still refuse oversized models. Null in unit tests that
     * supply null for providerSelector. See [[JIT Fit-Gate Degraded Mode Can Spill]].
     */
    private final OllamaDirectProber directProber;
    /** v2 JIT-scheduling: atomic VRAM-footprint claim/release per call. See docs/scheduler.md. */
    private final ai.kubemoot.agent.provider.TicketManager ticketManager;
    /**
     * Waits for GPU capacity when every GPU that could hold the model is busy.
     * Null in unit tests that do not exercise waiting: a busy cluster then stands
     * aside at once with reason gpu-busy.
     */
    private final ai.kubemoot.agent.provider.GpuCapacityWaiter capacityWaiter;
    /** Other models a mulling call may use when one is warm with room. Null: bound model only. */
    private final ai.kubemoot.agent.provider.CandidatePolicy candidatePolicy;
    /** Places mulling calls and plans an agent's first call at selection. */
    private final CallPlanner callPlanner;

    /**
     * Crew working memory. Recalled facts are auto-injected into the system
     * context every query (so agents start knowing what the crew learned), and
     * {@code REMEMBER:} directives in responses are persisted. Null in unit
     * tests; guarded at every use. See [[Crew Working Memory]].
     */
    private final ai.kubemoot.agent.memory.CrewMemoryClient crewMemory;

    // Triage model config — direct HTTP to Ollama API (avoids LangChain4j/Quarkus wiring issues in native)
    private final String triageEndpoint;
    private final String triageModelId;
    private final HttpClient triageHttpClient;

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

    // Most recent Ollama `load_duration` from a simpleLlmCall, in milliseconds.
    // 0 when the model was already loaded (warm path) or when LangChain4j
    // doesn't surface the field on this provider. Reflection-based extraction
    // lets us read it from OllamaChatResponseMetadata without a hard import,
    // so the build keeps working if the LangChain4j provider class moves.
    private volatile long lastLoadDurationMs = 0;
    private volatile boolean loadDurationProbed = false;

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
            ProviderSelector providerSelector,
            ChatModelPool chatModelPool,
            ai.kubemoot.agent.memory.CrewMemoryClient crewMemory,
            ai.kubemoot.agent.provider.TicketManager ticketManager,
            OllamaDirectProber directProber,
            ai.kubemoot.agent.provider.GpuCapacityWaiter capacityWaiter,
            ai.kubemoot.agent.provider.CandidatePolicy candidatePolicy,
            CallPlanner callPlanner
    ) {
        this.chatModel = chatModel;
        this.ragClient = ragClient;
        this.discussionOrchestrator = discussionOrchestrator;
        this.properties = properties;
        this.heartbeatService = heartbeatService;
        this.objectMapper = objectMapper;
        this.providerSelector = providerSelector;
        this.chatModelPool = chatModelPool;
        this.crewMemory = crewMemory;
        this.ticketManager = ticketManager;
        this.directProber = directProber;
        this.capacityWaiter = capacityWaiter;
        this.candidatePolicy = candidatePolicy;
        this.callPlanner = callPlanner != null ? callPlanner
                : new CallPlanner(providerSelector, ticketManager, null);

        // Triage model: use dedicated endpoint/model if configured, else fall back to primary
        var triage = properties.triageModel();
        this.triageEndpoint = triage.endpoint().orElse(properties.model().endpoint());
        this.triageModelId = triage.modelId().orElse(properties.model().model());
        this.triageHttpClient = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(10))
                .build();
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
            var orchestrateResult = discussionOrchestrator.orchestrate(request.message(), request.conversationId(), request.crew());
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
        return directChat(request, skipRag, ai.kubemoot.agent.provider.CapacityWait.NONE);
    }

    /**
     * The discussion contribution path with a capacity wait: when every GPU that
     * could hold the model is busy, the call waits for capacity (announcing it
     * through {@code wait}) instead of standing aside at once.
     */
    public ChatResult directChat(ChatRequest request, boolean skipRag,
                                 ai.kubemoot.agent.provider.CapacityWait wait) {
        boolean toolerRawOutput = "tooler".equals(properties.discuss().role());
        return directLlmCall(request, skipRag, toolerRawOutput, wait);
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
        return directLlmCall(request, skipRag, toolerRawOutput, ai.kubemoot.agent.provider.CapacityWait.NONE);
    }

    private ChatResult directLlmCall(ChatRequest request, boolean skipRag, boolean toolerRawOutput,
                                     ai.kubemoot.agent.provider.CapacityWait wait) {
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

        // JIT provider selection is hoisted OUT of callWithToolLoop so
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
        MullingPick pick = pickMullingChatModel(promptCharCount, wait, request.discussionThreadId());
        String providerName = pick.providerName();
        // Outbound wire trace: the exact model + endpoint this call will send to
        // Ollama. Pair with OLLAMA_DEBUG on the provider to confirm the wire model
        // matches what the scheduler intended (the 2026-06-11 spill: an 8B agent's
        // request to the 4090 loaded 32B - this line records what we believe we sent).
        log.info("OUTBOUND inference (tool-loop): agent={} endpoint={} model={} provider={} reason={}",
                properties.agentName(), pick.endpoint(), pick.modelName(),
                providerName.isEmpty() ? "static" : providerName, pick.pickReason());
        long callStartMs = System.currentTimeMillis();
        ToolLoopResult loopResult;
        // Stays true for any Throwable escaping the loop (timeouts, 5xx, ToolCallFailure
        // with an infrastructure cause, or an Error): the circuit-breaker input.
        boolean callFailed = true;
        try {
            loopResult = callWithToolLoop(messages, pick.model(), providerName, pick.pickReason(), toolerRawOutput,
                    contextLengthOf(pick));
            callFailed = false;
        } finally {
            // ALWAYS record observed latency, even on ToolCallFailure -
            // the failed-call duration is exactly the signal we need to
            // steer subsequent picks away from the bad provider.
            long callDurationMs = System.currentTimeMillis() - callStartMs;
            recordSchedulerOutcome(pick, providerName, callDurationMs, callFailed);
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
                loopResult.providerName(), loopResult.pickReason(), pick.evicted());
    }

    /**
     * Result of the tool loop including accumulated token counts AND the
     * provider name the JIT-selector chose for this call. providerName is
     * empty when the call fell back to the static Quarkus-injected
     * ChatModel (selector unwired or NATS unavailable). Used by the
     * caller to attribute the inference to a specific provider in the
     * agent's outbound signal metadata ([[Per-Call Provider Attribution]]).
     */
    record ToolLoopResult(String text, long inputTokens, long outputTokens,
                                   String providerName, String pickReason) {}

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
     * 2026-05-26 in discussion dccdb33f: nvidia-gpu's ProviderSelector
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
        MullingPick pick = pickMullingChatModel(PromptSize.chars(messages, tools().specs()));
        try {
            // Legacy/test entry defaults to reasoning behavior, with no raw output;
            // the tooler raw-output contract is opt-in via the 5-arg overload.
            return callWithToolLoop(messages, pick.model(), pick.providerName(), pick.pickReason(), false,
                    contextLengthOf(pick));
        } finally {
            // v2 release safety: this overload is the test/legacy entry
            // point (the primary directLlmCall hoists pickMullingChatModel
            // for latency capture). Still drop the ticket on every path
            // so callers via this overload don't leak VRAM reservations.
            if (ticketManager != null) {
                pick.ticket().ifPresent(ticketManager::release);
            }
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
                                     String providerName,
                                     String pickReason,
                                     boolean toolerRawOutput) {
        return callWithToolLoop(messages, modelForThisCall, providerName, pickReason, toolerRawOutput, 0L);
    }

    /**
     * {@link #callWithToolLoop(List, ChatModel, String, String, boolean)} against a
     * provider that gives the model {@code contextLength} tokens per request (zero
     * when unknown). Each turn fails with CONTEXT_EXCEEDED instead of sending a
     * prompt the engine would cut.
     */
    ToolLoopResult callWithToolLoop(List<ChatMessage> messages,
                                     ChatModel modelForThisCall,
                                     String providerName,
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
                    providerName, pickReason);
            if (gathered.isPresent()) {
                return gathered.get();
            }

            var aiMessage = invokeModelForLoop(modelForThisCall, state);

            // If no tool calls, decide the final text (or retry an empty turn).
            if (!aiMessage.hasToolExecutionRequests()) {
                var done = resolveNoToolResult(aiMessage, state, toolerRawOutput,
                        providerName, pickReason);
                if (done.isPresent()) {
                    return done.get();
                }
                continue; // empty turn: retry budget consumed, loop again
            }

            executeToolCalls(aiMessage, state);
        }

        return exhaustedIterationsResult(state, maxIterations, toolerRawOutput,
                providerName, pickReason);
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
     * Decide what happens when the next prompt would not fit the provider's
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
            String providerName, String pickReason) {
        if (contextLength <= 0) {
            return java.util.Optional.empty();
        }
        long next = PromptSize.nextPromptTokens(state.allMessages, tools().specs(),
                state.lastPromptTokens, state.lastReplyTokens, state.messagesAtLastCall, charsPerToken);
        if (next <= contextLength) {
            return java.util.Optional.empty();
        }
        if (toolerRawOutput && state.toolsExecuted && state.toolOutput.length() > 0) {
            if (state.totalToolFailures >= state.toolCalls) {
                throw new ToolCallFailure(ToolCallFailure.FailureType.GATHER_FAILED, null, null, iteration,
                        "every tool call failed and the next prompt (~" + next + " tokens) exceeds the "
                                + contextLength + "-token context window");
            }
            log.info("agent {}: the next prompt (~{} tokens) exceeds the {}-token context; "
                    + "contributing the tool output gathered in {} iterations", properties.agentName(),
                    next, contextLength, iteration);
            return java.util.Optional.of(rawOutputResult(state, providerName, pickReason));
        }
        throw new ToolCallFailure(ToolCallFailure.FailureType.CONTEXT_EXCEEDED,
                null, null, iteration,
                "The next prompt (~" + next + " tokens) exceeds the " + contextLength
                        + "-token context window of the chosen provider at iteration " + iteration);
    }

    /**
     * The context window, in tokens, the picked provider gives the picked model;
     * zero when the pick is static or the provider publishes no context.
     */
    private long contextLengthOf(MullingPick pick) {
        if (providerSelector == null || pick.providerName().isEmpty()) {
            return 0L;
        }
        return providerSelector.readState().stream()
                .filter(p -> p.name().equals(pick.providerName()))
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
            String providerName, String pickReason) {
        // Metrics drill contract runs BEFORE the tooler raw-output return: the
        // metrics toolers post raw output, so their empty-query "unavailable" would
        // otherwise ship without ever trying a discovery pass. A re-prompt returns
        // empty (caller continues the loop); a satisfied/inapplicable contract falls
        // through. Honest escapes (NO_DATA / TOOL_GAP) are handled just below.
        if (metricsDrillNeedsRetry(state)) {
            return java.util.Optional.empty();
        }
        if (toolerRawOutput && state.toolsExecuted) {
            return java.util.Optional.of(toolerRawOutputResult(aiMessage, state, providerName, pickReason));
        }
        // Compute contract: a compute agent must produce its result by RUNNING
        // execute_code, and when the data was spilled to an artifact it must read
        // the materialized FILE - never tally the truncated inline preview. The
        // helper re-prompts (returns true) or, once the budget is spent, throws.
        if (computeContractNeedsRetry(aiMessage, state)) {
            return java.util.Optional.empty();
        }
        // No raw-output contract (or no tools ran): this agent reasons to its own answer.
        return resolveReasonedText(aiMessage, state, providerName, pickReason);
    }

    /**
     * The contribution of a tooler whose tools ran (raw-output contract). The
     * tooler's NO_DATA declaration raises GATHER_FAILED, a TOOL_GAP declaration is
     * kept as the contribution, and anything else returns the raw tool output.
     */
    private ToolLoopResult toolerRawOutputResult(AiMessage aiMessage, ToolLoopState state,
                                                 String providerName, String pickReason) {
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
                    state.totalInput, state.totalOutput, providerName, pickReason);
        }
        return rawOutputResult(state, providerName, pickReason);
    }

    /**
     * The contribution of an agent that reasons to its own answer: its prose, with
     * one retry (empty Optional) for a flaky empty turn and an echo guard so it never
     * publishes its own instruction prompt as the answer.
     */
    private java.util.Optional<ToolLoopResult> resolveReasonedText(AiMessage aiMessage, ToolLoopState state,
                                                                   String providerName, String pickReason) {
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
                providerName, pickReason));
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

    /** Execute every tool call in the turn, accumulating output and enforcing failure caps. */
    private void executeToolCalls(AiMessage aiMessage, ToolLoopState state) {
        for (var toolRequest : aiMessage.toolExecutionRequests()) {
            var executor = findExecutor(toolRequest.name());
            String result = executor != null
                    ? executor.execute(toolRequest, null)
                    : "{\"error\": \"Unknown tool: " + toolRequest.name() + "\"}";
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
            recordToolFailure(toolRequest.name(), result, state);
        }
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
    private static ToolLoopResult rawOutputResult(ToolLoopState state, String providerName, String pickReason) {
        return new ToolLoopResult(state.toolOutput.toString().strip(),
                state.totalInput, state.totalOutput, providerName, pickReason);
    }

    /**
     * Build the result when the loop exhausts its iteration budget. If a tooler
     * gathered raw output, post it as the contribution (the Analyst/Coordinator
     * turn it into an answer) rather than failing outright; otherwise throw
     * ITERATIONS_EXHAUSTED.
     */
    private ToolLoopResult exhaustedIterationsResult(ToolLoopState state, int maxIterations,
            boolean toolerRawOutput, String providerName, String pickReason) {
        if (toolerRawOutput && state.toolsExecuted && state.toolOutput.length() > 0) {
            log.info("iterations-exhausted for tooler {}: posting {} chars of raw tool output as the contribution",
                    properties.agentName(), state.toolOutput.length());
            return rawOutputResult(state, providerName, pickReason);
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
     * of the provider it routes to (empty string when falling back to
     * the static Quarkus-injected chatModel). Carries the providerName
     * up the call chain so DiscussionSubscriber can include it in the
     * agent's signal metadata for per-call attribution in the dashboard.
     * See [[Per-Call Provider Attribution]] (Epic - JIT GPU Scheduling).
     */
    private record MullingPick(ChatModel model, String providerName,
                                java.util.Optional<ai.kubemoot.agent.provider.Ticket> ticket,
                                String pickReason, String modelName, String endpoint,
                                long occupancyMiB, java.util.List<String> evicted) {
        MullingPick(ChatModel model, String providerName,
                    java.util.Optional<ai.kubemoot.agent.provider.Ticket> ticket,
                    String pickReason, String modelName, String endpoint, long occupancyMiB) {
            this(model, providerName, ticket, pickReason, modelName, endpoint, occupancyMiB, java.util.List.of());
        }

        MullingPick withEvicted(java.util.List<String> models) {
            return new MullingPick(model, providerName, ticket, pickReason, modelName, endpoint, occupancyMiB, models);
        }
    }

    /**
     * Resolve a model's cold-load occupancy (MiB) for JIT placement.
     *
     * Resolution order:
     * <ol>
     *   <li>NATS KV {@code kubemoot_provider_state}: the operator publishes
     *       per-model footprints from /api/ps (loaded) and /api/tags (on-disk).
     *       This is the normal, fast path.</li>
     *   <li>Direct HTTP self-fetch ({@link OllamaDirectProber}): when NATS returns
     *       nothing (stalled, operator restart window), probe each provider's
     *       /api/ps and /api/tags directly at decision time. This makes the
     *       fit-gate NATS-INDEPENDENT — the 2026-06-14 failure mode (NATS stalled
     *       -> degraded gate -> 32b spilled onto 4090) cannot recur. Prefers an
     *       observed warm footprint (/api/ps) over the on-disk proxy (/api/tags).
     *       See [[JIT Fit-Gate Degraded Mode Can Spill]].</li>
     *   <li>Parameter-count estimate (KvCacheEstimator): when even direct probes
     *       yield nothing (model not downloaded on any provider), fall back to a
     *       rough bytes-per-parameter estimate so a ballpark fit check is possible.</li>
     * </ol>
     *
     * Shared by the mulling and triage paths so BOTH place via the same scheduler.
     * Returns 0 only when the model is genuinely unknown on every provider AND the
     * name contains no parseable parameter count.
     */
    private long resolveOccupancyMiB(java.util.List<ProviderState> states, String modelName) {
        long fp = states.stream()
                .mapToLong(p -> p.coldLoadFootprintMiB(modelName))
                .filter(f -> f > 0L)
                .max()
                .orElse(0L);
        if (fp > 0) return fp;

        // KV returned nothing - self-fetch directly from provider endpoints.
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
        return ai.kubemoot.agent.provider.KvCacheEstimator.estimateColdOccupancyMiB(modelName);
    }

    /**
     * Pick the ChatModel for THIS mulling call via JIT provider selection and
     * claim a VRAM ticket on the chosen provider. Falls back to the static
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
    private MullingPick pickMullingChatModel(int promptCharCount) {
        return pickMullingChatModel(promptCharCount, ai.kubemoot.agent.provider.CapacityWait.NONE, null);
    }

    private MullingPick pickMullingChatModel(int promptCharCount, ai.kubemoot.agent.provider.CapacityWait wait,
                                             String threadId) {
        var staticModel = properties.model().model();
        var staticEndpoint = properties.model().endpoint();

        // Static-fallback guards (selector/pool unwired, ticket manager unwired,
        // or footprint unknown/unestimable). Resolution order for the footprint
        // is documented on resolveOccupancyMiB; zero means we cannot gate it.
        var states = (providerSelector != null) ? providerSelector.readState() : null;
        long coldLoadFootprintMiB = (states != null) ? resolveOccupancyMiB(states, staticModel) : 0L;
        var fallback = staticFallbackPick(staticModel, staticEndpoint, staticModel, coldLoadFootprintMiB);
        if (fallback.isPresent()) {
            return fallback.get();
        }
        // staticFallbackPick is always present when providerSelector is null, and
        // this explicit guard keeps the dereferences below locally provable.
        if (providerSelector == null) {
            return new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK, staticModel, staticEndpoint, 0L);
        }
        var placed = heldPlacement(threadId, states, promptTokens(promptCharCount))
                .or(() -> placeCall(states, promptCharCount));
        MullingPick pick = placed.isPresent() ? placed.get()
                : waitForCapacity(staticModel, coldLoadFootprintMiB, promptCharCount, wait);
        callPlanner.callStarted(threadId, pick.modelName());
        return pick;
    }

    /**
     * The placement planned for this thread when the coordinator selected the
     * agent, when its provider is still ready and its context holds the prompt
     * (the plan was made before the prompt existed); otherwise its claim is
     * released and the call plans afresh.
     */
    private java.util.Optional<MullingPick> heldPlacement(String threadId, java.util.List<ProviderState> states,
                                                          long promptTokens) {
        var held = callPlanner.takeHeld(threadId);
        if (held.isEmpty()) {
            return java.util.Optional.empty();
        }
        String provider = held.get().pick().provider().name();
        String model = held.get().model();
        boolean usable = states.stream().anyMatch(p -> p.name().equals(provider) && p.ready()
                && ai.kubemoot.agent.provider.ContextFit.holds(p, model, promptTokens));
        if (!usable) {
            ticketManager.release(held.get().pick().ticket());
            return java.util.Optional.empty();
        }
        log.info("Using the placement planned at selection: {} on {}", held.get().model(), provider);
        return java.util.Optional.of(fromPlacement(held.get()));
    }

    /**
     * Nothing has room for the bound model now. Stand aside with reason
     * model-too-large when no known GPU can ever hold it; otherwise wait for a
     * capacity change and retry {@link #placeCall} on each one. The wait is
     * published to the shared demand view while it lasts.
     */
    private MullingPick waitForCapacity(String model, long footprintMiB, int promptCharCount,
                                        ai.kubemoot.agent.provider.CapacityWait wait) {
        if (!ProviderSelector.canEverHold(providerSelector.readState(), footprintMiB)) {
            log.info("No GPU can hold {} (coldLoad {}MiB) - stand-aside", model, footprintMiB);
            throw ai.kubemoot.agent.provider.NoFitException.modelTooLarge(model,
                    "model=" + model + " coldLoad=" + footprintMiB + "MiB exceeds every GPU's usable VRAM");
        }
        requireSomeContextHolds(model, promptTokens(promptCharCount));
        String busy = "model=" + model + " coldLoad=" + footprintMiB + "MiB: every GPU that can hold it is busy";
        if (capacityWaiter == null || wait.threadEnded()) {
            throw ai.kubemoot.agent.provider.NoFitException.gpuBusy(model, busy);
        }
        callPlanner.waitStarted(model);
        try {
            return capacityWaiter.await(model, () -> {
                providerSelector.invalidateCache();
                return placeCall(providerSelector.readState(), promptCharCount);
            }, wait);
        } finally {
            callPlanner.waitEnded(model);
        }
    }

    /**
     * Stand aside with reason prompt-too-large when no provider can ever run an
     * acceptable model with a context that holds the prompt (VRAM and context
     * judged for the same provider and model): no capacity change can place it,
     * and sending it anyway lets the engine drop the question.
     */
    private void requireSomeContextHolds(String model, long promptTokens) {
        var states = providerSelector.readState();
        java.util.List<String> models = new ArrayList<>();
        models.add(model);
        models.addAll(mullingAlternatives(model));
        java.util.function.ToLongFunction<String> footprint =
                m -> m.equals(model) ? resolveOccupancyMiB(states, m) : observedFootprintMiB(states, m);
        if (!ai.kubemoot.agent.provider.ContextFit.anyCanRun(states, models, promptTokens, footprint)) {
            long largest = ai.kubemoot.agent.provider.ContextFit.largestContext(states, models);
            log.info("Prompt of ~{} tokens exceeds every context window for {} (largest {}) - stand-aside",
                    promptTokens, models, largest);
            throw ai.kubemoot.agent.provider.NoFitException.promptTooLarge(model,
                    "prompt ~" + promptTokens + " tokens exceeds the largest context window (" + largest
                            + " tokens) any provider gives " + models);
        }
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
     * One placement attempt against the given provider state; empty when no
     * provider has room right now. {@link CallPlanner#place} decides; with no
     * provider state (NATS degraded), the direct-probe path decides.
     */
    private java.util.Optional<MullingPick> placeCall(java.util.List<ProviderState> states, int promptCharCount) {
        String preferred = properties.model().model();
        if (states.isEmpty()) {
            return degradedMullingPick(preferred, properties.model().endpoint(),
                    resolveOccupancyMiB(states, preferred));
        }
        return callPlanner.place(placementRequest(states, promptCharCount)).map(this::fromPlacement);
    }

    private CallPlanner.PlacementRequest placementRequest(java.util.List<ProviderState> states, int promptCharCount) {
        String preferred = properties.model().model();
        // Phase D: each model's KV-cache need for this call, so the predictor's gate
        // factors in in-flight context-size pressure. Conservative (overestimate).
        long kvTokens = ai.kubemoot.agent.provider.KvCacheEstimator.estimateTokensFromChars(promptCharCount);
        return new CallPlanner.PlacementRequest(preferred, mullingAlternatives(preferred), states,
                m -> m.equals(preferred) ? resolveOccupancyMiB(states, m) : observedFootprintMiB(states, m),
                m -> ai.kubemoot.agent.provider.KvCacheEstimator.estimateMiB(m, kvTokens, properties.model().maxTokens()),
                promptTokens(promptCharCount));
    }

    private MullingPick fromPlacement(CallPlanner.Placement p) {
        MullingPick pick = buildScheduledMullingPick(p.pick(), p.model(), p.footprintMiB());
        return p.evicted().isEmpty() ? pick : pick.withEvicted(p.evicted());
    }

    private java.util.List<String> mullingAlternatives(String preferred) {
        return candidatePolicy == null ? java.util.List.of() : candidatePolicy.mullingAlternatives(preferred);
    }

    /** A model's KV-published footprint; zero when no provider reports it. */
    private static long observedFootprintMiB(java.util.List<ProviderState> states, String model) {
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
     * conversation), so it lands on a provider whose context holds the first call
     * instead of starting a load the real prompt then abandons. Runs on the
     * caller's thread; the discussion subscriber calls it from a worker.
     */
    public void commitToThread(String threadId, long selectedAtMs, String conversation) {
        if (providerSelector == null || ticketManager == null || chatModelPool == null) {
            return;
        }
        String preferred = properties.model().model();
        java.util.List<String> candidates = new ArrayList<>();
        candidates.add(preferred);
        candidates.addAll(mullingAlternatives(preferred));
        int planChars = planPromptChars(conversation);
        callPlanner.commit(threadId, selectedAtMs, candidates, () -> {
            var states = providerSelector.readState();
            return states.isEmpty() ? java.util.Optional.empty()
                    : callPlanner.place(placementRequest(states, planChars));
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
     * observed on any provider nor estimable from the model name. Empty means the
     * caller should proceed to pickAndClaim with the (positive) footprint.
     */
    private java.util.Optional<MullingPick> staticFallbackPick(String staticModel,
            String staticEndpoint, String modelName, long coldLoadFootprintMiB) {
        if (providerSelector == null || chatModelPool == null) {
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK, staticModel, staticEndpoint, 0L));
        }
        if (ticketManager == null) {
            log.debug("TicketManager unwired - fallback to static endpoint (tests/degraded)");
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK, staticModel, staticEndpoint, 0L));
        }
        if (coldLoadFootprintMiB <= 0) {
            // Footprint neither observed on any provider NOR estimable from the
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
     * Why there is no provider state: the NATS read failed (with its error), or the
     * bucket had no current entries.
     */
    private String noStateReason() {
        return providerSelector == null ? "no scheduler"
                : providerSelector.lastReadError().map(e -> "provider state read failed: " + e)
                        .orElse("no provider state published");
    }

    /**
     * Placement with no provider state in NATS (degraded window). The direct
     * prober checks each endpoint for one that passes the VRAM fit gate; it is
     * used when found. Empty when none fits: without provider VRAM data the
     * runtime cannot tell a busy cluster from a model that never fits, so the
     * caller treats it as busy and waits for provider state to return. With no
     * prober, the static endpoint is the last resort. See [[JIT Fit-Gate Degraded
     * Mode Can Spill]].
     */
    private java.util.Optional<MullingPick> degradedMullingPick(String modelName, String staticEndpoint,
                                                                long coldLoadFootprintMiB) {
        if (directProber == null) {
            log.debug("No NATS provider state and no directProber - last-resort static endpoint");
            return java.util.Optional.of(new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK + " (nats degraded)", modelName, staticEndpoint, 0L));
        }
        String fittingEndpoint = directProber.pickFittingEndpoint(
                java.util.List.of(), staticEndpoint, modelName, coldLoadFootprintMiB);
        if (fittingEndpoint == null) {
            log.info("No provider state ({}): directProber found no fitting endpoint for {} (coldLoad {}MiB) - waiting for capacity",
                    noStateReason(), modelName, coldLoadFootprintMiB);
            return java.util.Optional.empty();
        }
        log.info("No provider state ({}): fit pick endpoint={} model={} coldLoad={}MiB (direct probe)",
                noStateReason(), fittingEndpoint, modelName, coldLoadFootprintMiB);
        ChatModel degradedModel = chatModelPool.forEndpoint(
                fittingEndpoint,
                modelName,
                properties.model().temperature(),
                properties.model().maxTokens(),
                java.time.Duration.ofMinutes(5),
                properties.model().think().orElse(null));
        return java.util.Optional.of(new MullingPick(degradedModel, "", java.util.Optional.empty(),
                STATIC_FALLBACK + " (nats degraded, fit-gated)", modelName, fittingEndpoint, coldLoadFootprintMiB));
    }

    /** Build the MullingPick for a provider the scheduler chose and a ticket it claimed. */
    private MullingPick buildScheduledMullingPick(
            ai.kubemoot.agent.provider.Pick p,
            String modelName, long coldLoadFootprintMiB) {
        var ps = p.provider();
        // INFO (placement visibility): which provider the scheduler chose, the
        // model, and the cold-load footprint estimate that drove the fit gate.
        // This is the trace needed to verify the scheduler keeps oversized models
        // off too-small GPUs; promote from debug so a live run shows every pick.
        log.info("JIT pick: provider={} endpoint={} model={} coldLoadFootprintEstimate={}MiB totalVram={}MiB",
                ps.name(), ps.endpoint(), modelName, coldLoadFootprintMiB, ps.totalVramMiB());
        ChatModel built = chatModelPool.forEndpoint(
                ps.endpoint(),
                modelName,
                properties.model().temperature(),
                properties.model().maxTokens(),
                // Mulling timeout: 5 min matches the homelab coordinator's
                // existing QUARKUS_LANGCHAIN4J_OLLAMA_TIMEOUT=300s setting.
                // Generous because tool-loop iterations chain multiple
                // inferences; individual tool failures are already bounded
                // by ToolCallFailure thresholds in callWithToolLoop.
                java.time.Duration.ofMinutes(5),
                // Per-agent thinking control; empty leaves the model default.
                properties.model().think().orElse(null)
        );
        return new MullingPick(built, ps.name(), java.util.Optional.of(p.ticket()),
                p.score() != null ? p.score().reasoning() : "", modelName, ps.endpoint(), coldLoadFootprintMiB);
    }

    private ToolExecutor findExecutor(String toolName) {
        for (var entry : tools().executors().entrySet()) {
            if (entry.getKey().name().equals(toolName)) {
                return entry.getValue();
            }
        }
        return null;
    }

    /** Result of a simple (no-tool) LLM call with token usage. */
    public record SimpleLlmResult(String text, long inputTokens, long outputTokens) {}

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
        // provider and claims/releases a VRAM ticket so concurrent calls account
        // for each other. See [[JIT is the sole scheduler]].
        int promptChars = (systemPrompt == null ? 0 : systemPrompt.length())
                + (userPrompt == null ? 0 : userPrompt.length());
        MullingPick pick = pickForSimpleCall(promptChars);
        String providerName = pick.providerName();
        // Outbound wire trace (no-tools path: advisory/synthesis/triage). Pair with
        // OLLAMA_DEBUG on the provider to confirm the wire model matches intent.
        log.info("OUTBOUND inference (simple): agent={} endpoint={} model={} provider={} reason={}",
                properties.agentName(), pick.endpoint(), pick.modelName(),
                providerName.isEmpty() ? "static" : providerName, pick.pickReason());
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
            recordSchedulerOutcome(pick, providerName, callDurationMs, callFailed);
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
     * JIT-pick a provider for a no-tool coordinator call. When every GPU is busy or
     * none can hold the model, the coordinator MUST still complete its
     * synthesis/advisory, so it falls back to the static endpoint the operator
     * assigned: a last resort, never a routine bypass. A prompt larger than every
     * context is rethrown instead, because the static endpoint would cut it without
     * an error; every caller handles a failed call visibly.
     */
    private MullingPick pickForSimpleCall(int promptChars) {
        try {
            return pickMullingChatModel(promptChars);
        } catch (ai.kubemoot.agent.provider.NoFitException nfe) {
            if (ai.kubemoot.agent.provider.NoFitException.REASON_PROMPT_TOO_LARGE.equals(nfe.reason())) {
                throw nfe;
            }
            log.info("simpleLlmCall: no provider fit ({}) - falling back to static endpoint", nfe.predictorReason());
            return new MullingPick(chatModel, "", java.util.Optional.empty(),
                    STATIC_FALLBACK + " (no-fit)", properties.model().model(), properties.model().endpoint(), 0L);
        }
    }

    /** Send a no-tool chat request, record load_duration, and return text + token counts. */
    private SimpleLlmResult invokeSimpleCall(MullingPick pick, List<ChatMessage> messages) {
        var response = pick.model().chat(dev.langchain4j.model.chat.request.ChatRequest.builder()
                .messages(messages)
                .build());
        long inTok = 0;
        long outTok = 0;
        if (response.tokenUsage() != null) {
            inTok = response.tokenUsage().inputTokenCount();
            outTok = response.tokenUsage().outputTokenCount();
        }
        // Capture Ollama's load_duration from the response metadata if available.
        lastLoadDurationMs = extractLoadDurationMs(response);
        return new SimpleLlmResult(response.aiMessage().text(), inTok, outTok);
    }

    /**
     * Shared post-call scheduler bookkeeping for the JIT-selected provider:
     * record observed latency, success/failure for the circuit breaker, the
     * per-(provider, model) outcome EMA, the residency overlay on success, and
     * finally release the held VRAM ticket. No-op for the static-fallback path
     * (empty providerName). Extracted from directLlmCall and
     * simpleLlmCallWithTokens, which performed the identical sequence.
     */
    private void recordSchedulerOutcome(MullingPick pick, String providerName,
                                        long callDurationMs, boolean callFailed) {
        if (providerSelector != null && providerName != null && !providerName.isEmpty()) {
            providerSelector.readState().stream()
                    .filter(p -> p.name().equals(providerName))
                    .findFirst()
                    .ifPresent(p -> providerSelector.recordObservedLatency(p.endpoint(), callDurationMs));
            // AI Circuit Breaker (Phase A): record success/failure so a wedged
            // provider (HTTP 500 / timeout cluster) is excluded from future picks
            // for CIRCUIT_COOLDOWN. Per-agent local; faster than the operator's
            // 30s probe.
            if (callFailed) {
                providerSelector.recordFailure(providerName);
            } else {
                providerSelector.recordSuccess(providerName);
            }
            // FitPredictor v2 learning: feed the per-(provider, model) EMA so the
            // selector's ranking weighs historical reliability.
            providerSelector.recordOutcome(providerName, pick.modelName(),
                    callDurationMs, !callFailed);
            // Residency overlay (anti-thrash): on success the model just ran on
            // this provider, so it is resident now. Record it immediately so other
            // agents see it without waiting for the ~30s probe.
            if (!callFailed && ticketManager != null && pick.occupancyMiB() > 0) {
                ticketManager.recordResidency(providerName, pick.modelName(), pick.occupancyMiB());
            }
        }
        // v2 release: drop the held VRAM ticket so the next call sees the freed
        // budget. Fires on EVERY path because the bucket TTL is only the safety
        // net; the finally-block delete is the fast reclaim. See docs/scheduler.md
        // "two-layer release guarantee".
        if (ticketManager != null) {
            pick.ticket().ifPresent(ticketManager::release);
        }
    }

    /** Extract the text payload of a chat message, or null when it carries none. */
    private static String messageText(ChatMessage m) {
        if (m == null) return null;
        if (m instanceof dev.langchain4j.data.message.SystemMessage sm) return sm.text();
        if (m instanceof dev.langchain4j.data.message.UserMessage um) return um.singleText();
        if (m instanceof dev.langchain4j.data.message.AiMessage am) return am.text();
        return null;
    }

    /**
     * Returns the Ollama `load_duration` in milliseconds from the most recent
     * `simpleLlmCallWithTokens` call. 0 if the model was already warm or if
     * the underlying ChatModel doesn't expose the field.
     */
    public long getLastLoadDurationMs() {
        return lastLoadDurationMs;
    }

    /**
     * Reflection-based extraction of `load_duration` (nanoseconds, per Ollama
     * convention) from a LangChain4j ChatResponse's metadata. Tries common
     * accessor patterns the Ollama provider may expose:
     *
     *   - response.metadata() has a `loadDuration()` / `getLoadDuration()` accessor
     *   - or a `loadDurationNanos()` accessor
     *   - or the response itself carries `loadDuration()` on a provider-specific subclass
     *
     * If nothing matches, logs once and returns 0 — the dashboard simply
     * doesn't render the load-prefix in that case.
     */
    private long extractLoadDurationMs(dev.langchain4j.model.chat.response.ChatResponse response) {
        if (response == null) return 0;
        try {
            // Probe metadata() first (the spec-compliant location).
            var meta = response.metadata();
            Long ns = invokeLongAccessor(meta, "loadDuration", "getLoadDuration", "loadDurationNanos");
            if (ns == null) {
                // Some providers expose it directly on the response wrapper.
                ns = invokeLongAccessor(response, "loadDuration", "getLoadDuration", "loadDurationNanos");
            }
            if (ns != null && ns > 0) {
                return Math.round(ns / 1_000_000.0);
            }
        } catch (Exception e) {
            log.debug("load_duration extraction threw: {}", e.getMessage());
        }
        if (!loadDurationProbed) {
            loadDurationProbed = true;
            log.info("LangChain4j ChatResponse does not surface load_duration on this provider — dashboard will not render the model-load span prefix.");
        }
        return 0;
    }

    private static Long invokeLongAccessor(Object target, String... names) {
        if (target == null) return null;
        Class<?> cls = target.getClass();
        for (String name : names) {
            try {
                var method = cls.getMethod(name);
                Object value = method.invoke(target);
                if (value instanceof Number n) {
                    long v = n.longValue();
                    if (v > 0) return v;
                }
                if (value instanceof java.time.Duration d) {
                    return d.toNanos();
                }
            } catch (NoSuchMethodException ignored) {
                // try next
            } catch (Exception e) {
                // accessor exists but errored — give up on this target
                return null;
            }
        }
        return null;
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
        // Route the triage model's PLACEMENT through the same JIT scheduler as
        // mulling. Triage stays a native-safe direct-HTTP Ollama call, but WHERE it
        // runs is now a scheduler decision (best-fit / fit / residency / eviction +
        // a claimed VRAM ticket), not a static endpoint baked in by the operator.
        // Before this, triage was the one inference path that bypassed the scheduler
        // entirely, so an 8B triage call pinned to the big GPU loaded 8B there and
        // evicted the resident big model (the 2026-06-11 thrash). Falls back to the
        // static triageEndpoint only when the scheduler cannot decide (NATS down).
        TriagePlacement placement = pickTriagePlacement(systemPrompt, conversation);
        String endpoint = placement.endpoint();

        long startMs = System.currentTimeMillis();
        boolean ok = false;
        try {
            var messages = new ArrayList<Map<String, String>>();
            if (systemPrompt != null && !systemPrompt.isEmpty()) {
                messages.add(Map.of(OLLAMA_ROLE, "system", OLLAMA_CONTENT, systemPrompt));
            }
            messages.add(Map.of(OLLAMA_ROLE, "user", OLLAMA_CONTENT, conversation));

            var body = objectMapper.writeValueAsString(Map.of(
                    "model", triageModelId,
                    "messages", messages,
                    "stream", false,
                    "options", Map.of("temperature", 0.3, "num_predict", 2048)
            ));

            var request = HttpRequest.newBuilder()
                    .uri(URI.create(endpoint + "/api/chat"))
                    .header("Content-Type", "application/json")
                    .timeout(Duration.ofSeconds(properties.triageModel().timeoutSeconds()))
                    .POST(HttpRequest.BodyPublishers.ofString(body))
                    .build();

            var response = triageHttpClient.send(request, HttpResponse.BodyHandlers.ofString());

            if (response.statusCode() != 200) {
                throw new IOException("Ollama returned " + response.statusCode() + ": " + response.body());
            }

            SimpleLlmResult result = parseOllamaChat(objectMapper.readTree(response.body()));
            ok = true;
            return result;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new java.io.UncheckedIOException(new IOException("Triage call interrupted", e));
        } catch (IOException e) {
            throw new java.io.UncheckedIOException("Triage call failed: " + e.getMessage(), e);
        } finally {
            long durMs = System.currentTimeMillis() - startMs;
            recordTriageOutcome(placement, durMs, ok);
        }
    }

    /**
     * JIT placement decision for a triage call: the endpoint to send to, the
     * chosen provider name (empty when falling back to the static triageEndpoint),
     * the model occupancy used to claim VRAM, and the held ticket. Extracted from
     * triageChatWithTokens so the call body stays under the complexity gate.
     */
    private record TriagePlacement(String endpoint, String pickedProvider, long occupancy,
                                   java.util.Optional<ai.kubemoot.agent.provider.Ticket> ticket) {}

    /**
     * Route the triage model's PLACEMENT through the same JIT scheduler as
     * mulling. Returns the static triageEndpoint (no provider claimed) when the
     * scheduler is unwired, NATS is down, the footprint is unknown, or no provider
     * fits. See the triageChatWithTokens javadoc for the 2026-06-11 thrash this fixes.
     */
    private TriagePlacement pickTriagePlacement(String systemPrompt, String conversation) {
        if (providerSelector == null || ticketManager == null) {
            return new TriagePlacement(triageEndpoint, "", 0L, java.util.Optional.empty());
        }
        var states = providerSelector.readState();
        if (states.isEmpty()) {
            return new TriagePlacement(triageEndpoint, "", 0L, java.util.Optional.empty());
        }
        long occupancy = resolveOccupancyMiB(states, triageModelId);
        if (occupancy <= 0) {
            return new TriagePlacement(triageEndpoint, "", 0L, java.util.Optional.empty());
        }
        int promptChars = lengthOrZero(systemPrompt) + lengthOrZero(conversation);
        long kv = ai.kubemoot.agent.provider.KvCacheEstimator.estimateMiB(triageModelId,
                ai.kubemoot.agent.provider.KvCacheEstimator.estimateTokensFromChars(promptChars), 2048);
        long promptTokens = promptTokens(promptChars);
        requireTriageContextFits(states, promptTokens);
        return claimTriagePlacement(occupancy, kv, promptTokens);
    }

    private static int lengthOrZero(String text) {
        return text == null ? 0 : text.length();
    }

    /** Throws NoFitException when no provider's context window can hold the triage prompt. */
    private void requireTriageContextFits(List<ProviderState> states, long promptTokens) {
        if (!ai.kubemoot.agent.provider.ContextFit.anyCanHold(states, List.of(triageModelId), promptTokens)) {
            throw ai.kubemoot.agent.provider.NoFitException.promptTooLarge(triageModelId,
                    "triage prompt ~" + promptTokens + " tokens exceeds the largest context window ("
                            + ai.kubemoot.agent.provider.ContextFit.largestContext(states, List.of(triageModelId))
                            + " tokens) any provider gives " + triageModelId);
        }
    }

    /**
     * Claim a provider for the triage call through the JIT scheduler, or keep the
     * static triageEndpoint when no provider fits.
     */
    private TriagePlacement claimTriagePlacement(long occupancy, long kv, long promptTokens) {
        try {
            var pick = providerSelector.pickAndClaim(triageModelId, occupancy, kv, promptTokens);
            if (pick.isPresent()) {
                String endpoint = pick.get().provider().endpoint();
                String pickedProvider = pick.get().provider().name();
                log.info("OUTBOUND inference (triage): agent={} endpoint={} model={} provider={}",
                        properties.agentName(), endpoint, triageModelId, pickedProvider);
                return new TriagePlacement(endpoint, pickedProvider, occupancy,
                        java.util.Optional.of(pick.get().ticket()));
            }
            // pick empty (NoFit/degraded) -> keep the static triageEndpoint
        } catch (ai.kubemoot.agent.provider.NoFitException nfe) {
            log.info("triage: no provider fit ({}) - static endpoint", nfe.predictorReason());
        }
        return new TriagePlacement(triageEndpoint, "", occupancy, java.util.Optional.empty());
    }

    /**
     * Post-triage-call scheduler bookkeeping for a JIT-selected provider: record
     * latency, success/failure for the circuit breaker, residency on success, then
     * release the VRAM ticket. No-op when the static fallback was used (empty
     * provider name). Extracted from triageChatWithTokens's finally block.
     */
    private void recordTriageOutcome(TriagePlacement placement, long durMs, boolean ok) {
        if (providerSelector != null && !placement.pickedProvider().isEmpty()) {
            providerSelector.recordObservedLatency(placement.endpoint(), durMs);
            if (ok) {
                providerSelector.recordSuccess(placement.pickedProvider());
                if (ticketManager != null && placement.occupancy() > 0) {
                    ticketManager.recordResidency(placement.pickedProvider(), triageModelId, placement.occupancy());
                }
            } else {
                providerSelector.recordFailure(placement.pickedProvider());
            }
        }
        if (ticketManager != null) {
            placement.ticket().ifPresent(ticketManager::release);
        }
    }

    /**
     * Parse an Ollama /api/chat non-streaming response into text + token counts.
     * Uses readTree navigation (record deserialization fails silently in GraalVM
     * native — see CLAUDE.md). prompt_eval_count / eval_count are Ollama's input /
     * output token fields; absent when the response omits them → 0.
     */
    static SimpleLlmResult parseOllamaChat(com.fasterxml.jackson.databind.JsonNode responseBody) {
        String text = responseBody.path("message").path(OLLAMA_CONTENT).asText("");
        long inTok = responseBody.path("prompt_eval_count").asLong(0);
        long outTok = responseBody.path("eval_count").asLong(0);
        return new SimpleLlmResult(text, inTok, outTok);
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
     * what the documents should match.
     */
    public record ChatRequest(String conversationId, String message, String crew, String discussionThreadId,
                              String retrievalQuery) {
        public ChatRequest(String conversationId, String message, String crew, String discussionThreadId) {
            this(conversationId, message, crew, discussionThreadId, null);
        }
        public ChatRequest(String conversationId, String message, String crew) {
            this(conversationId, message, crew, null, null);
        }
        public ChatRequest(String conversationId, String message) {
            this(conversationId, message, null, null, null);
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

    public record ChatResult(String conversationId, String response, String model, String threadId,
                              long inputTokens, long outputTokens,
                              String providerName, String pickReason, List<String> evicted) {
        /** Without evictions: the call needed no model unloaded. */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens, String providerName, String pickReason) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, providerName, pickReason,
                    List.of());
        }
        /** Convenience constructor without token counts or provider attribution. */
        public ChatResult(String conversationId, String response, String model, String threadId) {
            this(conversationId, response, model, threadId, 0, 0, "", "");
        }
        /** Convenience constructor without provider attribution (legacy callers; pre-JIT). */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, "", "");
        }
        /** Convenience constructor without pickReason (callers pre-FitPredictor v2). */
        public ChatResult(String conversationId, String response, String model, String threadId,
                          long inputTokens, long outputTokens, String providerName) {
            this(conversationId, response, model, threadId, inputTokens, outputTokens, providerName, "");
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
