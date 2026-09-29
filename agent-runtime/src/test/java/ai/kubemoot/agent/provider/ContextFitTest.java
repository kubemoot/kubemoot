package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/**
 * A call's prompt must fit the context window the chosen provider gives the model;
 * the engine cuts an oversized prompt without an error. Covers the check itself,
 * the provider state it reads, and the selector paths that apply it.
 */
class ContextFitTest {

    private static final String MODEL = "any-family:14b";

    /** A ready provider with {@code MODEL} loaded at {@code loadedContext} and a provider context of {@code context}. */
    private static ProviderState gpu(String name, long context, long loadedContext) {
        Map<String, Long> loaded = loadedContext > 0 ? Map.of(MODEL, loadedContext) : Map.of();
        return new ProviderState(name, "http://" + name, 1, 0, 0, List.of(MODEL), true,
                "2026-09-29T00:00:00Z", 48_000, Map.of(MODEL, 10_000L), Map.of(), context, loaded);
    }

    // ---- ProviderState.contextLengthFor ----

    @Test
    void loadedModelsContext_winsOverTheProviderContext() {
        assertEquals(40_960, gpu("a", 8_192, 40_960).contextLengthFor(MODEL));
        assertEquals(8_192, gpu("a", 8_192, 40_960).contextLengthFor("not-loaded:8b"));
    }

    @Test
    void loadedContext_isMatchedByExactModelName() {
        // /api/ps names a model by its tag; a differently written tag is another model.
        assertEquals(8_192, gpu("a", 8_192, 40_960).contextLengthFor(MODEL + ":latest"));
    }

    @Test
    void unknownContext_isZero_evenForNullInputs() {
        var legacy = new ProviderState("a", "http://a", 1, 0, 0, List.of(), true, "", 0L, Map.of());
        assertEquals(0, legacy.contextLengthFor(MODEL));
        assertEquals(0, legacy.contextLengthFor(null));
        var nullMap = new ProviderState("a", "http://a", 1, 0, 0, List.of(), true, "", 0L, Map.of(), Map.of(), -5L, null);
        assertEquals(0, nullMap.contextLengthFor(MODEL), "a negative context is unknown, not a limit");
    }

    // ---- ContextFit ----

    @Test
    void holds_comparesThePromptWithTheContext() {
        var p = gpu("a", 8_192, 0);
        assertTrue(ContextFit.holds(p, MODEL, 8_192), "a prompt exactly the size of the context fits");
        assertFalse(ContextFit.holds(p, MODEL, 8_193));
    }

    @Test
    void holds_neverRefusesWhenTheContextOrPromptIsUnknown() {
        assertTrue(ContextFit.holds(gpu("a", 0, 0), MODEL, 1_000_000), "unknown context");
        assertTrue(ContextFit.holds(gpu("a", 8_192, 0), MODEL, 0), "unknown prompt size");
        assertTrue(ContextFit.holds(null, MODEL, 1_000_000), "no provider state");
    }

    @Test
    void anyCanHold_isFalseOnlyWhenEveryContextIsTooSmall() {
        var small = gpu("small", 8_192, 0);
        var large = gpu("large", 32_768, 0);
        assertTrue(ContextFit.anyCanHold(List.of(small, large), List.of(MODEL), 20_000));
        assertFalse(ContextFit.anyCanHold(List.of(small), List.of(MODEL), 20_000));
        assertTrue(ContextFit.anyCanHold(List.of(small, gpu("unknown", 0, 0)), List.of(MODEL), 20_000),
                "a provider with an unknown context might hold it");
        assertTrue(ContextFit.anyCanHold(List.of(), List.of(MODEL), 20_000), "no state never proves it too large");
        assertTrue(ContextFit.anyCanHold(null, List.of(MODEL), 20_000));
    }

    @Test
    void anyCanRun_judgesVramAndContextForTheSameProviderAndModel() {
        // The big-context provider cannot hold the big model; the provider that can
        // hold it gives it too small a context. Neither pair can run the prompt.
        var roomyContextSmallGpu = new ProviderState("a", "http://a", 1, 0, 0, List.of(), true, "",
                12_000, Map.of(), Map.of(), 32_768, Map.of());
        var bigGpuSmallContext = new ProviderState("b", "http://b", 1, 0, 0, List.of(), true, "",
                48_000, Map.of(), Map.of(), 8_192, Map.of());
        java.util.function.ToLongFunction<String> footprint = m -> 20_000L;
        var states = List.of(roomyContextSmallGpu, bigGpuSmallContext);

        assertTrue(ContextFit.anyCanHold(states, List.of(MODEL), 20_000), "context alone looks fine");
        assertFalse(ContextFit.anyCanRun(states, List.of(MODEL), 20_000, footprint));
        assertTrue(ContextFit.anyCanRun(states, List.of(MODEL), 4_000, footprint), "a small prompt runs on b");
        assertTrue(ContextFit.anyCanRun(states, List.of(MODEL), 20_000, m -> 0L), "an unknown footprint fits");
        assertTrue(ContextFit.anyCanRun(List.of(), List.of(MODEL), 20_000, footprint));
    }

    @Test
    void largestContext_acrossProvidersAndModels() {
        assertEquals(32_768, ContextFit.largestContext(List.of(gpu("a", 8_192, 0), gpu("b", 4_096, 32_768)),
                List.of(MODEL, "other:8b")));
        assertEquals(0, ContextFit.largestContext(null, List.of(MODEL)));
    }

    // ---- the operator document ----

    @Test
    void fromJson_readsTheContextFields() throws Exception {
        var json = "{\"name\":\"a\",\"contextLength\":16384,\"loadedModelContextLengths\":{\"" + MODEL + "\":8192}}";
        var s = ProviderSelector.fromJson(new ObjectMapper().readTree(json));
        assertEquals(16_384, s.contextLength());
        assertEquals(8_192, s.contextLengthFor(MODEL));
        assertEquals(16_384, s.contextLengthFor("other:8b"));
        assertEquals(0, ProviderSelector.fromJson(new ObjectMapper().readTree("{}")).contextLengthFor(MODEL));
    }

    // ---- the selector ----

    private static ProviderSelector selectorOver(List<ProviderState> states, TicketManager tickets) {
        var sel = spy(new ProviderSelector(null, new ObjectMapper(), tickets, null, null));
        doReturn(states).when(sel).readState();
        return sel;
    }

    private static TicketManager ticketsGranting() {
        var tickets = mock(TicketManager.class);
        when(tickets.claim(anyString(), anyLong(), anyLong(), anyLong(), any()))
                .thenAnswer(inv -> Optional.of(new Ticket("t", inv.getArgument(0), 512, "test", "2026-09-29T00:00:00Z")));
        return tickets;
    }

    @Test
    void pickAndClaim_skipsAProviderWhoseContextCannotHoldThePrompt() {
        var tickets = ticketsGranting();
        var sel = selectorOver(List.of(gpu("small-context", 8_192, 0), gpu("large-context", 32_768, 0)), tickets);

        for (int i = 0; i < 5; i++) {
            var pick = sel.pickAndClaim(MODEL, 10_000, 256, 20_000).orElseThrow();
            assertEquals("large-context", pick.provider().name());
        }
        verify(tickets, never()).claim(eq("small-context"), anyLong(), anyLong(), anyLong(), any());
    }

    @Test
    void pickAndClaim_emptyWhenNoContextHoldsThePrompt_inEveryMode() {
        var tickets = ticketsGranting();
        var sel = selectorOver(List.of(gpu("a", 8_192, 0), gpu("b", 4_096, 0)), tickets);

        assertTrue(sel.pickAndClaim(MODEL, 10_000, 256, 20_000).isEmpty());
        assertTrue(sel.pickAndClaimWarm(MODEL, 10_000, 256, 20_000).isEmpty());
        assertTrue(sel.pickAndClaimLoading(MODEL, 10_000, 256, 20_000).isEmpty());
        verify(tickets, never()).claim(anyString(), anyLong(), anyLong(), anyLong(), any());
    }

    @Test
    void pickAndClaim_withoutAPromptSize_keepsEveryProvider() {
        var sel = selectorOver(List.of(gpu("small-context", 8_192, 0)), ticketsGranting());
        assertTrue(sel.pickAndClaim(MODEL, 10_000, 256).isPresent());
    }

    @Test
    void queueOption_skipsACopyWhoseContextCannotHoldThePrompt() {
        var sel = selectorOver(List.of(), mock(TicketManager.class));
        var states = List.of(gpu("small-context", 8_192, 0), gpu("large-context", 32_768, 0));

        assertEquals("large-context", sel.queueOption(List.of(MODEL), states, 20_000).orElseThrow().provider());
        assertTrue(sel.queueOption(List.of(MODEL), List.of(gpu("small-context", 8_192, 0)), 20_000).isEmpty());
    }

    @Test
    void planEviction_skipsAProviderWhoseContextCannotHoldThePrompt() {
        var sel = selectorOver(List.of(), mock(TicketManager.class));
        var full = new ProviderState("small-context", "http://s", 1, 0, 0, List.of("idle:32b"), true,
                "2026-09-29T00:00:00Z", 24_000, Map.of("idle:32b", 20_000L), Map.of(), 8_192, Map.of());

        assertTrue(sel.planEviction(MODEL, 10_000, List.of(full), Map.of(), 0).isPresent(),
                "with no prompt size the idle model can be unloaded");
        assertTrue(sel.planEviction(MODEL, 10_000, List.of(full), Map.of(), 20_000).isEmpty(),
                "unloading cannot make the context larger");
    }
}
