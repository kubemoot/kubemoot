package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.Placement;
import ai.kubemoot.agent.modelserver.CallPlanner.PlacementRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * A call that claims a ticket on a vLLM server reads the server's state again afterwards. A
 * model that became draining between the ranking and the claim is not served: the ticket is
 * released and the call is placed again. A ticket claimed before the operator publishes
 * draining stays, and the operator's drain counts it.
 */
class DrainingClaimTest {

    private static final String MODEL = "qwen3:14b";
    private static final long FOOTPRINT_MIB = 9_000L;

    private TicketManager tickets;
    private final AtomicBoolean draining = new AtomicBoolean(false);
    private final java.util.List<Ticket> issued = new java.util.ArrayList<>();

    private static ServerState vllm(String name, boolean drainingNow, double kv) {
        return new ServerState(name, "", 1, 0, 0, List.of(MODEL), true, "2026-10-19T00:00:00Z", 0L,
                Map.of(MODEL, 0L), Map.of(), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://" + name + ":8000"),
                0, 0, kv, Map.of(), drainingNow ? Map.of(MODEL, "draining") : Map.of(MODEL, "awake"));
    }

    /** a is the better server; the operator publishes it draining once {@code draining} is set. */
    private ServerSelector selector() {
        return new ServerSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ServerState> readState() {
                return List.of(vllm("vllm-a", draining.get(), 0.1), vllm("vllm-b", false, 0.3));
            }
        };
    }

    private static PlacementRequest request() {
        return new PlacementRequest(MODEL, List.of(), List.of(vllm("vllm-a", false, 0.1), vllm("vllm-b", false, 0.3)), m -> FOOTPRINT_MIB, m -> 0L, 500L);
    }

    /** Hands out a ticket; {@code onClaimOnA} runs when the claim is on vllm-a. */
    private void issueTickets(Runnable onClaimOnA) {
        org.mockito.stubbing.Answer<Optional<Ticket>> answer = inv -> {
            if ("vllm-a".equals(inv.getArgument(0))) {
                onClaimOnA.run();
            }
            Ticket t = new Ticket("t" + issued.size(), inv.getArgument(0), inv.getArgument(1), "agent",
                    "2026-10-19T00:00:00Z");
            issued.add(t);
            return Optional.of(t);
        };
        when(tickets.claimSlot(anyString(), anyLong(), anyLong(), anyLong(), anyString(), any())).thenAnswer(answer);
        when(tickets.claim(anyString(), anyLong(), anyLong(), anyLong(), anyString())).thenAnswer(answer);
        when(tickets.claimLoad(anyString(), anyLong(), anyLong(), anyLong(), anyString(), any())).thenAnswer(answer);
    }

    @BeforeEach
    void setUp() {
        tickets = mock(TicketManager.class);
    }

    @Test
    void aTicketClaimedAfterDrainingWasPublishedIsReleasedAndTheCallIsPlacedAgain() {
        issueTickets(() -> draining.set(true)); // the operator publishes draining as the claim lands
        Optional<Placement> placed = new AlgorithmPlacement(selector(), tickets, null).place(request());
        assertEquals("vllm-b", placed.orElseThrow().pick().server().name(), "placed on the other awake provider");
        Ticket onA = issued.get(0);
        assertEquals("vllm-a", onA.server());
        verify(tickets).release(onA);
        verify(tickets, never()).release(issued.get(1));
    }

    @Test
    void aTicketClaimedBeforeDrainingWasPublishedStaysAndHoldsTheSwitch() {
        issueTickets(() -> { });
        Optional<Placement> placed = new AlgorithmPlacement(selector(), tickets, null).place(request());
        assertEquals("vllm-a", placed.orElseThrow().pick().server().name());
        draining.set(true); // the operator publishes draining after the claim: its drain counts this ticket
        verify(tickets, never()).release(any(Ticket.class));
        assertEquals(1, issued.size());
    }

    @Test
    void aModelThatIsAlreadyDrainingIsNeverClaimedAsAWarmCopy() {
        draining.set(true);
        issueTickets(() -> { });
        Optional<Placement> placed = new AlgorithmPlacement(selector(), tickets, null).place(request());
        Placement p = placed.orElseThrow();
        assertFalse(p.pick().server().hasModelLoaded(MODEL) && "vllm-a".equals(p.pick().server().name()),
                "a draining model is never read as awake");
        verify(tickets, never()).release(eq(issued.get(0)));
    }

    @Test
    void aCopyAnotherCallIsWakingStaysClaimedSoASecondCallCanJoinIt() {
        // The model is asleep in the published state and a first call's ticket is waking it, so
        // it ranks warm only through the loading overlay. It reads not awake in the fresh state
        // too: that is not a copy that left the awake state, and the claim must stay.
        ServerState waking = new ServerState("vllm-a", "", 1, 0, 0, List.of(), true, "2026-10-19T00:00:00Z", 0L,
                Map.of(), Map.of(MODEL, 0L), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://vllm-a:8000"),
                0, 0, 0.1, Map.of(), Map.of(MODEL, "asleep"));
        when(tickets.activeModelsOn("vllm-a")).thenReturn(java.util.Set.of(MODEL));
        issueTickets(() -> { });
        ServerSelector selector = new ServerSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ServerState> readState() {
                return List.of(waking);
            }
        };
        Optional<Placement> placed = new AlgorithmPlacement(selector, tickets, null)
                .place(new PlacementRequest(MODEL, List.of(), List.of(waking), m -> FOOTPRINT_MIB, m -> 0L, 500L));
        assertEquals("vllm-a", placed.orElseThrow().pick().server().name());
        verify(tickets, never()).release(any(Ticket.class));
    }
}
