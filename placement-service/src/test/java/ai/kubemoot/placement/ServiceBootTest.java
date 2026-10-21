package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.TicketManager;
import io.quarkus.arc.Arc;
import io.quarkus.test.junit.QuarkusTest;
import org.junit.jupiter.api.Test;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.containsString;
import static org.hamcrest.Matchers.equalTo;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Boots the service through Quarkus ArC with no NATS configured, which is a standby that never
 * wins the lease: every bean resolves, readiness is down, the API answers 503 not_leader, and the
 * metrics are exported.
 */
@QuarkusTest
class ServiceBootTest {

    @Test
    void theServiceGraphResolves() {
        for (Class<?> bean : new Class<?>[] {PlacementEngine.class, TicketTable.class, TicketMirror.class,
                TicketManager.class, ServerSelector.class, Leadership.class, ServiceLifecycle.class,
                CapacityVersion.class, PlacementResource.class}) {
            assertTrue(Arc.container().instance(bean).isAvailable(), bean.getSimpleName() + " does not resolve");
        }
    }

    @Test
    void theTicketManagerKeepsItsTicketsInTheTable() {
        assertEquals(0, Arc.container().instance(TicketTable.class).get().size());
        assertFalse(Arc.container().instance(TicketTable.class).get().available());
    }

    @Test
    void aStandbyIsNotReady_butAlive() {
        given().when().get("/q/health/live").then().statusCode(200);
        given().when().get("/q/health/ready").then().statusCode(503).body(containsString("standby"));
    }

    @Test
    void aStandbyAnswersTheApiWithNotLeader() {
        given().contentType("application/json").body("{\"models\":[{\"name\":\"m\"}]}")
                .when().post("/placement/v1/place")
                .then().statusCode(503).body("code", equalTo("not_leader"));
        given().when().get("/placement/v1/capacity").then().statusCode(503).body("code", equalTo("not_leader"));
    }

    @Test
    void theMetricsAreExported() {
        given().when().get("/q/metrics").then().statusCode(200)
                .body(containsString("kubemoot_placement_leader"))
                .body(containsString("kubemoot_placement_tickets"));
    }
}
