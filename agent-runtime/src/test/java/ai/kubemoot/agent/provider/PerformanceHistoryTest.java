package ai.kubemoot.agent.provider;

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
    private static final String PROVIDER = "gpu-a";
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
        var h = history(new InMemoryKv().provider(PerformanceHistory.BUCKET), "a", new SchedulerMetrics(registry, "learned"));

        h.record(call(true, false, 100));
        h.record(new CallObservation("", "", CallObservation.KIND_SIMPLE, true, false, 0, 0, 1, -1, -1, -1, 0, 0L));
        h.recordWait(PROVIDER, MODEL, 2_000);
        h.recordWait(PROVIDER, MODEL, -1);
        h.recordUnplacedWait(MODEL, 9_000);

        assertEquals(1.0, registry.find(SchedulerMetrics.CALLS).counter().count(), "an unattributable call is not counted");
        assertEquals(1, registry.find(SchedulerMetrics.WAIT_SECONDS).tag("outcome", "placed").timer().count());
        assertEquals(1, registry.find(SchedulerMetrics.WAIT_SECONDS).tag("outcome", "not_placed").timer().count());
    }

    private static CallObservation call(boolean ok, boolean cold, long ms) {
        return new CallObservation(PROVIDER, MODEL, CallObservation.KIND_TOOL_TURN, ok, cold, 1200, 300, ms,
                CallObservation.UNKNOWN, CallObservation.UNKNOWN, CallObservation.UNKNOWN, 2, 1_000L);
    }

    private static JsonNode stored(InMemoryKv bucket, String agent) throws Exception {
        byte[] v = bucket.data.get(PerformanceHistory.key(PROVIDER, MODEL, "crew-ns/" + agent));
        assertNotNull(v, "no entry written");
        return MAPPER.readTree(v);
    }

    @Test
    void record_writesTheRequestUnderThisAgentsKey() throws Exception {
        var bucket = new InMemoryKv();
        history(bucket.provider(PerformanceHistory.BUCKET), "a").record(call(true, true, 4_000));

        JsonNode e = stored(bucket, "a");
        assertEquals(PROVIDER, e.path("provider").asText());
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
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");
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
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");
        for (int i = 0; i < PerformanceHistory.MAX_WAITS + 3; i++) {
            h.recordWait(PROVIDER, MODEL, 1_000L + i);
        }

        JsonNode waits = stored(bucket, "a").path(PerformanceHistory.F_WAITS);
        assertEquals(PerformanceHistory.MAX_WAITS, waits.size());
        assertEquals(1_003L, waits.get(0).path("ms").asLong());
        assertEquals(0, stored(bucket, "a").path("requests").asLong(), "a wait is not a request");
    }

    @Test
    void unattributableObservationsAndNegativeWaits_areIgnored() {
        var bucket = new InMemoryKv();
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");
        h.record(null);
        h.record(new CallObservation("", MODEL, CallObservation.KIND_SIMPLE, true, false, 0, 0, 1,
                -1, -1, -1, 0, 0));
        h.recordWait(PROVIDER, null, 10);
        h.recordWait(PROVIDER, MODEL, -1);
        assertTrue(bucket.data.isEmpty());
    }

    @Test
    void entriesFor_readsEveryAgentsEntry_forThatProviderAndModelOnly() {
        var bucket = new InMemoryKv();
        var nats = bucket.provider(PerformanceHistory.BUCKET);
        history(nats, "a").record(call(true, false, 100));
        history(nats, "b").record(call(true, false, 200));
        history(nats, "b").record(new CallObservation(PROVIDER, "qwen3:8b", CallObservation.KIND_TRIAGE, true,
                false, 10, 10, 50, -1, -1, -1, 0, 0));

        assertEquals(2, history(nats, "c").entriesFor(PROVIDER, MODEL).size());
        assertEquals(1, history(nats, "c").entriesFor(PROVIDER, "qwen3:8b").size());
        assertTrue(history(nats, "c").entriesFor("gpu-b", MODEL).isEmpty());
    }

    @Test
    void afterARestart_theAgentContinuesItsStoredEntry() throws Exception {
        var bucket = new InMemoryKv();
        var nats = bucket.provider(PerformanceHistory.BUCKET);
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
        assertTrue(h.entriesFor(PROVIDER, MODEL).isEmpty());
    }

    // ---- CallObservation ----

    @Test
    void aFailedRead_skipsTheWrite_andNeverReplacesTheStoredHistory() throws Exception {
        var bucket = new InMemoryKv();
        var nats = bucket.provider(PerformanceHistory.BUCKET);
        history(nats, "a").record(call(true, false, 100));
        String key = PerformanceHistory.key(PROVIDER, MODEL, "crew-ns/a");
        when(bucket.kv.get(key)).thenThrow(new java.io.IOException("timeout"));

        var restarted = history(nats, "a");
        restarted.record(call(true, false, 200));

        assertEquals(1, stored(bucket, "a").path("requests").asLong(), "the stored history is kept");
        assertEquals(1, restarted.droppedWrites());
    }

    @Test
    void aCorruptStoredEntry_isRebuilt() throws Exception {
        var bucket = new InMemoryKv();
        String key = PerformanceHistory.key(PROVIDER, MODEL, "crew-ns/a");
        bucket.data.put(key, "{\"samples\": 3, \"waits\": \"x\"}".getBytes());

        history(bucket.provider(PerformanceHistory.BUCKET), "a").record(call(true, false, 100));

        JsonNode e = stored(bucket, "a");
        assertEquals(1, e.path(PerformanceHistory.F_SAMPLES).size());
        assertTrue(e.path(PerformanceHistory.F_WAITS).isArray());
    }

    @Test
    void aFailedWrite_isCounted_andDoesNotThrow() throws Exception {
        var bucket = new InMemoryKv();
        when(bucket.kv.put(any(String.class), any(byte[].class))).thenThrow(new java.io.IOException("full"));
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");

        assertDoesNotThrow(() -> h.record(call(true, false, 100)));
        assertEquals(1, h.droppedWrites());
    }

    @Test
    void theBackgroundWriter_writesOffTheCallersThread() throws Exception {
        var bucket = new InMemoryKv();
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn("a");
        when(props.namespace()).thenReturn(Optional.of("crew-ns"));
        var h = new PerformanceHistory(bucket.provider(PerformanceHistory.BUCKET), MAPPER, props, SchedulerMetrics.none());

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
        return new CallContext(PROVIDER, MODEL, kind, cold, 3);
    }

    @Test
    void observedModel_recordsEachRequest_coldOnlyOnTheFirst() throws Exception {
        var bucket = new InMemoryKv();
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");
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
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");
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
        var h = history(bucket.provider(PerformanceHistory.BUCKET), "a");
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
        doThrow(new IllegalStateException("broken")).when(h).record(any());
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
        assertEquals(PROVIDER, o.provider());
        assertEquals(3, o.activeCalls());
        assertEquals(70L, o.durationMs());
        assertEquals(CallObservation.UNKNOWN, o.evalMs());
    }
}
