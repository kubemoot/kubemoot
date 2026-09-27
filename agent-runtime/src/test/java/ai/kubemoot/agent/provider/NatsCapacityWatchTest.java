package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueWatchOption;
import io.nats.client.api.KeyValueWatcher;
import io.nats.client.impl.NatsKeyValueWatchSubscription;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.io.IOException;
import java.time.Duration;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/** The KV-watch-backed capacity signal: watch setup, version bumps, and waiting for a change. */
class NatsCapacityWatchTest {

    private static NatsConnectionProvider providerWith(Connection conn) {
        var provider = mock(NatsConnectionProvider.class);
        when(provider.getConnection()).thenReturn(conn);
        return provider;
    }

    @Test
    void ensureWatching_watchesBothBucketsOnce_andEveryUpdateAdvancesTheVersion() throws Exception {
        var conn = mock(Connection.class);
        var stateKv = mock(KeyValue.class);
        var ticketKv = mock(KeyValue.class);
        when(conn.keyValue(ProviderSelector.STATE_BUCKET)).thenReturn(stateKv);
        when(conn.keyValue(TicketManager.TICKETS_BUCKET)).thenReturn(ticketKv);
        var watcher = ArgumentCaptor.forClass(KeyValueWatcher.class);
        when(stateKv.watchAll(watcher.capture(), any(KeyValueWatchOption[].class)))
                .thenReturn(mock(NatsKeyValueWatchSubscription.class));
        when(ticketKv.watchAll(any(KeyValueWatcher.class), any(KeyValueWatchOption[].class)))
                .thenReturn(mock(NatsKeyValueWatchSubscription.class));
        var watch = new NatsCapacityWatch(providerWith(conn));

        assertTrue(watch.ensureWatching());
        assertTrue(watch.ensureWatching());
        verify(conn, times(1)).keyValue(ProviderSelector.STATE_BUCKET);
        verify(conn, times(1)).keyValue(TicketManager.TICKETS_BUCKET);

        long before = watch.version();
        watcher.getValue().watch(mock(KeyValueEntry.class));
        assertEquals(before + 1, watch.version(), "a KV update is a capacity change");
    }

    @Test
    void ensureWatching_falseWithoutConnection() {
        assertFalse(new NatsCapacityWatch(providerWith(null)).ensureWatching());
    }

    @Test
    void ensureWatching_falseWhenABucketCannotBeWatched_andRetriesLater() throws Exception {
        var conn = mock(Connection.class);
        when(conn.keyValue(anyString())).thenThrow(new IOException("no bucket"));
        var watch = new NatsCapacityWatch(providerWith(conn));

        assertFalse(watch.ensureWatching());
        assertFalse(watch.ensureWatching());
        verify(conn, times(2)).keyValue(ProviderSelector.STATE_BUCKET);
    }

    @Test
    void awaitChange_returnsAtOnceWhenTheVersionAlreadyMoved() throws Exception {
        var watch = new NatsCapacityWatch(providerWith(null));
        long seen = watch.version();
        watch.nudge();
        assertTrue(watch.awaitChange(seen, Duration.ofSeconds(5)));
    }

    @Test
    void awaitChange_timesOutWithoutAChange() throws Exception {
        var watch = new NatsCapacityWatch(providerWith(null));
        assertFalse(watch.awaitChange(watch.version(), Duration.ofMillis(50)));
    }

    @Test
    void awaitChange_wakesOnANudgeFromAnotherThread() throws Exception {
        var watch = new NatsCapacityWatch(providerWith(null));
        long seen = watch.version();
        var nudger = new Thread(() -> {
            try {
                Thread.sleep(50);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
            watch.nudge();
        });
        nudger.start();
        assertTrue(watch.awaitChange(seen, Duration.ofSeconds(5)));
        nudger.join();
    }
}
