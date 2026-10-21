package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import org.junit.jupiter.api.Test;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

/** The scheduler's performance history: what it records, how it bounds it, and how it reads it back. */
class PerformanceHistoryTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String SERVER = "gpu-a";
    private static final String MODEL = "qwen3:14b";

    private static PerformanceHistory history(NatsConnectionProvider nats, String agent) {
        return history(nats, agent, SchedulerMetrics.none());
    }

    private static PerformanceHistory history(NatsConnectionProvider nats, String agent, SchedulerMetrics metrics) {
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn(agent);
        when(props.namespace()).thenReturn(Optional.of("crew-ns"));
        return new PerformanceHistory(nats, MAPPER, props, Runnable::run, metrics);
    }

    @Test
    void recordedCallsAndWaits_reachTheMetrics() {
        var registry = new io.micrometer.core.instrument.simple.SimpleMeterRegistry();
        var h = history(new InMemoryKv().server(PerformanceHistory.BUCKET), "a", new SchedulerMetrics(registry, "learned"));

        h.record(call(true, false, 100));
        h.record(new CallObservation("", "", CallObservation.KIND_SIMPLE, true, false, 0, 0, 1, -1, -1, -1, 0, 0L));
        h.recordWait(SERVER, MODEL, 2_000);
        h.recordWait(SERVER, MODEL, -1);
        h.recordUnplacedWait(MODEL, 9_000);

        assertEquals(1.0, registry.find(SchedulerMetrics.CALLS).counter().count(), "an unattributable call is not counted");
        assertEquals(1, registry.find(SchedulerMetrics.WAIT_SECONDS).tag("outcome", "placed").timer().count());
        assertEquals(1, registry.find(SchedulerMetrics.WAIT_SECONDS).tag("outcome", "not_placed").timer().count());
    }

    private static CallObservation call(boolean ok, boolean cold, long ms) {
        return new CallObservation(SERVER, MODEL, CallObservation.KIND_TOOL_TURN, ok, cold, 1200, 300, ms,
                CallObservation.UNKNOWN, CallObservation.UNKNOWN, CallObservation.UNKNOWN, 2, 1_000L);
    }

    private static JsonNode stored(InMemoryKv bucket, String agent) throws Exception {
        byte[] v = bucket.data.get(PerformanceHistory.key(SERVER, MODEL, "crew-ns/" + agent));
        assertNotNull(v, "no entry written");
        return MAPPER.readTree(v);
    }

    @Test
    void record_writesTheRequestUnderThisAgentsKey() throws Exception {
        var bucket = new InMemoryKv();
        history(bucket.server(PerformanceHistory.BUCKET), "a").record(call(true, true, 4_000));

        JsonNode e = stored(bucket, "a");
        assertEquals(SERVER, e.path("server").asText());
        assertEquals(MODEL, e.path("model").asText());
        assertEquals(1, e.path("requests").asLong());
        assertEquals(0, e.path("failures").asLong());
        JsonNode s = e.path(PerformanceHistory.F_SAMPLES).get(0);
        assertEquals(4_000, s.path("durationMs").asLong());
        assertEquals(1200, s.path("promptTokens").asLong());
        assertTrue(s.path("coldStart").asBoolean());
        assertEquals(2, s.path("activeCalls").asInt());
        assertEquals(CallObservation.UNKNOWN, s.path("loadMs").asLong());
    }

    @Test
    void record_keepsOnlyTheLatestSamples_butCountsEveryRequestAndFailure() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        for (int i = 0; i < PerformanceHistory.MAX_SAMPLES + 5; i++) {
            h.record(call(i % 2 == 0, false, i));
        }

        JsonNode e = stored(bucket, "a");
        assertEquals(PerformanceHistory.MAX_SAMPLES, e.path(PerformanceHistory.F_SAMPLES).size());
        assertEquals(5, e.path(PerformanceHistory.F_SAMPLES).get(0).path("durationMs").asLong(),
                "the oldest samples are dropped first");
        assertEquals(PerformanceHistory.MAX_SAMPLES + 5, e.path("requests").asLong());
        assertEquals((PerformanceHistory.MAX_SAMPLES + 5) / 2, e.path("failures").asLong());
    }

    @Test
    void recordWait_keepsTheLatestWaits() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        for (int i = 0; i < PerformanceHistory.MAX_WAITS + 3; i++) {
            h.recordWait(SERVER, MODEL, 1_000L + i);
        }

        JsonNode waits = stored(bucket, "a").path(PerformanceHistory.F_WAITS);
        assertEquals(PerformanceHistory.MAX_WAITS, waits.size());
        assertEquals(1_003L, waits.get(0).path("ms").asLong());
        assertEquals(0, stored(bucket, "a").path("requests").asLong(), "a wait is not a request");
    }

    @Test
    void unattributableObservationsAndNegativeWaits_areIgnored() {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        h.record(null);
        h.record(new CallObservation("", MODEL, CallObservation.KIND_SIMPLE, true, false, 0, 0, 1,
                -1, -1, -1, 0, 0));
        h.recordWait(SERVER, null, 10);
        h.recordWait(SERVER, MODEL, -1);
        assertTrue(bucket.data.isEmpty());
    }

    @Test
    void entriesFor_readsEveryAgentsEntry_forThatServerAndModelOnly() {
        var bucket = new InMemoryKv();
        var nats = bucket.server(PerformanceHistory.BUCKET);
        history(nats, "a").record(call(true, false, 100));
        history(nats, "b").record(call(true, false, 200));
        history(nats, "b").record(new CallObservation(SERVER, "qwen3:8b", CallObservation.KIND_TRIAGE, true,
                false, 10, 10, 50, -1, -1, -1, 0, 0));

        assertEquals(2, history(nats, "c").entriesFor(SERVER, MODEL).size());
        assertEquals(1, history(nats, "c").entriesFor(SERVER, "qwen3:8b").size());
        assertTrue(history(nats, "c").entriesFor("gpu-b", MODEL).isEmpty());
    }

    @Test
    void afterARestart_theAgentContinuesItsStoredEntry() throws Exception {
        var bucket = new InMemoryKv();
        var nats = bucket.server(PerformanceHistory.BUCKET);
        history(nats, "a").record(call(true, false, 100));
        history(nats, "a").record(call(false, false, 200));

        JsonNode e = stored(bucket, "a");
        assertEquals(2, e.path("requests").asLong());
        assertEquals(1, e.path("failures").asLong());
        assertEquals(2, e.path(PerformanceHistory.F_SAMPLES).size());
    }

    @Test
    void withoutNats_recordingAndReadingDoNothing() {
        var nats = mock(NatsConnectionProvider.class);
        var h = history(nats, "a");
        assertDoesNotThrow(() -> h.record(call(true, false, 1)));
        assertTrue(h.entriesFor(SERVER, MODEL).isEmpty());
    }

    // ---- CallObservation ----

    @Test
    void aFailedRead_skipsTheWrite_andNeverReplacesTheStoredHistory() throws Exception {
        var bucket = new InMemoryKv();
        var nats = bucket.server(PerformanceHistory.BUCKET);
        history(nats, "a").record(call(true, false, 100));
        String key = PerformanceHistory.key(SERVER, MODEL, "crew-ns/a");
        when(bucket.kv.get(key)).thenThrow(new java.io.IOException("timeout"));

        var restarted = history(nats, "a");
        restarted.record(call(true, false, 200));

        assertEquals(1, stored(bucket, "a").path("requests").asLong(), "the stored history is kept");
        assertEquals(1, restarted.droppedWrites());
    }

    @Test
    void aCorruptStoredEntry_isRebuilt() throws Exception {
        var bucket = new InMemoryKv();
        String key = PerformanceHistory.key(SERVER, MODEL, "crew-ns/a");
        bucket.data.put(key, "{\"samples\": 3, \"waits\": \"x\"}".getBytes());

        history(bucket.server(PerformanceHistory.BUCKET), "a").record(call(true, false, 100));

        JsonNode e = stored(bucket, "a");
        assertEquals(1, e.path(PerformanceHistory.F_SAMPLES).size());
        assertTrue(e.path(PerformanceHistory.F_WAITS).isArray());
    }

    @Test
    void aFailedWrite_isCounted_andDoesNotThrow() throws Exception {
        var bucket = new InMemoryKv();
        when(bucket.kv.put(any(String.class), any(byte[].class))).thenThrow(new java.io.IOException("full"));
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");

        assertDoesNotThrow(() -> h.record(call(true, false, 100)));
        assertEquals(1, h.droppedWrites());
    }

    @Test
    void theBackgroundWriter_writesOffTheCallersThread() throws Exception {
        var bucket = new InMemoryKv();
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn("a");
        when(props.namespace()).thenReturn(Optional.of("crew-ns"));
        var h = new PerformanceHistory(bucket.server(PerformanceHistory.BUCKET), MAPPER, props, SchedulerMetrics.none());

        h.record(call(true, false, 100));

        long deadline = System.currentTimeMillis() + 5_000;
        while (bucket.data.isEmpty() && System.currentTimeMillis() < deadline) {
            Thread.onSpinWait();
        }
        assertEquals(1, stored(bucket, "a").path("requests").asLong());
    }

    // ---- ObservedChatModel ----

    private static ChatRequest request() {
        return ChatRequest.builder().messages(UserMessage.from("hi")).build();
    }

    private static CallContext context(String kind, boolean cold) {
        return new CallContext(SERVER, MODEL, kind, cold, 3);
    }

    @Test
    void observedModel_recordsEachRequest_coldOnlyOnTheFirst() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        var delegate = mock(ChatModel.class);
        var response = ChatResponse.builder().aiMessage(AiMessage.from("ok"))
                .tokenUsage(new TokenUsage(800, 50)).build();
        when(delegate.chat(any(ChatRequest.class))).thenReturn(response);
        var model = new ObservedChatModel(delegate, h, context(CallObservation.KIND_TOOL_TURN, true));

        assertSame(response, model.chat(request()));
        model.chat(request());

        JsonNode samples = stored(bucket, "a").path(PerformanceHistory.F_SAMPLES);
        assertEquals(2, samples.size());
        assertTrue(samples.get(0).path("coldStart").asBoolean());
        assertFalse(samples.get(1).path("coldStart").asBoolean());
        assertEquals(800, samples.get(0).path("promptTokens").asLong());
        assertEquals(50, samples.get(0).path("outputTokens").asLong());
        assertEquals(3, samples.get(0).path("activeCalls").asInt());
        assertEquals(CallObservation.KIND_TOOL_TURN, samples.get(0).path("kind").asText());
    }

    @Test
    void observedModel_aFailedColdRequest_leavesTheNextOneCold_andRethrows() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        var delegate = mock(ChatModel.class);
        var response = ChatResponse.builder().aiMessage(AiMessage.from("ok")).build();
        when(delegate.chat(any(ChatRequest.class))).thenThrow(new IllegalStateException("timeout"))
                .thenReturn(response);
        var model = new ObservedChatModel(delegate, h, context(CallObservation.KIND_SIMPLE, true));

        assertThrows(IllegalStateException.class, () -> model.chat(request()));
        model.chat(request());

        JsonNode e = stored(bucket, "a");
        assertEquals(1, e.path("failures").asLong());
        JsonNode samples = e.path(PerformanceHistory.F_SAMPLES);
        assertFalse(samples.get(0).path("ok").asBoolean());
        assertTrue(samples.get(1).path("coldStart").asBoolean(), "the model was still loading");
        assertEquals(0, samples.get(1).path("promptTokens").asLong(), "no token usage reported");
    }

    @Test
    void observedModel_theOptionsOverloadIsRecordedToo() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        var delegate = mock(ChatModel.class);
        var response = ChatResponse.builder().aiMessage(AiMessage.from("ok")).build();
        when(delegate.chat(any(ChatRequest.class), any())).thenReturn(response);
        var model = new ObservedChatModel(delegate, h, context(CallObservation.KIND_SIMPLE, false));

        assertSame(response, model.chat(request(), mock(dev.langchain4j.model.chat.ChatRequestOptions.class)));
        assertEquals(1, stored(bucket, "a").path("requests").asLong());
    }

    @Test
    void observedModel_aRecordingFailure_neverReachesTheCaller() {
        var h = mock(PerformanceHistory.class);
        doThrow(new IllegalStateException("broken")).when(h).record(any(), any());
        var delegate = mock(ChatModel.class);
        var response = ChatResponse.builder().aiMessage(AiMessage.from("ok")).build();
        when(delegate.chat(any(ChatRequest.class))).thenReturn(response);
        var model = new ObservedChatModel(delegate, h, context(CallObservation.KIND_SIMPLE, false));

        assertSame(response, model.chat(request()));
    }

    @Test
    void callContext_measuredCarriesTheContext_andColdCanBeReplaced() {
        var ctx = context(CallObservation.KIND_TRIAGE, true);
        assertSame(ctx, ctx.withColdStart(true));
        CallObservation o = ctx.withColdStart(false).measured(true, 5L, 70L, 10L, 2L);
        assertFalse(o.coldStart());
        assertEquals(SERVER, o.server());
        assertEquals(3, o.activeCalls());
        assertEquals(70L, o.durationMs());
        assertEquals(CallObservation.UNKNOWN, o.evalMs());
    }

    // ---- prediction error ----

    private static PlacedCall placed(LearnedPlacement.Prediction predicted) {
        return new PlacedCall(SERVER, MODEL, new PlacementDecision(PlacementDecision.LEARNED, "a long reason",
                java.util.List.of(new PlacementDecision.Alternative("other", MODEL, true, true, null)), predicted));
    }

    private static final LearnedPlacement.Prediction SIX_AND_A_HALF = new LearnedPlacement.Prediction(0.5, 2.0, 1.0, 3.0);

    @Test
    void aScoredCall_keepsItsPredictionBesideWhatItMeasured_andReachesTheMetrics() throws Exception {
        var registry = new io.micrometer.core.instrument.simple.SimpleMeterRegistry();
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a", new SchedulerMetrics(registry, "learned"));

        h.record(call(true, true, 8_000), placed(SIX_AND_A_HALF));

        JsonNode e = stored(bucket, "a");
        assertEquals(1, e.path(PerformanceHistory.F_SAMPLES).size());
        JsonNode p = e.path(PerformanceHistory.F_PREDICTIONS).get(0);
        assertEquals("learned", p.path("policy").asText());
        assertEquals(6_500, p.path("predicted").path("totalMs").asLong());
        assertEquals(8_000, p.path("actual").path("totalMs").asLong());
        assertEquals(1_500, p.path("error").path("totalAbsMs").asLong());
        assertFalse(p.has("reason"), "the stored form is compact");
        assertFalse(p.has("alternatives"));
        assertEquals(1.5, registry.find(SchedulerMetrics.PREDICTION_ERROR_SECONDS).tag("policy", "learned")
                .tag("part", "total").summary().totalAmount(), 1e-9);
    }

    @Test
    void predictions_areBounded_oldestFirst() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        for (int i = 0; i < PerformanceHistory.MAX_PREDICTIONS + 3; i++) {
            h.record(call(true, false, 1_000 + i), placed(SIX_AND_A_HALF));
        }

        JsonNode list = stored(bucket, "a").path(PerformanceHistory.F_PREDICTIONS);
        assertEquals(PerformanceHistory.MAX_PREDICTIONS, list.size());
        assertEquals(1_003, list.get(0).path("actual").path("totalMs").asLong());
    }

    @Test
    void aCallWithoutAPredictionOrAFailedFirstRequest_addsNoPrediction() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");

        h.record(call(true, false, 100), null);
        h.record(call(true, false, 100), placed(null));
        h.record(call(false, false, 100), placed(SIX_AND_A_HALF));

        JsonNode e = stored(bucket, "a");
        assertEquals(3, e.path(PerformanceHistory.F_SAMPLES).size());
        assertFalse(e.has(PerformanceHistory.F_PREDICTIONS));
    }

    @Test
    void anEntryStoredBeforePredictionsExisted_isExtended() throws Exception {
        var bucket = new InMemoryKv();
        bucket.data.put(PerformanceHistory.key(SERVER, MODEL, "crew-ns/a"), ("{\"server\":\"" + SERVER
                + "\",\"model\":\"" + MODEL + "\",\"agent\":\"crew-ns/a\",\"samples\":[],\"waits\":[],"
                + "\"requests\":4,\"failures\":1}").getBytes(java.nio.charset.StandardCharsets.UTF_8));

        history(bucket.server(PerformanceHistory.BUCKET), "a").record(call(true, false, 900), placed(SIX_AND_A_HALF));

        JsonNode e = stored(bucket, "a");
        assertEquals(5, e.path("requests").asLong());
        assertEquals(1, e.path(PerformanceHistory.F_PREDICTIONS).size());
    }

    @Test
    void observedModel_scoresTheFirstRequestOfThePlacementOnly() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.server(PerformanceHistory.BUCKET), "a");
        var delegate = mock(ChatModel.class);
        var response = ChatResponse.builder().aiMessage(AiMessage.from("ok"))
                .tokenUsage(new TokenUsage(800, 50)).build();
        when(delegate.chat(any(ChatRequest.class))).thenReturn(response);
        var call = placed(SIX_AND_A_HALF);
        var ctx = new CallContext(SERVER, MODEL, CallObservation.KIND_TOOL_TURN, true, 3, call);
        var model = new ObservedChatModel(delegate, h, ctx);

        model.chat(request());
        model.chat(request());
        model.chat(request());

        JsonNode e = stored(bucket, "a");
        assertEquals(3, e.path(PerformanceHistory.F_SAMPLES).size());
        assertEquals(1, e.path(PerformanceHistory.F_PREDICTIONS).size());
        @SuppressWarnings("unchecked")
        var actual = (java.util.Map<String, Object>) call.snapshot(false).get("actual");
        assertEquals(3, actual.get("requests"));
    }

    @Test
    void aStreamedObservationCarriesTheTimeToTheFirstToken_whetherOrNotTheModelWasCold() {
        var timed = new StreamedChat.Timed(null, 1_200, 5_000);
        var cold = new CallContext(SERVER, MODEL, CallObservation.KIND_SIMPLE, true, 1).streamed(true, 0L, timed, 10, 50);
        var warm = new CallContext(SERVER, MODEL, CallObservation.KIND_SIMPLE, false, 1).streamed(true, 0L, timed, 10, 50);

        assertEquals(1_200, cold.firstTokenMs(), "a cold request's first token includes the load");
        assertEquals(CallObservation.UNKNOWN, cold.promptEvalMs());
        assertEquals(1_200, warm.firstTokenMs());
        assertEquals(1_200, warm.promptEvalMs());
        assertEquals(CallObservation.UNKNOWN, call(true, false, 1).firstTokenMs());
    }
}
