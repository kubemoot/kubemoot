package ai.kubemoot.agent.modelserver;

import jakarta.ws.rs.core.Response;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The placement service API over the existing placement code: place, no fit, bad request, release. */
class PlacementResourceTest {

    private CallPlanner planner;
    private ServerSelector selector;
    private TicketManager tickets;
    private PlacementResource resource;

    @BeforeEach
    void setUp() {
        planner = mock(CallPlanner.class);
        selector = mock(ServerSelector.class);
        tickets = mock(TicketManager.class);
        when(selector.readState()).thenReturn(List.of());
        resource = new PlacementResource(planner, selector, tickets);
    }

    private static PlacementResource.PlaceRequest request(List<String> models) {
        return new PlacementResource.PlaceRequest("ns", "crew", "agent", "thread", "EVALUATING", models, 100, 256, true);
    }

    @Test
    void anEmptyModelListIsABadRequest() {
        assertEquals(400, resource.place(request(List.of())).getStatus());
        assertEquals(400, resource.place(request(null)).getStatus());
    }

    @Test
    void noRoomIsAConflictWithTheNoFitCode() {
        when(planner.place(any())).thenReturn(Optional.empty());
        Response r = resource.place(request(List.of("m")));
        assertEquals(409, r.getStatus());
        assertEquals("kubemoot_no_fit", ((java.util.Map<?, ?>) r.getEntity()).get("code"));
    }

    @Test
    void aPlacementReturnsTheServerTheModelAndATicketThatReleaseGivesBack() {
        ServerState server = mock(ServerState.class);
        when(server.name()).thenReturn("gpu-1");
        when(server.endpointFor("m")).thenReturn("http://gpu-1:8000");
        when(server.hasModelLoaded("m")).thenReturn(true);
        Ticket ticket = new Ticket("t1", "gpu-1", 1000, "holder", "now");
        CallPlanner.Placement placed = new CallPlanner.Placement(new Pick(server, ticket), "m", 1000, List.of());
        when(planner.place(any())).thenReturn(Optional.of(placed));
        when(planner.mode()).thenReturn(PlacementMode.ALGORITHM);

        Response r = resource.place(request(List.of("m", "backup")));
        assertEquals(200, r.getStatus());
        PlacementResource.PlaceReply reply = assertInstanceOf(PlacementResource.PlaceReply.class, r.getEntity());
        assertEquals("gpu-1", reply.server());
        assertEquals("http://gpu-1:8000", reply.endpoint());
        assertEquals("t1", reply.ticket());

        assertEquals(204, resource.release(new PlacementResource.ReleaseRequest("t1")).getStatus());
        verify(tickets).release(ticket);
    }

    @Test
    void releasingAnUnknownTicketChangesNothing() {
        assertEquals(204, resource.release(new PlacementResource.ReleaseRequest("nope")).getStatus());
        verify(tickets, never()).release(any());
    }
}
