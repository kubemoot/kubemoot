package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.api.ObjectInfo;
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
 * thread-scoped key schema, so the producer (spill in {@link DiscussionSubscriber})
 * and the GC reaper (eviction in {@link DiscussionOrchestrator}) agree byte-for-byte
 * on where an object lives. A drift between the spill key and the delete prefix would
 * silently leak artifacts, which is exactly the divergent-duplication class this
 * helper exists to prevent.
 */
final class DiscussionArtifacts {

    static final String BUCKET = "kubemoot_discussion_artifacts";

    private static final Logger log = LoggerFactory.getLogger(DiscussionArtifacts.class);

    private DiscussionArtifacts() {
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
                    e.getClass().getSimpleName(), e.getMessage());
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
                    scopePrefix, e.getClass().getSimpleName(), e.getMessage());
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
        var store = conn.objectStore(BUCKET);
        int deleted = 0;
        for (ObjectInfo info : store.getList()) {
            if (!shouldDelete.test(info)) {
                continue;
            }
            try {
                store.delete(info.getObjectName());
                deleted++;
            } catch (Exception e) {
                log.warn("GC: failed to delete {} ({}): {} - continuing",
                        info.getObjectName(), e.getClass().getSimpleName(), e.getMessage());
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
