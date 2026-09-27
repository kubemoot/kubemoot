package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;

import java.util.ArrayList;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/** A thread-safe in-memory NATS KV bucket (keys, get, create, put, delete) behind Mockito mocks. */
public final class InMemoryKv {

    public final Map<String, byte[]> data = new ConcurrentHashMap<>();
    public final KeyValue kv = mock(KeyValue.class);

    public InMemoryKv() {
        try {
            when(kv.keys()).thenAnswer(inv -> new ArrayList<>(data.keySet()));
            when(kv.get(anyString())).thenAnswer(inv -> entry(data.get((String) inv.getArgument(0))));
            when(kv.create(anyString(), any(byte[].class))).thenAnswer(inv -> {
                if (data.putIfAbsent(inv.getArgument(0), inv.getArgument(1)) != null) {
                    throw new IllegalStateException("key exists");
                }
                return 1L;
            });
            when(kv.put(anyString(), any(byte[].class))).thenAnswer(inv -> {
                data.put(inv.getArgument(0), inv.getArgument(1));
                return 1L;
            });
            doAnswer(inv -> data.remove((String) inv.getArgument(0))).when(kv).delete(anyString());
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

    private static KeyValueEntry entry(byte[] value) {
        if (value == null) {
            return null;
        }
        KeyValueEntry e = mock(KeyValueEntry.class);
        when(e.getValue()).thenReturn(value);
        return e;
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
