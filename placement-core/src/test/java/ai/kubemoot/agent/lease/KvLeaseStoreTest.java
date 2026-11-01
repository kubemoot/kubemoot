package ai.kubemoot.agent.lease;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueOperation;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The KV mapping: a lost race is "not held", any other failure is raised, a deleted entry is not a lease. */
class KvLeaseStoreTest {

    private static final int WRONG_LAST_SEQUENCE = 10071;

    private final KeyValue kv = mock(KeyValue.class);
    private KvLeaseStore store;

    @BeforeEach
    void open() throws Exception {
        Connection connection = mock(Connection.class);
        when(connection.keyValue("bucket")).thenReturn(kv);
        store = new KvLeaseStore(connection, "bucket");
    }

    private static JetStreamApiException apiError(int code) {
        JetStreamApiException e = mock(JetStreamApiException.class);
        when(e.getApiErrorCode()).thenReturn(code);
        return e;
    }

    @Test
    void acquireCreatesTheKeyAndReturnsItsRevision() throws Exception {
        when(kv.create(eq("t1"), any())).thenReturn(7L);
        assertEquals(7L, store.acquire("t1", "pod-a"));
        verify(kv).create("t1", "pod-a".getBytes(StandardCharsets.UTF_8));
    }

    @Test
    void acquireOfAHeldKeyIsNotHeldButOtherFailuresAreRaised() throws Exception {
        var lost = apiError(WRONG_LAST_SEQUENCE);
        var broke = apiError(10008);
        when(kv.create(eq("held"), any())).thenThrow(lost);
        when(kv.create(eq("broken"), any())).thenThrow(broke);
        assertEquals(LeaseStore.NOT_HELD, store.acquire("held", "pod-a"));
        assertThrows(JetStreamApiException.class, () -> store.acquire("broken", "pod-a"));
    }

    @Test
    void renewIsACompareAndSetOnTheRevision() throws Exception {
        when(kv.update("t1", "pod-a".getBytes(StandardCharsets.UTF_8), 7L)).thenReturn(8L);
        var lost = apiError(WRONG_LAST_SEQUENCE);
        when(kv.update(eq("t1"), any(byte[].class), eq(3L))).thenThrow(lost);
        assertEquals(8L, store.renew("t1", "pod-a", 7L));
        assertEquals(LeaseStore.NOT_HELD, store.renew("t1", "pod-a", 3L));
    }

    @Test
    void releaseIgnoresALostRaceButRaisesOtherFailures() throws Exception {
        var lost = apiError(WRONG_LAST_SEQUENCE);
        var broke = apiError(10008);
        org.mockito.Mockito.doThrow(lost).when(kv).delete("t1", 1L);
        org.mockito.Mockito.doThrow(broke).when(kv).delete("t1", 2L);
        store.release("t1", 1L);
        assertThrows(JetStreamApiException.class, () -> store.release("t1", 2L));
        store.release("t1", 9L);
        verify(kv).delete(eq("t1"), eq(9L));
        verify(kv, org.mockito.Mockito.times(3)).delete(any(String.class), anyLong());
    }

    @Test
    void aKeyIsHeldOnlyWhileItsLatestEntryIsAPut() throws Exception {
        KeyValueEntry put = mock(KeyValueEntry.class);
        when(put.getOperation()).thenReturn(KeyValueOperation.PUT);
        KeyValueEntry deleted = mock(KeyValueEntry.class);
        when(deleted.getOperation()).thenReturn(KeyValueOperation.DELETE);
        when(kv.get("on")).thenReturn(put);
        when(kv.get("off")).thenReturn(deleted);
        assertTrue(store.isHeld("on"));
        assertFalse(store.isHeld("off"));
        assertFalse(store.isHeld("never"));
    }
}
