package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.ArrayDeque;
import java.util.Deque;
import java.util.concurrent.atomic.AtomicInteger;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** Scope resolution in the shared NATS server; no NATS server is contacted. */
class NatsConnectionProviderTest {

    private static AgentProperties props(String natsUrl, String namespace, String crew) {
        var props = mock(AgentProperties.class);
        var nats = mock(AgentProperties.Nats.class);
        when(nats.url()).thenReturn(Optional.ofNullable(natsUrl));
        when(props.nats()).thenReturn(nats);
        when(props.agentName()).thenReturn("k8s-config");
        when(props.namespace()).thenReturn(Optional.ofNullable(namespace));
        when(props.crew()).thenReturn(Optional.ofNullable(crew));
        return props;
    }

    @Test
    void configured_scopeCarriesNamespaceAndCrew() {
        var server = new NatsConnectionProvider(props("nats://nats:4222", "team-a", "homelab-pilot"));
        assertEquals(CrewScope.of("team-a", "homelab-pilot"), server.scope());
    }

    @Test
    void notConfigured_hasNoScope() {
        var server = new NatsConnectionProvider(props(null, "team-a", "homelab-pilot"));
        assertFalse(server.isConfigured());
        assertThrows(IllegalStateException.class, server::scope);
    }

    private static Connection conn(Connection.Status status) {
        var c = mock(Connection.class);
        when(c.getStatus()).thenReturn(status);
        return c;
    }

    /** A provider whose connector hands out the given connections in order and counts calls. */
    private static NatsConnectionProvider provider(AtomicInteger calls, Deque<Object> results) {
        return new NatsConnectionProvider(props("nats://nats:4222", "ns", "crew"), options -> {
            calls.incrementAndGet();
            var next = results.poll();
            if (next instanceof Exception e) {
                throw e;
            }
            return (Connection) next;
        }, Duration.ofMillis(1));
    }

    private static Deque<Object> queue(Object... items) {
        return new ArrayDeque<>(java.util.List.of(items));
    }

    @Test
    void connected_isReused() {
        var calls = new AtomicInteger();
        var p = provider(calls, queue(conn(Connection.Status.CONNECTED)));
        var first = p.getConnection();
        assertSame(first, p.getConnection());
        assertEquals(1, calls.get());
    }

    @Test
    void reconnecting_isReusedNotReplaced() {
        var calls = new AtomicInteger();
        var first = conn(Connection.Status.CONNECTED);
        var p = provider(calls, queue(first));
        assertSame(first, p.getConnection());
        when(first.getStatus()).thenReturn(Connection.Status.RECONNECTING);
        assertSame(first, p.getConnection());
        assertEquals(1, calls.get());
    }

    @Test
    void disconnected_isReusedNotReplaced() {
        var calls = new AtomicInteger();
        var first = conn(Connection.Status.CONNECTED);
        var p = provider(calls, queue(first));
        p.getConnection();
        when(first.getStatus()).thenReturn(Connection.Status.DISCONNECTED);
        assertSame(first, p.getConnection());
        assertEquals(1, calls.get());
    }

    @Test
    void closed_isReplacedAndOldOneClosed() throws Exception {
        var calls = new AtomicInteger();
        var first = conn(Connection.Status.CONNECTED);
        var second = conn(Connection.Status.CONNECTED);
        var p = provider(calls, queue(first, second));
        p.getConnection();
        when(first.getStatus()).thenReturn(Connection.Status.CLOSED);
        assertSame(second, p.getConnection());
        verify(first).close();
        verify(second, never()).close();
        assertEquals(2, calls.get());
    }

    @Test
    void absent_connectsAfterRetries() {
        var calls = new AtomicInteger();
        var ok = conn(Connection.Status.CONNECTED);
        var p = provider(calls, queue(new java.io.IOException("down"), new java.io.IOException("down"), ok));
        assertSame(ok, p.getConnection());
        assertEquals(3, calls.get());
    }

    @Test
    void failedConnect_backsOffWithoutMoreAttempts() {
        var calls = new AtomicInteger();
        var p = provider(calls, queue(new java.io.IOException("1"), new java.io.IOException("2"),
                new java.io.IOException("3"), new java.io.IOException("4"), new java.io.IOException("5")));
        assertNull(p.getConnection());
        assertEquals(5, calls.get());
        assertNull(p.getConnection());
        assertEquals(5, calls.get());
    }

    @Test
    void notConfigured_neverConnects() {
        var calls = new AtomicInteger();
        var p = new NatsConnectionProvider(props(null, "ns", "crew"), o -> {
            calls.incrementAndGet();
            return null;
        }, Duration.ofMillis(1));
        assertNull(p.getConnection());
        assertEquals(0, calls.get());
    }
}
