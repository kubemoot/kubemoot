package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.support.Validator;

import java.util.ArrayList;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/**
 * A thread-safe in-memory NATS KV bucket (keys, get, create, put, delete) behind Mockito mocks.
 * Every write gets the next revision, as a JetStream stream sequence does. Keys are checked
 * with jnats's own validator, so a key the real client rejects (such as one holding ':')
 * fails here with the same IllegalArgumentException.
 */
public final class InMemoryKv {

    public final Map<String, byte[]> data = new ConcurrentHashMap<>();
    public final KeyValue kv = mock(KeyValue.class);
    private final Map<String, Long> revisions = new ConcurrentHashMap<>();
    private final AtomicLong sequence = new AtomicLong();

    public InMemoryKv() {
        try {
            when(kv.keys()).thenAnswer(inv -> new ArrayList<>(data.keySet()));
            when(kv.get(anyString())).thenAnswer(inv -> entry(inv.getArgument(0)));
            when(kv.create(anyString(), any(byte[].class))).thenAnswer(inv -> write(inv.getArgument(0), inv.getArgument(1), true));
            when(kv.put(anyString(), any(byte[].class))).thenAnswer(inv -> write(inv.getArgument(0), inv.getArgument(1), false));
            doAnswer(inv -> data.remove(validKey(inv.getArgument(0)))).when(kv).delete(anyString());
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }

    private io.nats.client.api.StreamInfo streamInfo(String bucket) {
        var state = mock(io.nats.client.api.StreamState.class);
        var subjects = new ArrayList<io.nats.client.api.Subject>();
        data.keySet().forEach(k -> subjects.add(new io.nats.client.api.Subject("$KV." + bucket + "." + k, 1)));
        when(state.getSubjects()).thenReturn(subjects);
        var info = mock(io.nats.client.api.StreamInfo.class);
        when(info.getStreamState()).thenReturn(state);
        return info;
    }

    /** Stores the value under the next revision; a create of an existing key fails. */
    private synchronized long write(String key, byte[] value, boolean create) {
        validKey(key);
        if (create && data.containsKey(key)) {
            throw new IllegalStateException("key exists");
        }
        long revision = sequence.incrementAndGet();
        revisions.put(key, revision);
        data.put(key, value);
        return revision;
    }

    private KeyValueEntry entry(String key) {
        byte[] value = data.get(validKey(key));
        if (value == null) {
            return null;
        }
        KeyValueEntry e = mock(KeyValueEntry.class);
        when(e.getValue()).thenReturn(value);
        when(e.getRevision()).thenReturn(revisions.getOrDefault(key, 0L));
        return e;
    }

    /** {@code key}, or the IllegalArgumentException jnats throws for a key the server cannot hold. */
    private static String validKey(String key) {
        Validator.validateNonWildcardKvKeyRequired(key);
        return key;
    }

    /**
     * A connection provider whose connection serves this bucket under {@code name},
     * with the backing stream's subject index listing the current keys.
     */
    public NatsConnectionProvider provider(String name) {
        try {
            var conn = mock(Connection.class);
            when(conn.keyValue(name)).thenReturn(kv);
            var jsm = mock(io.nats.client.JetStreamManagement.class);
            when(conn.jetStreamManagement()).thenReturn(jsm);
            when(jsm.getStreamInfo(eq("KV_" + name), any(io.nats.client.api.StreamInfoOptions.class)))
                    .thenAnswer(inv -> streamInfo(name));
            var provider = mock(NatsConnectionProvider.class);
            when(provider.isAvailable()).thenReturn(true);
            when(provider.getConnection()).thenReturn(conn);
            return provider;
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }
}
