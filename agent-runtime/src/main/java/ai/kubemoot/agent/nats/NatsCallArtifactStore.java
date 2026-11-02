package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallArtifactStore;
import ai.kubemoot.agent.chat.CallHeaders;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.api.ObjectMeta;
import io.nats.client.impl.Headers;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import java.io.ByteArrayInputStream;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Writes a tooler's tool results to the discussion workspace, one NATS object per call, in the
 * bucket {@link DiscussionArtifacts#BUCKET} under the thread's key prefix. Objects carry the call's
 * description as NATS object headers, so they expire with the bucket's TTL and are deleted with the
 * thread's other objects by prefix ({@link DiscussionArtifacts#deleteThreadArtifacts}). Each object
 * is also announced on the artifact subject so a sandbox's materializer gets the file.
 */
@ApplicationScoped
public class NatsCallArtifactStore implements CallArtifactStore {

    private static final Logger log = LoggerFactory.getLogger(NatsCallArtifactStore.class);

    /** Call counters kept at once; cleared wholesale past this, like the runtime's other thread caches. */
    private static final int MAX_COUNTERS = 500;
    private static final String CONTENT_TYPE_JSON = "application/json";

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    private final Map<String, AtomicInteger> counters = new ConcurrentHashMap<>();
    private volatile boolean bucketEnsured;

    @Inject
    public NatsCallArtifactStore(NatsConnectionProvider natsProvider, ObjectMapper mapper) {
        this.natsProvider = natsProvider;
        this.mapper = mapper;
    }

    @Override
    public String reserveKey(String threadId, String agent, String tool) {
        CrewScope scope = natsProvider.scope();
        if (counters.size() > MAX_COUNTERS) {
            counters.clear();
        }
        int n = counters.computeIfAbsent(DiscussionArtifacts.threadPrefix(scope, threadId) + agent,
                k -> new AtomicInteger()).incrementAndGet();
        return DiscussionArtifacts.callKey(scope, threadId, agent, n, tool);
    }

    @Override
    public boolean put(String threadId, String agent, String tool, String key, byte[] body,
                       Map<String, String> headers) {
        try {
            Connection conn = natsProvider.getConnection();
            if (conn == null) {
                return false;
            }
            ensureBucket(conn);
            var meta = ObjectMeta.builder(key).description(tool + " result").headers(natsHeaders(headers)).build();
            conn.objectStore(DiscussionArtifacts.BUCKET).put(meta, new ByteArrayInputStream(body));
            conn.publish(natsProvider.scope().artifactSubject(threadId),
                    mapper.writeValueAsBytes(Map.of("artifact", reference(agent, tool, key, body.length, headers))));
            log.info("Stored {} call result ({} bytes) as workspace artifact {}", tool, body.length, key);
            return true;
        } catch (Exception e) {
            log.warn("Workspace artifact write failed for {} ({}): {} - the contribution carries the output",
                    key, e.getClass().getSimpleName(), e.getMessage());
            return false;
        }
    }

    private void ensureBucket(Connection conn) throws Exception {
        if (!bucketEnsured) {
            DiscussionArtifacts.ensureBucket(conn);
            bucketEnsured = true;
        }
    }

    /** The headers as NATS headers; a line break in a value would end the header, so it becomes a space. */
    static Headers natsHeaders(Map<String, String> values) {
        var headers = new Headers();
        values.forEach((k, v) -> headers.put(k, v == null ? "" : v.replace('\r', ' ').replace('\n', ' ')));
        return headers;
    }

    private static Map<String, Object> reference(String agent, String tool, String key, int bytes,
                                                 Map<String, String> headers) {
        var ref = new LinkedHashMap<String, Object>();
        ref.put("bucket", DiscussionArtifacts.BUCKET);
        ref.put("key", key);
        ref.put("bytes", bytes);
        ref.put("agent", agent);
        ref.put("tool", tool);
        ref.put("createdAt", Instant.now().toString());
        if (CallHeaders.FORMAT_JSON.equals(headers.get(CallHeaders.FORMAT))) {
            ref.put("contentType", CONTENT_TYPE_JSON);
        }
        String rows = headers.get(CallHeaders.ROWS);
        if (rows != null) {
            ref.put("rows", Long.parseLong(rows));
        }
        return ref;
    }
}
