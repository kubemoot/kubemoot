package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.provider.CallPlanner.PlacementRequest;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

/**
 * When no provider has room, the algorithm weighs queueing against unloading idle
 * models, unless the request says it may not unload: then it places nothing.
 */
class AlgorithmPlacementUnloadTest {

    private static final String MODEL = "qwen3:8b";

    private static PlacementRequest request(boolean mayUnload) {
        ProviderState full = new ProviderState("gpu", "http://gpu", 1, 0, 0, List.of("other:32b"), true,
                "2026-01-01T00:00:00Z", 32_000, Map.of("other:32b", 30_000L));
        return new PlacementRequest(MODEL, List.of(), List.of(full), m -> 5_000L, m -> 0L, 100L, mayUnload);
    }

    @Test
    void aRequestThatMayNotUnloadNeverWeighsUnloading() {
        ProviderSelector selector = mock(ProviderSelector.class);
        var algorithm = new AlgorithmPlacement(selector, mock(TicketManager.class), null);

        assertTrue(algorithm.place(request(false)).isEmpty());
        verify(selector, never()).planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong());
        verify(selector, never()).claimWithEvictions(anyString(), anyLong(), anyLong(), any(), anyList());
    }

    @Test
    void aProviderTheRuntimeCannotUnloadIsNeverPlannedAnEviction() {
        ProviderSelector selector = mock(ProviderSelector.class);
        var algorithm = new AlgorithmPlacement(selector, mock(TicketManager.class), null);
        when(selector.driverFor(any())).thenReturn(new NoLifecycleDriver(new OllamaDriver(null), "vllm"));

        algorithm.place(request(true));
        verify(selector).planEviction(eq(MODEL), eq(5_000L), eq(List.of()), anyMap(), eq(100L));
        verify(selector, never()).claimWithEvictions(anyString(), anyLong(), anyLong(), any(), anyList());
    }

    @Test
    void aRequestThatMayUnloadWeighsIt() {
        ProviderSelector selector = mock(ProviderSelector.class);
        var algorithm = new AlgorithmPlacement(selector, mock(TicketManager.class), null);

        when(selector.driverFor(any())).thenReturn(new OllamaDriver(null));
        algorithm.place(request(true));
        verify(selector).planEviction(eq(MODEL), eq(5_000L), anyList(), anyMap(), eq(100L));
    }
}
