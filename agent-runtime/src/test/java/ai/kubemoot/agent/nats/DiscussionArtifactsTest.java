package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

/**
 * Tests for the shared artifact naming + GC helper. Plain JUnit 5 (not @QuarkusTest)
 * so CI needs no NATS. The key/prefix tests pin the schema that the spill (producer)
 * and the reaper (GC) both depend on; the delete tests cover the lifecycle prefix-delete.
 */
class DiscussionArtifactsTest {

    @Test
    void key_format() {
        assertEquals("homelab-pilot/t1/k8s-config/agree-uuid123",
                DiscussionArtifacts.key("homelab-pilot", "t1", "k8s-config", "agree", "uuid123"));
    }

    @Test
    void threadPrefix_isKeyPrefix() {
        String prefix = DiscussionArtifacts.threadPrefix("homelab-pilot", "t1");
        assertEquals("homelab-pilot/t1/", prefix);
        // The spill key for the same crew+thread must start with the delete prefix,
        // otherwise GC would never match the objects it is meant to reap.
        assertTrue(DiscussionArtifacts.key("homelab-pilot", "t1", "k8s-config", "agree", "u")
                .startsWith(prefix));
    }

    @Test
    void crewOrDefault_fallsBackOnEmptyOrMissing() {
        assertEquals("homelab-pilot", DiscussionArtifacts.crewOrDefault(Optional.of("homelab-pilot")));
        assertEquals("nocrew", DiscussionArtifacts.crewOrDefault(Optional.of("")));
        assertEquals("nocrew", DiscussionArtifacts.crewOrDefault(Optional.empty()));
    }

    @Test
    void deleteThreadArtifacts_emptyInput_noStoreAccess() throws Exception {
        var conn = mock(Connection.class);
        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(conn, "homelab-pilot", List.of()));
        verifyNoInteractions(conn);
    }

    @Test
    void deleteThreadArtifacts_nullConn_returnsZeroWithoutNpe() {
        // The scheduler can fire before NATS is connected; a null connection must be
        // a clean no-op, not an NPE that the catch block swallows into a misleading log.
        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(null, "homelab-pilot", List.of("t1")));
    }

    @Test
    void deleteThreadArtifacts_deletesOnlyMatchingLiveObjects() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        // Build the ObjectInfo mocks before the getList() stub: each does its own
        // stubbing, which would otherwise nest inside an unfinished when(getList()).
        var match1 = objectInfo("homelab-pilot/t1/k8s-config/agree-a", false);
        var match2 = objectInfo("homelab-pilot/t1/net/agree-b", false);       // other agent, same thread
        var otherThread = objectInfo("homelab-pilot/t2/k8s-config/agree-c", false);
        var alreadyDeleted = objectInfo("homelab-pilot/t1/gone/agree-d", true); // matches prefix but deleted
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(match1, match2, otherThread, alreadyDeleted));

        int deleted = DiscussionArtifacts.deleteThreadArtifacts(conn, "homelab-pilot", List.of("t1"));

        assertEquals(2, deleted);
        verify(store).delete("homelab-pilot/t1/k8s-config/agree-a");
        verify(store).delete("homelab-pilot/t1/net/agree-b");
        verify(store, never()).delete("homelab-pilot/t2/k8s-config/agree-c");
        verify(store, never()).delete("homelab-pilot/t1/gone/agree-d");
    }

    @Test
    void deleteThreadArtifacts_multipleThreadsListedOnce() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var o1 = objectInfo("homelab-pilot/t1/a/agree-1", false);
        var o2 = objectInfo("homelab-pilot/t2/a/agree-2", false);
        var o3 = objectInfo("homelab-pilot/t3/a/agree-3", false);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(o1, o2, o3));

        int deleted = DiscussionArtifacts.deleteThreadArtifacts(conn, "homelab-pilot", List.of("t1", "t2"));

        assertEquals(2, deleted);
        verify(store, times(1)).getList(); // one list per batch, not per thread
        verify(store).delete("homelab-pilot/t1/a/agree-1");
        verify(store).delete("homelab-pilot/t2/a/agree-2");
        verify(store, never()).delete("homelab-pilot/t3/a/agree-3");
    }

    @Test
    void deleteThreadArtifacts_swallowsFailure() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenThrow(new RuntimeException("nats down"));
        // GC must never break the lifecycle path; failure returns 0, no exception.
        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(conn, "homelab-pilot", List.of("t1")));
    }

    // --- orphan reaper (GC Layer 3) ---

    private static final Instant NOW = Instant.parse("2026-06-25T12:00:00Z");
    private static final Duration GRACE = Duration.ofHours(2);

    @Test
    void threadIdOf_parsesThreadSegment() {
        assertEquals("t1", DiscussionArtifacts.threadIdOf("homelab-pilot/t1/agent/agree-u", "homelab-pilot"));
        assertNull(DiscussionArtifacts.threadIdOf("other-crew/t1/agent/agree-u", "homelab-pilot"),
                "a different crew's key must not parse under our crew");
        assertNull(DiscussionArtifacts.threadIdOf("homelab-pilot/no-more-slashes", "homelab-pilot"),
                "a key with no thread-terminating slash is malformed");
    }

    @Test
    void reapOrphans_reapsDeadThreadPastGrace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var orphan = objectInfo("homelab-pilot/dead/agent/agree-a", false, NOW.minus(Duration.ofHours(3)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(orphan));

        int reaped = DiscussionArtifacts.reapOrphans(conn, "homelab-pilot", t -> false, GRACE, NOW);

        assertEquals(1, reaped);
        verify(store).delete("homelab-pilot/dead/agent/agree-a");
    }

    @Test
    void reapOrphans_keepsLiveThreadEvenPastGrace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var live = objectInfo("homelab-pilot/t-live/agent/agree-a", false, NOW.minus(Duration.ofHours(5)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(live));

        // A live thread's artifacts are never reaped, no matter how old - this is the
        // guard that makes the reaper safe to run during a measurement.
        int reaped = DiscussionArtifacts.reapOrphans(conn, "homelab-pilot", "t-live"::equals, GRACE, NOW);

        assertEquals(0, reaped);
        verify(store, never()).delete(anyString());
    }

    @Test
    void reapOrphans_keepsRecentOrphanWithinGrace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var recent = objectInfo("homelab-pilot/dead/agent/agree-a", false, NOW.minus(Duration.ofMinutes(30)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(recent));

        int reaped = DiscussionArtifacts.reapOrphans(conn, "homelab-pilot", t -> false, GRACE, NOW);

        assertEquals(0, reaped, "within the grace window even a dead-thread object is left alone");
        verify(store, never()).delete(anyString());
    }

    @Test
    void reapOrphans_ignoresOtherCrewDeletedAndUnknownAge() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var otherCrew = objectInfo("other/dead/agent/agree-a", false, NOW.minus(Duration.ofHours(3)));
        var alreadyDeleted = objectInfo("homelab-pilot/dead/agent/agree-b", true, NOW.minus(Duration.ofHours(3)));
        var unknownAge = objectInfo("homelab-pilot/dead/agent/agree-c", false, null);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(otherCrew, alreadyDeleted, unknownAge));

        int reaped = DiscussionArtifacts.reapOrphans(conn, "homelab-pilot", t -> false, GRACE, NOW);

        assertEquals(0, reaped);
        verify(store, never()).delete(anyString());
    }

    @Test
    void reapOrphans_nullConn_returnsZero() {
        assertEquals(0, DiscussionArtifacts.reapOrphans(null, "homelab-pilot", t -> false, GRACE, NOW));
    }

    @Test
    void reapOrphans_perObjectDeleteFailure_continuesToNext() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var bad = objectInfo("homelab-pilot/dead/agent/agree-bad", false, NOW.minus(Duration.ofHours(3)));
        var good = objectInfo("homelab-pilot/dead/agent/agree-good", false, NOW.minus(Duration.ofHours(3)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(bad, good));
        when(store.delete("homelab-pilot/dead/agent/agree-bad")).thenThrow(new RuntimeException("locked"));

        // One failing delete must not abort the rest of the pass (best-effort per object).
        int reaped = DiscussionArtifacts.reapOrphans(conn, "homelab-pilot", t -> false, GRACE, NOW);

        assertEquals(1, reaped);
        verify(store).delete("homelab-pilot/dead/agent/agree-good");
    }

    @Test
    void reapOrphans_swallowsFailure() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenThrow(new RuntimeException("nats down"));
        assertEquals(0, DiscussionArtifacts.reapOrphans(conn, "homelab-pilot", t -> false, GRACE, NOW));
    }

    private static ObjectInfo objectInfo(String name, boolean deleted) {
        var info = mock(ObjectInfo.class);
        when(info.getObjectName()).thenReturn(name);
        when(info.isDeleted()).thenReturn(deleted);
        return info;
    }

    private static ObjectInfo objectInfo(String name, boolean deleted, Instant modified) {
        var info = mock(ObjectInfo.class);
        when(info.getObjectName()).thenReturn(name);
        when(info.isDeleted()).thenReturn(deleted);
        when(info.getModified()).thenReturn(modified == null ? null : modified.atZone(ZoneOffset.UTC));
        return info;
    }
}
