package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.logging.LogContent;
import ai.kubemoot.agent.chat.CallArtifactStore;
import ai.kubemoot.agent.chat.CallHeaders;
import ai.kubemoot.agent.chat.HeaderValue;
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
import java.util.Collections;
import java.util.concurrent.atomic.AtomicInteger;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Writes a tooler's tool results to the discussion workspace, one NATS object per call, in the
 * bucket {@link DiscussionArtifacts#WORKSPACE_BUCKET} under the thread's key prefix. Objects carry the call's
 * description as NATS object headers, so they expire with the bucket's TTL and are deleted with the
 * thread's other objects by prefix ({@link DiscussionArtifacts#deleteThreadArtifacts}). Each object
 * is also announced on the artifact subject so a sandbox's materializer gets the file.
 */
@ApplicationScoped
public class NatsCallArtifactStore implements CallArtifactStore {

    private static final Logger log = LoggerFactory.getLogger(NatsCallArtifactStore.class);

    /** Call counters kept at once; the least recently used is dropped past this and found again from the store. */
    private static final int MAX_COUNTERS = 500;

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    /** The last call number given per {@code {thread prefix}{agent}/}, least recently used first out. */
    private final Map<String, AtomicInteger> counters = Collections.synchronizedMap(
            new LinkedHashMap<String, AtomicInteger>(16, 0.75f, true) {
                @Override
                protected boolean removeEldestEntry(Map.Entry<String, AtomicInteger> eldest) {
                    return size() > MAX_COUNTERS;
                }
            });
    private volatile boolean bucketEnsured;

    @Inject
    public NatsCallArtifactStore(NatsConnectionProvider natsProvider, ObjectMapper mapper) {
        this.natsProvider = natsProvider;
        this.mapper = mapper;
    }

    @Override
    public String threadPrefix(String threadId) {
        return DiscussionArtifacts.threadPrefix(natsProvider.scope(), threadId);
    }

    @Override
    public String reserveKey(String threadId, String agent, String tool) {
        CrewScope scope = natsProvider.scope();
        String agentPrefix = DiscussionArtifacts.threadPrefix(scope, threadId) + agent + "/";
        AtomicInteger counter;
        synchronized (counters) {
            counter = counters.get(agentPrefix);
            if (counter == null) {
                int highest = highestCallNumber(agentPrefix);
                if (highest < 0) {
                    return null;
                }
                counter = new AtomicInteger(highest);
                counters.put(agentPrefix, counter);
            }
        }
        return DiscussionArtifacts.callKey(scope, threadId, agent, counter.incrementAndGet(), tool);
    }

    /**
     * The highest call number already stored under {@code agentPrefix}, so numbering carries on after a
     * restart or after the counter was evicted instead of overwriting an earlier call. Zero when
     * nothing is stored or no connection exists yet; negative when a bucket that exists cannot be
     * listed, because numbering from 1 could overwrite an earlier call, so the call is not stored.
     */
    private int highestCallNumber(String agentPrefix) {
        try {
            Connection conn = natsProvider.getConnection();
            if (conn == null) {
                return 0;
            }
            ensureBucket(conn);
            int highest = 0;
            for (String bucket : DiscussionArtifacts.READ_BUCKETS) {
                highest = Math.max(highest, highestIn(conn, bucket, agentPrefix));
            }
            return highest;
        } catch (Exception e) {
            log.warn("Could not list stored calls under {} ({}): {} - this call is not stored",
                    agentPrefix, e.getClass().getSimpleName(), LogContent.reason(e));
            return -1;
        }
    }

    /** The highest call number under {@code agentPrefix} in one bucket; zero when that bucket does not exist. */
    private static int highestIn(Connection conn, String bucket, String agentPrefix) throws Exception {
        int highest = 0;
        try {
            for (var info : conn.objectStore(bucket).getList()) {
                if (!info.isDeleted() && info.getObjectName().startsWith(agentPrefix)) {
                    highest = Math.max(highest, DiscussionArtifacts.callNumberOf(info.getObjectName(), agentPrefix));
                }
            }
        } catch (Exception e) {
            if (!DiscussionArtifacts.isBucketMissing(e)) {
                throw e;
            }
        }
        return highest;
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
            conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET).put(meta, new ByteArrayInputStream(body));
            conn.publish(natsProvider.scope().artifactSubject(threadId),
                    mapper.writeValueAsBytes(Map.of("artifact", reference(agent, tool, key, body.length, headers))));
            log.info("Stored {} call result ({} bytes) as workspace artifact {}", tool, body.length, key);
            return true;
        } catch (Exception e) {
            log.warn("Workspace artifact write failed for {} ({}): {} - the contribution carries the output",
                    key, e.getClass().getSimpleName(), LogContent.reason(e));
            return false;
        }
    }

    private void ensureBucket(Connection conn) throws Exception {
        if (!bucketEnsured) {
            DiscussionArtifacts.ensureBucket(conn);
            bucketEnsured = true;
        }
    }

    /**
     * The headers as NATS headers. A value that is not printable ASCII (an argument with {@code é}, or a
     * line break in pretty-printed arguments) is encoded by {@link HeaderValue}, so it reads back as written.
     */
    static Headers natsHeaders(Map<String, String> values) {
        var headers = new Headers();
        values.forEach((k, v) -> headers.put(k, v == null ? "" : HeaderValue.encode(v)));
        return headers;
    }

    private static Map<String, Object> reference(String agent, String tool, String key, int bytes,
                                                 Map<String, String> headers) {
        var ref = new LinkedHashMap<String, Object>();
        ref.put("bucket", DiscussionArtifacts.WORKSPACE_BUCKET);
        ref.put("key", key);
        ref.put("bytes", bytes);
        ref.put("agent", agent);
        ref.put("tool", tool);
        ref.put("createdAt", Instant.now().toString());
        String type = headers.get(CallHeaders.CONTENT_TYPE);
        if (type != null) {
            ref.put("contentType", type);
        }
        String rows = headers.get(CallHeaders.ROWS);
        if (rows != null) {
            ref.put("rows", Long.parseLong(rows));
        }
        return ref;
    }
}
