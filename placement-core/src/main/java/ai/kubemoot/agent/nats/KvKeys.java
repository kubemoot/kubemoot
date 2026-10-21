package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamInfoOptions;
import io.nats.client.api.Subject;
import org.slf4j.Logger;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Lists the keys of a NATS KV bucket from the backing stream's subject index
 * (stream info with a subjects filter) instead of {@code KeyValue.keys()}.
 *
 * <p>{@code keys()} creates a JetStream consumer for every call, named by jnats's
 * process-global NUID. That consumer can collide with another process's consumer of
 * the same name (the server answers "deliver policy can not be updated [10012]"),
 * and each call adds consumer churn. The subject index needs no consumer.</p>
 *
 * <p>A key whose last message is a delete or purge marker still has a subject, so a
 * listed key can read back as absent; callers read each key and skip absent ones.</p>
 */
public final class KvKeys {

    private static final long WARN_INTERVAL_MS = 60_000L;
    private static final Map<String, Long> lastWarnAt = new ConcurrentHashMap<>();

    private KvKeys() {}

    /** The keys of {@code bucket}, or an exception when NATS answers with an error. */
    public static List<String> list(Connection conn, String bucket) throws IOException, JetStreamApiException {
        String prefix = "$KV." + bucket + ".";
        var info = conn.jetStreamManagement()
                .getStreamInfo("KV_" + bucket, StreamInfoOptions.filterSubjects(prefix + ">"));
        List<Subject> subjects = info.getStreamState().getSubjects();
        List<String> keys = new ArrayList<>();
        if (subjects != null) {
            for (Subject s : subjects) {
                if (s.getName().startsWith(prefix)) {
                    keys.add(s.getName().substring(prefix.length()));
                }
            }
        }
        return keys;
    }

    /**
     * Logs a failed read of {@code bucket} at WARN with the NATS error, at most once
     * a minute per bucket and purpose, so a failing read is visible without flooding.
     */
    public static void warnReadFailure(Logger log, String bucket, String purpose, Exception e) {
        if (warningDue("read|" + bucket + "|" + purpose)) {
            log.warn("Reading NATS KV bucket {} for {} failed: {} (NATS answered with an error; not an empty bucket)",
                    bucket, purpose, e.toString());
        } else {
            log.debug("Reading NATS KV bucket {} for {} failed again: {}", bucket, purpose, e.getMessage());
        }
    }

    /**
     * Logs a failed write (put or delete) of {@code key} in {@code bucket} at WARN with
     * the key and the error, at most once a minute per bucket and key, so a write that
     * never lands (an illegal key, a missing bucket) is visible without flooding.
     */
    public static void warnWriteFailure(Logger log, String bucket, String key, Exception e) {
        if (warningDue("write|" + bucket + "|" + key)) {
            log.warn("Writing NATS KV key {} in bucket {} failed: {}", key, bucket, e.toString());
        } else {
            log.debug("Writing NATS KV key {} in bucket {} failed again: {}", key, bucket, e.getMessage());
        }
    }

    /**
     * True when no warning for {@code key} was logged within the last minute; records this one.
     * Holds one entry per key it has seen, so callers pass a bounded set of keys.
     */
    private static boolean warningDue(String key) {
        long now = System.currentTimeMillis();
        boolean[] due = {false};
        lastWarnAt.compute(key, (k, last) -> {
            due[0] = last == null || now - last >= WARN_INTERVAL_MS;
            return due[0] ? now : last;
        });
        return due[0];
    }

    // Visible for testing
    static void resetWarningsForTest() {
        lastWarnAt.clear();
    }
}
