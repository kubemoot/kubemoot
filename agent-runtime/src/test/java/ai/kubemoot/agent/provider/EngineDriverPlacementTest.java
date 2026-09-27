package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/** Eviction plans and queue estimates take their costs and concurrency from the lifecycle driver. */
class EngineDriverPlacementTest {

    private static final SwitchCostProfile CHEAP_RELEASE = new SwitchCostProfile(1024.0, 0.2, 10240.0);

    private static ProviderState gpu(Map<String, Long> loaded) {
        return new ProviderState("gpu", "http://gpu", 1, 0, 0, List.copyOf(loaded.keySet()), true,
                "2026-09-27T00:00:00Z", 32_768, loaded);
    }

    private static ProviderSelector selector(TicketManager tickets, EngineDriver driver) {
        return new ProviderSelector(null, new ObjectMapper(), tickets, null, driver);
    }

    @Test
    void cheaperReleaseAndWake_makeTheSameEvictionCheaper() {
        var demand = Map.of("qwen3:32b", new ModelDemand(0, 0, 1.0, 0.0, 1L));
        var state = List.of(gpu(Map.of("qwen3:32b", 27_000L)));

        var onOllama = selector(mock(TicketManager.class), null).planEviction("qwen3:14b", 12_224, state, demand).orElseThrow();
        var cheap = selector(mock(TicketManager.class), new FakeEngineDriver(CHEAP_RELEASE))
                .planEviction("qwen3:14b", 12_224, state, demand).orElseThrow();

        assertEquals(List.of("qwen3:32b"), onOllama.victimModels());
        assertTrue(cheap.costSeconds() < 15.0 && onOllama.costSeconds() > 15.0,
                "against a 15s wait: queue on Ollama, unload when release is cheap");
    }

    @Test
    void residentWithInFlightWork_blocksThePlanOnThatGpu() {
        var tickets = mock(TicketManager.class);
        when(tickets.activeModelsOn(anyString())).thenReturn(Set.of("qwen3:32b"));
        assertTrue(selector(tickets, null).planEviction("qwen3:14b", 12_224,
                List.of(gpu(Map.of("qwen3:32b", 27_000L))), Map.of()).isEmpty());
    }

    @Test
    void modelWithWaiters_isKeptWhenAnotherIdleModelMakesRoom() {
        var demand = Map.of("qwen3:14b", new ModelDemand(2, 0, 0, 0, 0));
        // Either model alone makes room; the one agents wait for stays.
        var plan = selector(mock(TicketManager.class), null).planEviction("qwen3:32b", 14_000,
                List.of(gpu(Map.of("qwen3:14b", 12_000L, "qwen3:8b", 16_000L))), demand).orElseThrow();
        assertEquals(List.of("qwen3:8b"), plan.victimModels());
    }

    @Test
    void modelPlannedForReleaseByAnotherCall_isNotChosenAgain() {
        var tickets = mock(TicketManager.class);
        when(tickets.plannedEvictionsOn(anyString())).thenReturn(Map.of("qwen3:8b", 8_000L));
        var plan = selector(tickets, null).planEviction("qwen3:32b", 14_000,
                List.of(gpu(Map.of("qwen3:14b", 16_000L, "qwen3:8b", 8_000L))), Map.of()).orElseThrow();
        assertEquals(List.of("qwen3:14b"), plan.victimModels());
    }

    @Test
    void queueOption_usesTheDriversWaitEstimate() {
        var tickets = mock(TicketManager.class);
        long now = System.currentTimeMillis();
        when(tickets.inFlightStartsFor(anyString())).thenReturn(List.of(java.time.Instant.ofEpochMilli(now - 5_000)));
        var sel = selector(tickets, null);
        var q = sel.queueOption(List.of("qwen3:14b"), List.of(gpu(Map.of("qwen3:14b", 12_224L)))).orElseThrow();
        // One call 5 seconds (or a little more, by the time it is read) into a 30-second default.
        assertTrue(q.waitSeconds() <= PlacementCostModel.DEFAULT_CALL_SECONDS - 5.0 + 0.01 && q.waitSeconds() > 15.0,
                "wait was " + q.waitSeconds());
        assertEquals(Optional.empty(), sel.queueOption(List.of("qwen3:70b"), List.of(gpu(Map.of()))));
    }
}
