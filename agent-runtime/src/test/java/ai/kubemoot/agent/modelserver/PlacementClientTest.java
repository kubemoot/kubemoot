package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.net.http.HttpClient;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The HTTP side of a caller: answers are parsed, and everything that is not an answer is "unavailable". */
class PlacementClientTest {

    private static final ObjectMapper M = new ObjectMapper();

    private FakePlacementServer server;
    private final AtomicLong nanos = new AtomicLong(1);

    @BeforeEach
    void setUp() throws Exception {
        server = new FakePlacementServer();
    }

    @AfterEach
    void tearDown() {
        server.close();
    }

    private PlacementClient client(String url, Duration requestTimeout) {
        return new PlacementClient(url, M, requestTimeout, nanos::get,
                HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofMillis(300)).build());
    }

    @Test
    void anAnswerIsParsed_andAConflictIsAnAnswerNotAnOutage() {
        server.on("POST", "/placement/v1/place", FakePlacementServer.Reply.json(409,
                "{\"code\":\"kubemoot_no_fit\",\"reason\":\"busy\"}"));
        var c = client(server.url(), Duration.ofSeconds(2));

        var answer = c.post("/placement/v1/place", M.createObjectNode().put("x", 1));

        assertEquals(409, answer.status());
        assertEquals("kubemoot_no_fit", answer.body().path("code").asText());
        assertFalse(c.open());
    }

    @Test
    void anEmptyBodyIsNull() {
        server.on("POST", "/placement/v1/release", FakePlacementServer.Reply.noContent());
        var answer = client(server.url(), Duration.ofSeconds(2)).post("/placement/v1/release", M.createObjectNode());
        assertEquals(204, answer.status());
        assertEquals(null, answer.body());
    }

    @Test
    void aServerErrorIsUnavailable_includingANotLeaderStandby() {
        server.on("POST", "/placement/v1/place", FakePlacementServer.Reply.json(503,
                "{\"code\":\"not_leader\",\"reason\":\"this replica is not the placement leader\"}"));
        var c = client(server.url(), Duration.ofSeconds(2));

        var e = assertThrows(PlacementUnavailableException.class,
                () -> c.post("/placement/v1/place", M.createObjectNode()));
        assertTrue(e.getMessage().contains("503"));
        assertTrue(e.getMessage().contains("not_leader"));
    }

    @Test
    void noConnectionIsUnavailable() {
        int closedPort = server.port();
        server.close();
        var c = client("http://127.0.0.1:" + closedPort, Duration.ofSeconds(2));
        assertThrows(PlacementUnavailableException.class, () -> c.post("/placement/v1/place", M.createObjectNode()));
    }

    @Test
    void aServiceThatStopsAnsweringIsUnavailableAtTheTimeout() {
        server.on("POST", "/placement/v1/place", new FakePlacementServer.Reply(200, "{}", 3_000));
        var c = client(server.url(), Duration.ofMillis(200));
        long start = System.nanoTime();
        assertThrows(PlacementUnavailableException.class, () -> c.post("/placement/v1/place", M.createObjectNode()));
        assertTrue(Duration.ofNanos(System.nanoTime() - start).toMillis() < 2_000, "the call is not held for the service's sake");
    }

    @Test
    void afterAFailureTheClientAnswersAtOnceWithoutTouchingTheNetwork_thenTriesAgain() {
        server.on("POST", "/placement/v1/place", FakePlacementServer.Reply.json(503, "{}"));
        var c = client(server.url(), Duration.ofSeconds(2));
        assertThrows(PlacementUnavailableException.class, () -> c.post("/placement/v1/place", M.createObjectNode()));
        int requestsAfterFirst = server.requests.size();

        for (int i = 0; i < 5; i++) {
            var e = assertThrows(PlacementUnavailableException.class,
                    () -> c.post("/placement/v1/place", M.createObjectNode()));
            assertTrue(e.getMessage().contains("recently unreachable"));
        }
        assertEquals(requestsAfterFirst, server.requests.size(), "an outage costs one failed request, not one per call");
        assertTrue(c.open());

        nanos.addAndGet(PlacementClient.BREAKER.toNanos() + 1);
        server.on("POST", "/placement/v1/place", FakePlacementServer.Reply.ok("{\"server\":\"rig0\"}"));
        assertEquals(200, c.post("/placement/v1/place", M.createObjectNode()).status());
        assertFalse(c.open(), "an answer closes the breaker");
    }

    @Test
    void anAbandonedWaitClosesItsRequest() throws Exception {
        server.on("POST", "/placement/v1/capacity/await", new FakePlacementServer.Reply(200, "{}", 5_000));
        var c = client(server.url(), Duration.ofSeconds(10));
        var pending = c.postAsync("/placement/v1/capacity/await", M.createObjectNode(), Duration.ofSeconds(10));
        assertFalse(pending.isDone());
        pending.cancel(true);
        assertTrue(pending.isCancelled());
    }

    @Test
    void aSlashOrNotInTheBaseUrlReachesTheSamePath() {
        server.on("GET", "/placement/v1/capacity", FakePlacementServer.Reply.ok("{\"version\":7}"));
        assertEquals(7, client(server.url() + "/", Duration.ofSeconds(2)).get("/placement/v1/capacity")
                .body().path("version").asInt());
        assertEquals(7, client(server.url(), Duration.ofSeconds(2)).get("placement/v1/capacity")
                .body().path("version").asInt());
    }
}
