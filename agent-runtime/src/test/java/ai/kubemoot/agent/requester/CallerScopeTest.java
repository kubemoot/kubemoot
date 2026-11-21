package ai.kubemoot.agent.requester;

import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class CallerScopeTest {

    private static Caller caller(String conversation, String id) {
        return new Caller(conversation, new Principal("subscriber", "oreilly", id, null));
    }

    @Test
    void theBindingLastsExactlyUntilTheScopeCloses() {
        assertTrue(CallerScope.current().isEmpty());
        try (var scope = CallerScope.bind(caller("c1", "p1"))) {
            assertEquals("c1", CallerScope.current().orElseThrow().conversationId());
        }
        assertTrue(CallerScope.current().isEmpty());
    }

    @Test
    void aNestedBindingRestoresTheOuterOne() {
        try (var outer = CallerScope.bind(caller("outer", "p1"))) {
            try (var inner = CallerScope.bind(caller("inner", "p2"))) {
                assertEquals("inner", CallerScope.current().orElseThrow().conversationId());
            }
            assertEquals("outer", CallerScope.current().orElseThrow().conversationId());
        }
        assertTrue(CallerScope.current().isEmpty());
    }

    @Test
    void bindingNullClearsTheCaller() {
        try (var outer = CallerScope.bind(caller("outer", "p1"))) {
            try (var none = CallerScope.bind(null)) {
                assertTrue(CallerScope.current().isEmpty());
            }
            assertEquals("outer", CallerScope.current().orElseThrow().conversationId());
        }
    }

    @Test
    void theBindingIsClosedEvenWhenTheTurnFails() {
        try {
            try (var scope = CallerScope.bind(caller("c1", "p1"))) {
                throw new IllegalStateException("turn failed");
            }
        } catch (IllegalStateException expected) {
            // the turn failed; the scope must still have closed
        }
        assertTrue(CallerScope.current().isEmpty());
    }

    @Test
    void twoInterleavedTurnsNeverSeeEachOthersCaller() throws Exception {
        var bothBound = new CountDownLatch(2);
        var firstHasLooked = new CountDownLatch(1);
        ExecutorService pool = Executors.newFixedThreadPool(2);
        try {
            Future<Map<String, Object>> a = pool.submit(() -> {
                try (var scope = CallerScope.bind(caller("conv-a", "person-a"))) {
                    bothBound.countDown();
                    bothBound.await(5, TimeUnit.SECONDS);
                    Map<String, Object> seen = CallerScope.current().orElseThrow().toolMeta();
                    firstHasLooked.countDown();
                    return seen;
                }
            });
            Future<Map<String, Object>> b = pool.submit(() -> {
                try (var scope = CallerScope.bind(caller("conv-b", "person-b"))) {
                    bothBound.countDown();
                    bothBound.await(5, TimeUnit.SECONDS);
                    firstHasLooked.await(5, TimeUnit.SECONDS);
                    return CallerScope.current().orElseThrow().toolMeta();
                }
            });
            assertEquals(Map.of("conversationId", "conv-a", "principalId", "person-a"),
                    a.get(10, TimeUnit.SECONDS));
            assertEquals(Map.of("conversationId", "conv-b", "principalId", "person-b"),
                    b.get(10, TimeUnit.SECONDS));
        } finally {
            pool.shutdownNow();
        }
    }

    @Test
    void anAnonymousCallerSendsTheConversationOnly() {
        var anonymous = new Caller("c1", new Principal("anonymous", null, null, null));
        assertEquals(Map.of("conversationId", "c1"), anonymous.toolMeta());
    }

    @Test
    void aCallerWithNothingToSendIsEmpty() {
        assertTrue(new Caller(null, null).isEmpty());
        assertTrue(new Caller(" ", new Principal("anonymous", null, null, null)).isEmpty());
        assertTrue(new Caller(null, null).toolMeta().isEmpty());
    }
}
