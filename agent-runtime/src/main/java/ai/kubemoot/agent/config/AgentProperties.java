package ai.kubemoot.agent.config;

import io.smallrye.config.ConfigMapping;
import io.smallrye.config.WithDefault;

import java.util.List;
import java.util.Optional;

/**
 * Configuration properties for the Kubemoot Agent Runtime.
 * All kubemoot.* properties are mapped here via SmallRye @ConfigMapping.
 * Environment variables are auto-mapped (e.g., KUBEMOOT_AGENT_NAME -> kubemoot.agent-name).
 */
@ConfigMapping(prefix = "kubemoot")
public interface AgentProperties {

    @WithDefault("kubemoot-agent")
    String agentName();

    @WithDefault("")
    String agentDescription();

    /**
     * Kubernetes namespace of this agent, from KUBEMOOT_NAMESPACE (the operator sets it
     * from the downward API). When absent, the service-account namespace file is used;
     * see {@link ai.kubemoot.agent.nats.CrewScope#fromProperties}. Every NATS subject and
     * key carries it first, so the same crew name runs in many namespaces without crosstalk.
     */
    Optional<String> namespace();

    /**
     * Crew this agent belongs to (e.g., "homelab-pilot"). Scopes NATS subjects and keys
     * within the agent's namespace; see {@link ai.kubemoot.agent.nats.CrewScope}.
     */
    Optional<String> crew();

    /**
     * Crew Helm chart version (provenance), from KUBEMOOT_CREW_VERSION. The operator
     * sets it from the crew chart's kubemoot.ai/crew-version label; absent for
     * hand-applied crews. Recorded in thread_start metadata so a thread is
     * attributable to a specific crew version.
     */
    Optional<String> crewVersion();

    @WithDefault("chat")
    String agentType();

    Optional<String> systemPrompt();

    Optional<String> systemPromptFile();

    Model model();

    TriageModel triageModel();

    Gateway gateway();

    Nats nats();

    Discuss discuss();

    Onboarding onboarding();

    Rtfm rtfm();

    Heartbeat heartbeat();

    Memory memory();

    Optional<List<RagSource>> ragSources();

    Optional<List<McpServer>> mcpServers();

    Optional<List<String>> enabledTools();

    Optional<List<String>> disabledTools();

    ResumeSearch resumeSearch();

    interface ResumeSearch {
        /** Endpoint for the resume query service (set by operator as KUBEMOOT_RESUME_SEARCH_ENDPOINT). */
        Optional<String> endpoint();
    }

    interface Nats {
        Optional<String> url();
    }

    interface Discuss {
        Optional<String> channels();

        /**
         * Number of candidate agents the vector resume pre-filter returns to NARROW the
         * catalog the coordinator reasons over (RAG recall; the reasoning call then
         * prunes for precision). Generous so a multi-domain question is not starved -
         * the coordinator can only select WITHIN this set. Also caps the fallback path,
         * where the similarity result is applied directly.
         */
        @WithDefault("10")
        int resumePreFilterTopK();

        @WithDefault("10")
        int maxInferencesPerMinute();

        @WithDefault("3")
        int maxContributionsPerThread();

        @WithDefault("true")
        boolean tooler();

        @WithDefault("medium")
        String priority();

        // Default is "generic" (NOT "tooler"): the raw-tool-output contract in
        // ChatService.callWithToolLoop fires only for role=="tooler", so emitting
        // raw tool results instead of a reasoned answer must be OPT-IN via an
        // explicit discussRole: tooler. A roleless agent that calls a tool (the
        // fitness judge calling collect_scenario, then reasoning to its verdict)
        // reasons to its own text - defaulting to "tooler" made the judge dump the
        // raw scenario instead of a verdict ("no verdict after retries" -> all 0).
        // triggerTypesFor() only special-cases "analyst", so a "generic" role still
        // wakes on advisory_ready and evaluates; agree-counting excludes only
        // researchers, so a "generic" agree still counts as substantive.
        // NOTE: the default must be NON-EMPTY - SmallRye converts an empty-String
        // default to null and fails startup with SRCFG00040 on a non-Optional String.
        @WithDefault("generic")
        String role();

        @WithDefault("false")
        boolean coordinator();

        /**
         * Whether the coordinator may answer a turn DIRECTLY (selecting no toolers)
         * when its reasoning judges the question coordinator-answerable. On for
         * general crews (pilot) where a coordinator-answerable question shouldn't wake
         * the whole crew. MUST be off (KUBEMOOT_DISCUSS_ANSWER_DIRECTLY=false) for a
         * single-specialist crew like the fitness judge, whose coordinator must ALWAYS
         * delegate to its specialist: otherwise it answers a coordinator-answerable-
         * looking EMBEDDED question (a concept/gotcha scoring doc) instead of producing
         * the structured verdict, yielding errJudgeNoVerdict.
         */
        @WithDefault("true")
        boolean answerDirectly();

        /**
         * Compute-role contract (KUBEMOOT_DISCUSS_COMPUTE_CONTRACT). When true, the
         * tool loop REQUIRES this agent to produce its result by running a tool
         * (execute_code): a no-tool-call answer is re-prompted and, if it still
         * never runs code, fails rather than emitting an in-head (preview-counted)
         * number. Set ONLY on the compute agent; default off for everyone else.
         */
        @WithDefault("false")
        boolean computeContract();

        /**
         * Metrics drill contract (KUBEMOOT_DISCUSS_METRICS_DRILL_CONTRACT). When true,
         * a tooler whose metric query (execute_query / execute_range_query) came back
         * with no series - the usual symptom of a WRONG metric NAME, not a real gap -
         * is re-prompted to run a discovery pass (list_metrics / get_metric_metadata)
         * before it may conclude the metric is unavailable. Fires ONLY when a discovery
         * tool is in this agent's tool set (so a query-only specialist that cannot drill
         * is exempt) and the agent has not already drilled; on budget exhaustion the
         * honest empty result is let through, never thrown. Default ON: it is a scoped
         * correctness guard that no-ops for any non-metrics turn.
         */
        @WithDefault("true")
        boolean metricsDrillContract();

        /**
         * Cross-cutting analyst agents (comma-separated names) that must ALWAYS be a
         * candidate for reasoning-select, regardless of the domain RAG pre-filter rank.
         * A domain-agnostic capability (compute: counting, sorting, math) loses a domain
         * similarity contest and is filtered out before selection; unioning it into the
         * candidate set lets the reasoning LLM decide whether the question needs it. Set
         * on the coordinator (KUBEMOOT_DISCUSS_ALWAYS_CANDIDATE_AGENTS). Optional per the
         * empty-string-becomes-null gotcha (SRCFG00040).
         */
        Optional<String> alwaysCandidateAgents();

        /**
         * Whether the crew declares any analyst-role agent. Set on the
         * coordinator (KUBEMOOT_DISCUSS_HAS_ANALYSTS) so it runs the REVIEW
         * phase (where analysts self-select) instead of taking the single-agree
         * fast path that would skip it.
         */
        @WithDefault("false")
        boolean hasAnalysts();

        /**
         * Whether the crew declares the review decision
         * (KUBEMOOT_DISCUSS_REVIEW_DECISION). When true, the coordinator makes one
         * model call after EVALUATING that chooses how the gathered results are
         * checked: concur (one analyst is asked whether it concurs), full (the review
         * with the selected analysts), or none. The policy for that choice lives in
         * the crew's coordinator PromptModule; the runtime escalates to a full review
         * whenever a tooler failed or a concern or objection is on the board. Off:
         * the full review runs, as for any crew that does not declare it.
         */
        @WithDefault("false")
        boolean reviewDecision();

        /**
         * The model tier of the review decision call
         * (KUBEMOOT_DISCUSS_REVIEW_DECISION_TIER): "fast" uses the triage model,
         * "reasoning" the coordinator's main model. Any other value is the fast tier.
         */
        @WithDefault("fast")
        String reviewDecisionTier();

        /**
         * Max re-prompts the synthesis makes when its draft under-enumerates an
         * inventory answer (lists fewer entities than the gathered data contains).
         * The completeness contract names the omitted entries and asks for the full
         * list, up to this many times, then accepts the best draft. Default 0 (OFF) so
         * it never misfires on a coordinator whose answers are not entity inventories
         * (e.g. the fitness judge's structured verdicts); opt IN per crew by setting
         * KUBEMOOT_DISCUSS_SYNTHESIS_COMPLETENESS_RETRIES on the coordinator.
         */
        @WithDefault("0")
        int synthesisCompletenessRetries();

        // Phase budgets. They only sum into the discussion's hard ceiling (a safety
        // net); signals, not these values, move a discussion between phases.
        @WithDefault(PhaseBudgetDefaults.ADVISORY_SECONDS)
        int advisoryTimeoutSeconds();

        @WithDefault(PhaseBudgetDefaults.EVALUATION_SECONDS)
        int evaluationTimeoutSeconds();

        @WithDefault(PhaseBudgetDefaults.REVIEW_SECONDS)
        int reviewTimeoutSeconds();

        @WithDefault(PhaseBudgetDefaults.SYNTHESIS_SECONDS)
        int synthesisTimeoutSeconds();

        @WithDefault("20")
        int minEvalSeconds();

        @WithDefault("5")
        int settleSeconds();

        @WithDefault("10")
        int minReviewSeconds();

        Optional<String> jetstreamConsumer();

        Optional<String> triagePrompt();

        /** Seconds added on top of P90 before synthesizing a stand_aside for a stalled agent. */
        @WithDefault("10")
        int evalGraceSeconds();

        /** Maximum conversation turns kept for multi-turn follow-ups. */
        @WithDefault("10")
        int conversationMaxTurns();

        /** TTL in minutes for conversation history. */
        @WithDefault("1440")
        int conversationTtlMinutes();

        /** NATS KV bucket for durable conversation history. */
        @WithDefault("kubemoot_conversations")
        String conversationKvBucket();
    }

    interface Onboarding {
        @WithDefault("false")
        boolean mode();

        @WithDefault("10")
        int gapDetectionTimeoutSeconds();
    }

    interface Rtfm {
        @WithDefault("false")
        boolean mode();
    }

    interface Heartbeat {
        @WithDefault("true")
        boolean enabled();

        @WithDefault("60")
        int intervalSeconds();

        @WithDefault("kubemoot_agent_state")
        String kvBucket();
    }

    /**
     * Crew working-memory GC/injection policy, declared in Crew.spec.memory and
     * propagated by the operator as KUBEMOOT_MEMORY_* env. Defaults mirror the
     * CRD defaults so the agent is sane even if the operator omits them.
     */
    interface Memory {
        @WithDefault("true")
        boolean enabled();

        @WithDefault("5000")
        int maxFacts();

        @WithDefault("365")
        int ttlDays();

        @WithDefault("8")
        int injectLimit();

        @WithDefault("true")
        boolean verifyOnAdd();
    }

    interface Gateway {
        @WithDefault("false")
        boolean enabled();

        Optional<String> endpoint();

        @WithDefault("30s")
        String healthCheckInterval();

        @WithDefault("5s")
        String healthCheckTimeout();

        @WithDefault("true")
        boolean requiredForReadiness();
    }

    interface Model {
        @WithDefault("default")
        String name();

        @WithDefault("ollama")
        String provider();

        // The operator always injects the real model via KUBEMOOT_MODEL_MODEL
        // (agent_controller.go), so this default is never used in a deployed
        // agent. The sentinel is deliberately NOT a real model name: baking one
        // couples the runtime to a specific model the cluster may not run, and a
        // stale value silently masks a missing config. "unset" resolves for local
        // dev yet fails loudly at inference if a deployment ever forgets to set it.
        @WithDefault("unset")
        String model();

        @WithDefault("http://localhost:11434")
        String endpoint();

        @WithDefault("0.3")
        double temperature();

        @WithDefault("4096")
        int maxTokens();

        @WithDefault("10")
        int maxToolIterations();

        /**
         * Whether the model should "think" (emit a reasoning chain) before
         * answering. Maps to ollama's request `think` flag. Empty leaves the
         * model/family default (qwen3 thinks by default). Set false for
         * deterministic tool-calling agents whose latency is dominated by an
         * unneeded reasoning chain; true for reasoning agents (analysts,
         * coordinator). Declared per-agent via KUBEMOOT_MODEL_THINK; the
         * runtime stays generic and only honors the value.
         */
        Optional<Boolean> think();
    }

    /**
     * Triage model configuration — lightweight model for quick "should I contribute?"
     * assessment during discussions. Defaults to same endpoint/model as primary,
     * so without operator overrides the behavior is unchanged.
     */
    interface TriageModel {
        Optional<String> modelId();

        Optional<String> endpoint();

        @WithDefault("0.3")
        double temperature();

        @WithDefault("2048")
        int maxTokens();

        /** HTTP timeout for triage inference calls. High default allows agents to queue on
         *  a contended GPU rather than timing out. The GPU is a shared resource — agents
         *  should wait their turn, not fail because others are ahead in the queue. */
        @WithDefault("120")
        int timeoutSeconds();
    }

    interface RagSource {
        String name();
        String endpoint();

        @WithDefault("5")
        int topK();
    }

    interface McpServer {
        String name();
        String endpoint();

        Optional<List<String>> enabledTools();
        Optional<List<String>> disabledTools();
    }
}
