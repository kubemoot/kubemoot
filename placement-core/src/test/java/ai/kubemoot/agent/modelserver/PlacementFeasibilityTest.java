package ai.kubemoot.agent.modelserver;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The three refusals no wait can cure, and the busy cluster where waiting is right. */
class PlacementFeasibilityTest {

    private static final String MODEL = "qwen3:14b";

    private static ServerState server(long vramMiB, long context, boolean hasModel) {
        return new ServerState("rig0", "http://rig0", 1, 0, 0, hasModel ? List.of(MODEL) : List.of(), true,
                "2026-10-20T00:00:00Z", vramMiB, hasModel ? Map.of(MODEL, 9_000L) : Map.of(),
                hasModel ? Map.of() : Map.of(), context, Map.of());
    }

    private static java.util.Optional<PlacementFeasibility.Refusal> check(ServerState s, long footprint,
                                                                         long promptTokens) {
        return PlacementFeasibility.refusal(List.of(s), MODEL, footprint, List.of(MODEL), promptTokens,
                m -> footprint, name -> Set.of());
    }

    @Test
    void aBusyClusterIsNotARefusal() {
        assertTrue(check(server(32_000, 8_192, true), 9_000, 100).isEmpty());
    }

    @Test
    void aModelNoAcceleratorCanHoldIsRefused() {
        var r = check(server(8_000, 8_192, true), 20_000, 100).orElseThrow();
        assertEquals(PlacementFeasibility.MODEL_TOO_LARGE, r.code());
        assertInstanceOf(NoFitException.class, r.toException());
        assertEquals(NoFitException.REASON_MODEL_TOO_LARGE, ((NoFitException) r.toException()).reason());
    }

    @Test
    void aModelNoServerHasIsRefusedAsAFailureNotAStandAside() {
        var r = check(server(32_000, 8_192, false), 9_000, 100).orElseThrow();
        assertEquals(PlacementFeasibility.NO_SERVER_HAS_MODEL, r.code());
        assertInstanceOf(IllegalStateException.class, r.toException());
        assertTrue(r.detail().contains(MODEL));
    }

    @Test
    void aPromptNoContextWindowHoldsIsRefused() {
        var r = check(server(32_000, 4_096, true), 9_000, 50_000).orElseThrow();
        assertEquals(PlacementFeasibility.PROMPT_TOO_LARGE, r.code());
        assertEquals(NoFitException.REASON_PROMPT_TOO_LARGE, ((NoFitException) r.toException()).reason());
    }

    @Test
    void aModelBeingLoadedByAnotherClaimCountsAsPresent() {
        var s = server(32_000, 8_192, false);
        var r = PlacementFeasibility.refusal(List.of(s), MODEL, 9_000, List.of(MODEL), 100, m -> 9_000,
                name -> Set.of(MODEL));
        assertTrue(r.isEmpty());
    }
}
