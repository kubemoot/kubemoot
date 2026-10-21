package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/** Before a call goes out, a model the server does not show awake is made ready, or the call fails cleanly. */
class ModelReadinessTest {

    private static final String MODEL = "qwen3:14b";

    private final TicketManager tickets = mock(TicketManager.class);
    private final ServerSelector selector = spy(
            new ServerSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)));
    private final CallPlacement placement = new CallPlanner(selector, tickets, null);

    private static ServerState vllm(boolean awake) {
        return new ServerState("vllm-rig1", "", 1, 0, 0, awake ? List.of(MODEL) : List.of(), true, "", 0L,
                Map.of(), awake ? Map.of() : Map.of(MODEL, 0L), 0L, Map.of(), Map.of(), "vllm",
                Map.of(MODEL, "http://v:8000"));
    }

    private static Pick pick(ServerState p) {
        return new Pick(p, new Ticket("t1", p.name(), 512L, "agent", "2026-10-19T00:00:00Z"), null, false);
    }

    /** Answers every request the same way and records the requests. */
    private static final class Operator implements ModelLifecycle {
        final java.util.List<String> asked = new java.util.ArrayList<>();
        Reply answer = new Reply(true, "awake", 900L, "");

        @Override
        public Reply ready(String server, String model) {
            asked.add("ready " + server);
            return answer;
        }

        @Override
        public Reply release(String server, String model) {
            return answer;
        }
    }

    @Test
    void anAsleepModelIsWokenThroughTheOperator_andTheStateCacheIsDropped() {
        Operator operator = new Operator();
        selector.useLifecycle(operator);
        ModelReadiness.ensure(selector, placement, pick(vllm(false)), MODEL);
        assertEquals(List.of("ready vllm-rig1"), operator.asked);
        verify(selector).invalidateCache();
        verify(tickets, never()).release(any());
    }

    @Test
    void anAwakeModelAsksNothing() {
        Operator operator = new Operator();
        selector.useLifecycle(operator);
        ModelReadiness.ensure(selector, placement, pick(vllm(true)), MODEL);
        assertTrue(operator.asked.isEmpty());
        verify(selector, never()).invalidateCache();
    }

    @Test
    void aFailureReleasesTheTicketOnceRecordsTheServerAndFailsTheCall() {
        Operator operator = new Operator();
        operator.answer = new ModelLifecycle.Reply(false, "", 0L, "the server has no sleep mode");
        selector.useLifecycle(operator);
        Pick p = pick(vllm(false));

        IllegalStateException e = assertThrows(IllegalStateException.class,
                () -> ModelReadiness.ensure(selector, placement, p, MODEL));

        assertTrue(e.getMessage().contains("the server has no sleep mode"), e.getMessage());
        verify(tickets, times(1)).release(p.ticket());
        verify(selector).recordFailure("vllm-rig1");
        verify(selector, never()).invalidateCache();
    }

    @Test
    void ollamaLoadsOnDemand_nothingIsAskedAndTheCacheStays() {
        ServerState ollama = new ServerState("ollama-cpu", "http://cpu:11434", 1, 0, 0, List.of(), true, "",
                0L, Map.of());
        ModelReadiness.ensure(selector, placement, pick(ollama), MODEL);
        verify(selector, never()).invalidateCache();
        verify(tickets, never()).release(any());
    }

    @Test
    void withoutASelectorThereIsNothingToAsk() {
        assertDoesNotThrow(() -> ModelReadiness.ensure(null, placement, pick(vllm(false)), MODEL));
    }
}
