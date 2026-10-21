package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.nats.KvKeys;
import ai.kubemoot.agent.nats.NatsLink;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

/**
 * {@link TicketStore} over the NATS KV bucket {@value #BUCKET}, read and written by every process
 * that places calls. Each claim is a KV create whose revision orders concurrent claims; the bucket
 * TTL is the safety net that removes a ticket whose holder disappeared.
 */
public final class KvTicketStore implements TicketStore {

    private static final Logger log = LoggerFactory.getLogger(KvTicketStore.class);

    /** The tickets bucket; the operator provisions it. */
    public static final String BUCKET = "kubemoot_server_tickets";

    private final NatsLink nats;
    private final ObjectMapper mapper;
    /** The bucket handle for {@link #handleConnection}; opening one costs a JetStream round trip. */
    private volatile KeyValue handle;
    private volatile Connection handleConnection;

    public KvTicketStore(NatsLink nats, ObjectMapper mapper) {
        this.nats = nats;
        this.mapper = mapper;
    }

    @Override
    public boolean available() {
        return openBucket() != null;
    }

    @Override
    public long create(String key, JsonNode doc) {
        KeyValue kv = openBucket();
        if (kv == null) {
            throw new IllegalStateException("tickets bucket unavailable");
        }
        try {
            return kv.create(key, mapper.writeValueAsBytes(doc));
        } catch (Exception e) {
            throw new IllegalStateException("create " + key + " failed: " + e.getMessage(), e);
        }
    }

    @Override
    public void put(String key, JsonNode doc) {
        KeyValue kv = openBucket();
        if (kv == null) {
            return;
        }
        try {
            kv.put(key, mapper.writeValueAsBytes(doc));
        } catch (Exception e) {
            KvKeys.warnWriteFailure(log, BUCKET, key, e);
        }
    }

    @Override
    public void delete(String key) {
        KeyValue kv = openBucket();
        if (kv == null) {
            return;
        }
        try {
            kv.delete(key);
        } catch (Exception e) {
            // The bucket TTL is the safety net for a delete that did not land.
            log.debug("Ticket delete failed for {} (TTL safety net applies): {}", key, e.getMessage());
        }
    }

    @Override
    public List<Entry> scan(String prefix) {
        List<Entry> out = new ArrayList<>();
        KeyValue kv = openBucket();
        if (kv == null) {
            KvKeys.warnReadFailure(log, BUCKET, "tickets " + prefix, new IOException("tickets bucket unavailable"));
            return out;
        }
        try {
            for (String key : KvKeys.list(nats.getConnection(), BUCKET)) {
                if (key.startsWith(prefix)) {
                    read(kv, key).ifPresent(out::add);
                }
            }
        } catch (Exception e) {
            KvKeys.warnReadFailure(log, BUCKET, "tickets " + prefix, e);
        }
        return out;
    }

    /**
     * One entry, parsed with {@code readTree}, never {@code readValue} on a record (record
     * reflection fails silently in a native image). A key whose last message is a delete marker
     * reads back absent and is skipped, as is an unreadable one.
     */
    private Optional<Entry> read(KeyValue kv, String key) {
        try {
            KeyValueEntry entry = kv.get(key);
            if (entry == null || entry.getValue() == null) {
                return Optional.empty();
            }
            return Optional.of(new Entry(key, mapper.readTree(entry.getValue()), entry.getRevision()));
        } catch (Exception e) {
            log.debug("Skipping unreadable ticket {}: {}", key, e.getMessage());
            return Optional.empty();
        }
    }

    /** The tickets bucket, or null when NATS or the bucket is unavailable. */
    private KeyValue openBucket() {
        if (nats == null || !nats.isAvailable()) {
            return null;
        }
        Connection conn = nats.getConnection();
        if (conn == null) {
            return null;
        }
        KeyValue cached = handle;
        if (cached != null && handleConnection == conn) {
            return cached;
        }
        try {
            KeyValue opened = conn.keyValue(BUCKET);
            handleConnection = conn;
            handle = opened;
            return opened;
        } catch (Exception e) {
            log.debug("Tickets bucket {} not available: {}", BUCKET, e.getMessage());
            return null;
        }
    }
}
