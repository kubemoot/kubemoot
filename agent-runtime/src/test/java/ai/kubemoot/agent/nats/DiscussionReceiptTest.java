package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.time.Instant;

import static org.junit.jupiter.api.Assertions.*;

/** The receipt a coordinator publishes on thread_close: phases, consensus, tokens, agents, placements. */
class DiscussionReceiptTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Instant T0 = Instant.parse("2026-10-16T04:00:00Z");

    private static Instant at(long ms) {
        return T0.plusMillis(ms);
    }

    private static JsonNode signal(String metadataJson) throws Exception {
        return MAPPER.readTree("{\"metadata\":" + metadataJson + "}");
    }

    /** A full thread: advisory, triage, a wait, two agents, a stand-aside, then synthesis. */
    static DiscussionReceipt fullThread() throws Exception {
        var r = new DiscussionReceipt(T0);
        r.observe("coordinator", "thread_start", null, at(0));
        r.observe("coordinator", "advisory", null, at(9_000));
        r.observe("coordinator", "triage_result", null, at(15_000));
        r.observe("k8s", "triaging", null, at(16_000));
        r.observe("k8s", "stand_aside", signal("{\"modelsUsed\":[\"qwen3:8b@ollama-cpu\"],\"inputTokens\":363,"
                + "\"outputTokens\":148,\"inferenceMs\":28988,\"reason\":\"outside my domain\"}"), at(57_000));
        r.observe("pve", "waiting", null, at(20_000));
        r.observe("pve", "evaluating", null, at(32_000));
        r.observe("pve", "contribution", signal("{\"modelsUsed\":[\"qwen3:8b@ollama-cpu\",\"qwen3:14b@ollama-rig1\"],"
                + "\"inputTokens\":21480,\"outputTokens\":2911,\"inferenceMs\":101200}"), at(186_000));
        return r;
    }

    @Test
    void timesAndPhases_comeFromTheMessagesThatEndEachPhase() throws Exception {
        JsonNode r = fullThread().build(MAPPER, at(217_000), 2, 11_170, 2_108);

        assertEquals(T0.toEpochMilli(), r.path("startedAt").asLong());
        assertEquals(217_000, r.path("totalMs").asLong());
        assertEquals(57_000, r.path("firstSignalMs").asLong());
        JsonNode p = r.path("phasesMs");
        assertEquals(9_000, p.path("advisory").asLong());
        assertEquals(6_000, p.path("selection").asLong());
        assertEquals(171_000, p.path("evaluation").asLong());
        assertEquals(31_000, p.path("synthesis").asLong());
    }

    @Test
    void consensus_countsEachAgentsLastSignal_withContributionAsAgree() throws Exception {
        JsonNode c = fullThread().build(MAPPER, at(217_000), 2, 0, 0).path("consensus");
        assertEquals(2, c.path("convened").asInt());
        assertEquals(1, c.path("agree").asInt());
        assertEquals(1, c.path("standAside").asInt());
        assertEquals(0, c.path("failure").asInt());
    }

    @Test
    void tokens_addTheAgentsAndTheCoordinator() throws Exception {
        JsonNode t = fullThread().build(MAPPER, at(217_000), 2, 11_170, 2_108).path("tokens");
        assertEquals(21_843, t.path("agentsIn").asLong());
        assertEquals(3_059, t.path("agentsOut").asLong());
        assertEquals(21_843 + 11_170, t.path("in").asLong());
        assertEquals(3_059 + 2_108, t.path("out").asLong());
    }

    @Test
    void agentLines_areInSignalOrder_withModelsWaitAndReason() throws Exception {
        JsonNode agents = fullThread().build(MAPPER, at(217_000), 2, 0, 0).path("agents");
        assertEquals(2, agents.size());
        JsonNode k8s = agents.get(0);
        assertEquals("k8s", k8s.path("agent").asText());
        assertEquals("stand_aside", k8s.path("signal").asText());
        assertEquals("outside my domain", k8s.path("reason").asText());
        assertEquals(0, k8s.path("waitMs").asLong());
        JsonNode pve = agents.get(1);
        assertEquals("agree", pve.path("signal").asText());
        assertEquals(12_000, pve.path("waitMs").asLong(), "waiting at 20s, next message at 32s");
        assertEquals(2, pve.path("models").size());
        assertEquals(101_200, pve.path("inferenceMs").asLong());
        assertFalse(pve.has("reason"));
    }

    @Test
    void placements_countAgentsPerModelAndProvider() throws Exception {
        JsonNode places = fullThread().build(MAPPER, at(217_000), 2, 0, 0).path("placements");
        assertEquals(2, places.size());
        assertEquals("qwen3:14b@ollama-rig1", places.get(0).path("place").asText());
        assertEquals(1, places.get(0).path("agents").asInt());
        assertEquals("qwen3:8b@ollama-cpu", places.get(1).path("place").asText());
        assertEquals(2, places.get(1).path("agents").asInt());
    }

    @Test
    void anAgentsSignalsInOneRound_addUp_andTheLastSetsItsSignal() throws Exception {
        var r = new DiscussionReceipt(T0);
        r.observe("a", "concern", signal("{\"inputTokens\":10,\"modelsUsed\":[\"m@p\"]}"), at(1_000));
        r.observe("a", "agree", signal("{\"inputTokens\":30,\"modelsUsed\":[\"m@p\",\"n@q\"]}"), at(2_000));
        JsonNode out = r.build(MAPPER, at(3_000), 0, 0, 0);
        JsonNode a = out.path("agents").get(0);
        assertEquals(1, out.path("agents").size());
        assertEquals("agree", a.path("signal").asText());
        assertEquals(40, a.path("inputTokens").asLong());
        assertEquals(2, a.path("models").size(), "each place once");
        assertEquals(0, out.path("consensus").path("concern").asInt());
        assertEquals(1, out.path("consensus").path("convened").asInt(), "the agents that signalled");
    }

    @Test
    void aWait_endsOnlyWhenTheAgentStartsEvaluatingOrSignals() {
        var r = new DiscussionReceipt(T0);
        r.observe("a", "waiting", null, at(1_000));
        r.observe("a", "heartbeat", null, at(31_000));
        r.observe("a", "waiting", null, at(50_000));
        r.observe("a", "evaluating", null, at(91_000));
        r.observe("b", "waiting", null, at(2_000));
        r.observe("b", "failure", null, at(12_000));
        r.observe("a", "agree", null, at(120_000));
        JsonNode agents = r.build(MAPPER, at(121_000), 0, 0, 0).path("agents");
        assertEquals(10_000, agents.get(0).path("waitMs").asLong(), "b waited until it failed");
        assertEquals(90_000, agents.get(1).path("waitMs").asLong(),
                "a heartbeat or a repeated waiting neither ends nor restarts the wait");
    }

    @Test
    void signalsBeforeTheTriageResult_countInEvaluation() {
        var r = new DiscussionReceipt(T0);
        r.observe("c", "advisory", null, at(5_000));
        r.observe("a", "agree", null, at(8_000));
        r.observe("c", "triage_result", null, at(10_000));
        r.observe("b", "agree", null, at(30_000));
        JsonNode p = r.build(MAPPER, at(40_000), 2, 0, 0).path("phasesMs");
        assertEquals(5_000, p.path("advisory").asLong());
        assertEquals(5_000, p.path("selection").asLong());
        assertEquals(22_000, p.path("evaluation").asLong(), "from the first signal at 8s to the last at 30s");
        assertEquals(10_000, p.path("synthesis").asLong());
    }

    @Test
    void allSignalsBeforeTheTriageResult_leaveEvaluationShort_andSynthesisFromTheSelection() {
        var r = new DiscussionReceipt(T0);
        r.observe("a", "agree", null, at(3_000));
        r.observe("c", "triage_result", null, at(10_000));
        JsonNode p = r.build(MAPPER, at(12_000), 0, 0, 0).path("phasesMs");
        assertFalse(p.has("advisory"));
        assertEquals(10_000, p.path("selection").asLong());
        assertEquals(0, p.path("evaluation").asLong());
        assertEquals(2_000, p.path("synthesis").asLong());
    }

    @Test
    void aFailure_givesItsLastErrorAsTheReason_capped() throws Exception {
        var r = new DiscussionReceipt(T0);
        String longError = "x".repeat(DiscussionReceipt.REASON_MAX_CHARS + 50);
        r.observe("a", "failure", signal("{\"lastError\":\"" + longError + "\"}"), at(1_000));
        r.observe("b", "stand_aside", signal("{\"reason\":\"gpu-busy\",\"lastError\":\"ignored\"}"), at(2_000));
        r.observe("c", "agree", signal("{\"lastError\":\"not a reason for agreeing\"}"), at(3_000));
        JsonNode agents = r.build(MAPPER, at(4_000), 0, 0, 0).path("agents");
        assertEquals(DiscussionReceipt.REASON_MAX_CHARS, agents.get(0).path("reason").asText().length());
        assertEquals("gpu-busy", agents.get(1).path("reason").asText());
        assertFalse(agents.get(2).has("reason"));
    }

    @Test
    void aNewRound_startsAFreshReceipt() throws Exception {
        var r = fullThread();
        r.startRound(at(300_000));
        r.observe("pve", "agree", signal("{\"inputTokens\":5}"), at(320_000));
        JsonNode out = r.build(MAPPER, at(330_000), 0, 0, 0);
        assertEquals(30_000, out.path("totalMs").asLong());
        assertEquals(20_000, out.path("firstSignalMs").asLong());
        assertEquals(1, out.path("agents").size());
        assertEquals(5, out.path("tokens").path("agentsIn").asLong());
        assertFalse(out.path("phasesMs").has("selection"));
    }

    @Test
    void convened_isTheLargerOfTheSelectionAndTheAgentsThatSignalled() throws Exception {
        assertEquals(5, fullThread().build(MAPPER, at(217_000), 5, 0, 0).path("consensus").path("convened").asInt());
        assertEquals(2, fullThread().build(MAPPER, at(217_000), 1, 0, 0).path("consensus").path("convened").asInt());
    }

    @Test
    void isTerminal_namesTheCountedSignals() {
        assertTrue(DiscussionReceipt.isTerminal("contribution"));
        assertTrue(DiscussionReceipt.isTerminal("failure"));
        assertFalse(DiscussionReceipt.isTerminal("synthesis"));
        assertFalse(DiscussionReceipt.isTerminal(null));
    }

    @Test
    void aThreadWithNoSignals_hasNoFirstSignal_andAllTimeInSynthesis() {
        var r = new DiscussionReceipt(T0);
        r.observe("coordinator", "thread_start", null, at(0));
        JsonNode out = r.build(MAPPER, at(5_000), 0, 100, 20);
        assertFalse(out.has("firstSignalMs"));
        assertEquals(5_000, out.path("phasesMs").path("synthesis").asLong());
        assertFalse(out.path("phasesMs").has("evaluation"));
        assertEquals(0, out.path("agents").size());
        assertEquals(100, out.path("tokens").path("in").asLong());
    }

    @Test
    void signalsWithoutMetadata_andUnknownOrIncompleteMessages_areSafe() {
        var r = new DiscussionReceipt(T0);
        r.observe(null, "agree", null, at(1));
        r.observe("a", null, null, at(1));
        r.observe("a", "agree", null, null);
        r.observe("a", "heartbeat", null, at(500));
        r.observe("a", "failure", null, at(1_000));
        JsonNode out = r.build(MAPPER, at(2_000), 0, 0, 0);
        JsonNode a = out.path("agents").get(0);
        assertEquals("failure", a.path("signal").asText());
        assertEquals(0, a.path("models").size());
        assertEquals(0, a.path("inputTokens").asLong());
        assertEquals(0, out.path("placements").size());
    }
}
