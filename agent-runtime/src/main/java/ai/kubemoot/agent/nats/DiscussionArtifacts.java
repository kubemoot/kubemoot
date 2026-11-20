package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.logging.LogContent;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.ObjectInfo;
import io.nats.client.api.ObjectStoreConfiguration;
import io.nats.client.api.StorageType;
import java.time.Duration;
import java.time.Instant;
import java.util.Collection;
import java.util.List;
import java.util.function.Predicate;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Shared naming + lifecycle helpers for discussion artifacts in the NATS Object
 * Store. This is the single source of truth for the bucket name and the
 * thread-scoped key schema, so the producer ({@link NatsCallArtifactStore})
 * and the GC reaper (eviction in {@link DiscussionOrchestrator}) agree byte-for-byte
 * on where an object lives. A drift between the written key and the delete prefix would
 * silently leak artifacts, which is exactly the divergent-duplication class this
 * helper exists to prevent.
 */
final class DiscussionArtifacts {

    /** The workspace bucket: the one every writer stores into. */
    static final String WORKSPACE_BUCKET = "kubemoot_workspace";

    /**
     * The bucket the workspace was stored in before it had its name. Nothing writes to it; readers and
     * clean-up still look in it so a discussion that spans the move keeps its calls until the bucket's
     * TTL drains it.
     */
    static final String LEGACY_BUCKET = "kubemoot_discussion_artifacts";

    /** The buckets a reader looks in, in order: the workspace bucket, then the one it replaced. */
    static final List<String> READ_BUCKETS = List.of(WORKSPACE_BUCKET, LEGACY_BUCKET);

    private static final Logger log = LoggerFactory.getLogger(DiscussionArtifacts.class);

    /** JetStream API error code for "stream name already in use": the bucket already exists. */
    private static final int JSAPI_STREAM_NAME_IN_USE = 10058;

    /** How long an object lives: the bucket's TTL, which bounds anything thread-end clean-up misses. */
    static final Duration TTL = Duration.ofHours(48);

    private DiscussionArtifacts() {
    }

    /**
     * Create the workspace bucket, with its TTL, if it does not exist. Only a writer creates it: a
     * reader that created it would leave it without the TTL. create() is called unconditionally (no
     * check-then-create race); an "already in use" error from a concurrent or earlier create
     * is benign and ignored.
     */
    static void ensureBucket(Connection conn) throws Exception {
        try {
            conn.objectStoreManagement().create(ObjectStoreConfiguration.builder(WORKSPACE_BUCKET)
                    .ttl(TTL)
                    .storageType(StorageType.File)
                    .build());
        } catch (JetStreamApiException e) {
            if (e.getApiErrorCode() != JSAPI_STREAM_NAME_IN_USE) {
                throw e;
            }
        }
    }

    /** The store and the info of an object found in one of the {@link #READ_BUCKETS}. */
    record Held(ObjectStore store, ObjectInfo info) {
    }

    /** JetStream API error code for "stream not found": the bucket does not exist. */
    private static final int JSAPI_STREAM_NOT_FOUND = 10059;

    /**
     * True when the failure means the bucket does not exist (no writer has created it), at the top of
     * {@code e} or in its cause chain. Any other failure is a real one and is not hidden by this.
     */
    static boolean isBucketMissing(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof JetStreamApiException api && api.getApiErrorCode() == JSAPI_STREAM_NOT_FOUND) {
                return true;
            }
            String message = t.getMessage();
            if (message != null && message.contains("stream not found")) {
                return true;
            }
        }
        return false;
    }

    /**
     * The first {@link #READ_BUCKETS} bucket that holds a live object under {@code key}, with its info,
     * or null when none does. A bucket that does not exist yet is skipped; any other failure is thrown.
     */
    static Held storeHolding(Connection conn, String key) throws Exception {
        for (String bucket : READ_BUCKETS) {
            try {
                ObjectStore store = conn.objectStore(bucket);
                ObjectInfo info = store == null ? null : store.getInfo(key);
                if (info != null && !info.isDeleted()) {
                    return new Held(store, info);
                }
            } catch (Exception e) {
                if (!isBucketMissing(e)) {
                    throw e;
                }
                log.debug("{} does not exist yet", bucket);
            }
        }
        return null;
    }

    /** Thread-scoped key of one tool call's result: {@code {ns}/{crew}/{threadId}/{agent}/{nnn}-{tool}}. */
    static String callKey(CrewScope scope, String threadId, String agent, int callNumber, String tool) {
        return threadPrefix(scope, threadId) + agent + "/" + String.format("%03d", callNumber) + "-" + safe(tool);
    }

    /**
     * The call number of a key made by {@link #callKey}: the digits after {@code agentPrefix} and
     * before the first hyphen. Zero for any other key.
     */
    static int callNumberOf(String key, String agentPrefix) {
        String rest = key.substring(agentPrefix.length());
        int dash = rest.indexOf('-');
        if (dash <= 0 || rest.indexOf('/') >= 0) {
            return 0;
        }
        try {
            return Integer.parseInt(rest.substring(0, dash));
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    /** A tool name as a key segment: letters, digits, underscore, dot and hyphen; anything else becomes underscore. */
    static String safe(String tool) {
        return tool == null || tool.isEmpty() ? "tool" : tool.replaceAll("[^A-Za-z0-9_.-]", "_");
    }

    /** Thread-scoped object key: {@code {ns}/{crew}/{threadId}/{agent}/{signal}-{uuid}}. */
    static String key(CrewScope scope, String threadId, String agent, String signal, String uuid) {
        return threadPrefix(scope, threadId) + agent + "/" + signal + "-" + uuid;
    }

    /** Prefix that contains every artifact for one thread: {@code {ns}/{crew}/{threadId}/}. */
    static String threadPrefix(CrewScope scope, String threadId) {
        return scope.artifactKeyPrefix() + threadId + "/";
    }

    /**
     * Prefix-delete every live object under any of the given threads
     * ({@code {ns}/{crew}/{threadId}/}). The bucket is listed once for the whole batch
     * (cheaper than once per thread on each cleanup pass). Best-effort: any failure
     * is logged and swallowed because GC must never break the discussion lifecycle
     * path. Returns the number of objects deleted (logged, never silently dropped).
     */
    static int deleteThreadArtifacts(Connection conn, CrewScope scope, Collection<String> threadIds) {
        if (conn == null || threadIds.isEmpty()) {
            return 0;
        }
        List<String> prefixes = threadIds.stream().map(t -> threadPrefix(scope, t)).toList();
        try {
            int deleted = filterAndDelete(conn, info ->
                    !info.isDeleted() && prefixes.stream().anyMatch(info.getObjectName()::startsWith));
            if (deleted > 0) {
                log.info("GC: deleted {} artifact(s) for {} closed thread(s)", deleted, threadIds.size());
            }
            return deleted;
        } catch (Exception e) {
            log.warn("GC: artifact prefix-delete failed ({}): {} - TTL backstop will reap",
                    e.getClass().getSimpleName(), LogContent.reason(e));
            return 0;
        }
    }

    /**
     * Orphan reaper (GC Layer 3): repairs leaks the lifecycle delete missed (a failed
     * prefix-delete, or a coordinator crash that left a thread's artifacts behind).
     * Scoped to one namespace and crew: only objects under {@code {ns}/{crew}/} are
     * considered, so one crew's reaper never touches another crew's artifacts in the
     * shared bucket, including a same-named crew in another namespace. An
     * object is reaped only when BOTH its thread is no longer live ({@code threadLive}
     * is the coordinator's in-memory thread set) AND it is older than {@code grace} -
     * the generous grace window means a live discussion's artifacts are never at risk.
     * The 48h bucket TTL still bounds anything this misses. Best-effort; failures are
     * logged and swallowed. {@code now} is injected for deterministic testing.
     */
    static int reapOrphans(Connection conn, CrewScope scope, Predicate<String> threadLive,
                           Duration grace, Instant now) {
        if (conn == null) {
            return 0;
        }
        String scopePrefix = scope.artifactKeyPrefix();
        Instant cutoff = now.minus(grace);
        try {
            int reaped = filterAndDelete(conn, info ->
                    isReapableOrphan(info, scopePrefix, threadLive, cutoff));
            if (reaped > 0) {
                log.info("GC: reaped {} orphan artifact(s) under {} (no live thread, age > grace)",
                        reaped, scopePrefix);
            }
            return reaped;
        } catch (Exception e) {
            log.warn("GC: orphan reap failed under {} ({}): {}",
                    scopePrefix, e.getClass().getSimpleName(), LogContent.reason(e));
            return 0;
        }
    }

    /**
     * Shared delete pipeline for both the lifecycle delete and the orphan reaper: list
     * the bucket once and delete every object the {@code shouldDelete} predicate accepts.
     * A {@code getList()} failure propagates to the caller (which logs + returns 0), but
     * a single object's {@code delete()} failure is logged and skipped so one corrupt or
     * locked object never aborts the rest of the pass - best-effort per object, not
     * per pass. Returns the number actually deleted.
     */
    private static int filterAndDelete(Connection conn, Predicate<ObjectInfo> shouldDelete)
            throws Exception {
        int deleted = 0;
        for (String bucket : READ_BUCKETS) {
            deleted += filterAndDelete(conn, bucket, shouldDelete);
        }
        return deleted;
    }

    /** One bucket's share of {@link #filterAndDelete(Connection, Predicate)}; the workspace bucket may not exist yet. */
    private static int filterAndDelete(Connection conn, String bucket, Predicate<ObjectInfo> shouldDelete)
            throws Exception {
        // The client binds a bucket lazily: a missing bucket can surface from objectStore() or from the
        // first call on it (getList), so both are inside the try.
        ObjectStore store;
        List<ObjectInfo> infos;
        try {
            store = conn.objectStore(bucket);
            infos = store == null ? List.of() : store.getList();
        } catch (Exception e) {
            if (isBucketMissing(e)) {
                return 0; // no writer has created it
            }
            throw e;
        }
        int deleted = 0;
        for (ObjectInfo info : infos) {
            if (!shouldDelete.test(info)) {
                continue;
            }
            try {
                store.delete(info.getObjectName());
                deleted++;
            } catch (Exception e) {
                log.warn("GC: failed to delete {} ({}): {} - continuing",
                        info.getObjectName(), e.getClass().getSimpleName(), LogContent.reason(e));
            }
        }
        return deleted;
    }

    /** An object is a reapable orphan when it is live, ours, thread-dead, and past grace. */
    private static boolean isReapableOrphan(ObjectInfo info, String scopePrefix,
                                            Predicate<String> threadLive, Instant cutoff) {
        if (info.isDeleted()) {
            return false;
        }
        String name = info.getObjectName();
        if (!name.startsWith(scopePrefix)) {
            return false; // another crew's or namespace's artifact - never touch it
        }
        String threadId = threadIdOf(name, scopePrefix);
        if (threadId == null || threadLive.test(threadId)) {
            return false; // malformed key, or the thread is still live
        }
        var modified = info.getModified();
        return modified != null && modified.toInstant().isBefore(cutoff); // older than grace
    }

    /**
     * Extract the threadId segment from a key {@code {ns}/{crew}/{threadId}/...}, where
     * {@code prefix} is {@link CrewScope#artifactKeyPrefix()}; null if malformed.
     */
    static String threadIdOf(String key, String prefix) {
        if (!key.startsWith(prefix)) {
            return null;
        }
        int start = prefix.length();
        int slash = key.indexOf('/', start);
        return slash < 0 ? null : key.substring(start, slash);
    }
}
