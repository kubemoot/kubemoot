package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.List;
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

    private static final CrewScope SCOPE = CrewScope.of("ns-a", "homelab-pilot");
    private static final String PREFIX = SCOPE.artifactKeyPrefix();

    @Test
    void key_format() {
        assertEquals("ns-a/homelab-pilot/t1/k8s-config/agree-uuid123",
                DiscussionArtifacts.key(SCOPE, "t1", "k8s-config", "agree", "uuid123"));
    }

    @Test
    void callKey_format_numberedAndSafe() {
        assertEquals("ns-a/homelab-pilot/t1/k8s-workloads/007-pods_list",
                DiscussionArtifacts.callKey(SCOPE, "t1", "k8s-workloads", 7, "pods_list"));
        assertEquals("ns-a/homelab-pilot/t1/a/1000-weird_name_x",
                DiscussionArtifacts.callKey(SCOPE, "t1", "a", 1000, "weird/name x"));
        assertEquals("tool", DiscussionArtifacts.safe(""));
    }

    @Test
    void deleteThreadArtifacts_deletesPerCallObjectsByThePrefix() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        String call1 = DiscussionArtifacts.callKey(SCOPE, "t1", "k8s-workloads", 1, "pods_list");
        String call2 = DiscussionArtifacts.callKey(SCOPE, "t1", "k8s-workloads", 2, "events_list");
        String otherThread = DiscussionArtifacts.callKey(SCOPE, "t2", "k8s-workloads", 1, "pods_list");
        assertTrue(call1.startsWith(DiscussionArtifacts.threadPrefix(SCOPE, "t1")));
        var o1 = objectInfo(call1, false);
        var o2 = objectInfo(call2, false);
        var o3 = objectInfo(otherThread, false);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(o1, o2, o3));

        assertEquals(2, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));

        verify(store).delete(call1);
        verify(store).delete(call2);
        verify(store, never()).delete(otherThread);
    }

    @Test
    void threadPrefix_isKeyPrefix() {
        String prefix = DiscussionArtifacts.threadPrefix(SCOPE, "t1");
        assertEquals("ns-a/homelab-pilot/t1/", prefix);
        // The spill key for the same crew+thread must start with the delete prefix,
        // otherwise GC would never match the objects it is meant to reap.
        assertTrue(DiscussionArtifacts.key(SCOPE, "t1", "k8s-config", "agree", "u")
                .startsWith(prefix));
    }

    @Test
    void key_crewlessScope_usesNocrewSegment() {
        assertEquals("ns-a/nocrew/t1/agent/agree-u",
                DiscussionArtifacts.key(CrewScope.of("ns-a", ""), "t1", "agent", "agree", "u"));
    }

    @Test
    void key_sameCrewInTwoNamespaces_differs() {
        String a = DiscussionArtifacts.key(SCOPE, "t1", "agent", "agree", "u");
        String b = DiscussionArtifacts.key(CrewScope.of("ns-b", "homelab-pilot"), "t1", "agent", "agree", "u");
        assertNotEquals(a, b);
        assertEquals("ns-b/homelab-pilot/t1/agent/agree-u", b);
    }

    @Test
    void reapOrphans_neverTouchesSameCrewInAnotherNamespace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var otherNs = objectInfo("ns-b/homelab-pilot/dead/agent/agree-a", false, NOW.minus(Duration.ofHours(3)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(otherNs));

        assertEquals(0, DiscussionArtifacts.reapOrphans(conn, SCOPE, t -> false, GRACE, NOW));
        verify(store, never()).delete(anyString());
    }

    @Test
    void deleteThreadArtifacts_emptyInput_noStoreAccess() throws Exception {
        var conn = mock(Connection.class);
        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of()));
        verifyNoInteractions(conn);
    }

    @Test
    void deleteThreadArtifacts_nullConn_returnsZeroWithoutNpe() {
        // The scheduler can fire before NATS is connected; a null connection must be
        // a clean no-op, not an NPE that the catch block swallows into a misleading log.
        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(null, SCOPE, List.of("t1")));
    }

    @Test
    void deleteThreadArtifacts_deletesOnlyMatchingLiveObjects() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        // Build the ObjectInfo mocks before the getList() stub: each does its own
        // stubbing, which would otherwise nest inside an unfinished when(getList()).
        var match1 = objectInfo("ns-a/homelab-pilot/t1/k8s-config/agree-a", false);
        var match2 = objectInfo("ns-a/homelab-pilot/t1/net/agree-b", false);       // other agent, same thread
        var otherThread = objectInfo("ns-a/homelab-pilot/t2/k8s-config/agree-c", false);
        var alreadyDeleted = objectInfo("ns-a/homelab-pilot/t1/gone/agree-d", true); // matches prefix but deleted
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(match1, match2, otherThread, alreadyDeleted));

        int deleted = DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1"));

        assertEquals(2, deleted);
        verify(store).delete("ns-a/homelab-pilot/t1/k8s-config/agree-a");
        verify(store).delete("ns-a/homelab-pilot/t1/net/agree-b");
        verify(store, never()).delete("ns-a/homelab-pilot/t2/k8s-config/agree-c");
        verify(store, never()).delete("ns-a/homelab-pilot/t1/gone/agree-d");
    }

    @Test
    void deleteThreadArtifacts_multipleThreadsListedOnce() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var o1 = objectInfo("ns-a/homelab-pilot/t1/a/agree-1", false);
        var o2 = objectInfo("ns-a/homelab-pilot/t2/a/agree-2", false);
        var o3 = objectInfo("ns-a/homelab-pilot/t3/a/agree-3", false);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(o1, o2, o3));

        int deleted = DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1", "t2"));

        assertEquals(2, deleted);
        verify(store, times(1)).getList(); // one list per batch, not per thread
        verify(store).delete("ns-a/homelab-pilot/t1/a/agree-1");
        verify(store).delete("ns-a/homelab-pilot/t2/a/agree-2");
        verify(store, never()).delete("ns-a/homelab-pilot/t3/a/agree-3");
    }

    @Test
    void deleteThreadArtifacts_swallowsFailure() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenThrow(new RuntimeException("nats down"));
        // GC must never break the lifecycle path; failure returns 0, no exception.
        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));
    }

    // --- orphan reaper (GC Layer 3) ---

    private static final Instant NOW = Instant.parse("2026-06-25T12:00:00Z");
    private static final Duration GRACE = Duration.ofHours(2);

    @Test
    void threadIdOf_parsesThreadSegment() {
        assertEquals("t1", DiscussionArtifacts.threadIdOf("ns-a/homelab-pilot/t1/agent/agree-u", PREFIX));
        assertNull(DiscussionArtifacts.threadIdOf("ns-a/other-crew/t1/agent/agree-u", PREFIX),
                "a different crew's key must not parse under our crew");
        assertNull(DiscussionArtifacts.threadIdOf("ns-a/homelab-pilot/no-more-slashes", PREFIX),
                "a key with no thread-terminating slash is malformed");
    }

    @Test
    void reapOrphans_reapsDeadThreadPastGrace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var orphan = objectInfo("ns-a/homelab-pilot/dead/agent/agree-a", false, NOW.minus(Duration.ofHours(3)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(orphan));

        int reaped = DiscussionArtifacts.reapOrphans(conn, SCOPE, t -> false, GRACE, NOW);

        assertEquals(1, reaped);
        verify(store).delete("ns-a/homelab-pilot/dead/agent/agree-a");
    }

    @Test
    void reapOrphans_keepsLiveThreadEvenPastGrace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var live = objectInfo("ns-a/homelab-pilot/t-live/agent/agree-a", false, NOW.minus(Duration.ofHours(5)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(live));

        // A live thread's artifacts are never reaped, no matter how old - this is the
        // guard that makes the reaper safe to run during a measurement.
        int reaped = DiscussionArtifacts.reapOrphans(conn, SCOPE, "t-live"::equals, GRACE, NOW);

        assertEquals(0, reaped);
        verify(store, never()).delete(anyString());
    }

    @Test
    void reapOrphans_keepsRecentOrphanWithinGrace() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var recent = objectInfo("ns-a/homelab-pilot/dead/agent/agree-a", false, NOW.minus(Duration.ofMinutes(30)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(recent));

        int reaped = DiscussionArtifacts.reapOrphans(conn, SCOPE, t -> false, GRACE, NOW);

        assertEquals(0, reaped, "within the grace window even a dead-thread object is left alone");
        verify(store, never()).delete(anyString());
    }

    @Test
    void reapOrphans_ignoresOtherCrewDeletedAndUnknownAge() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var otherCrew = objectInfo("ns-a/other/dead/agent/agree-a", false, NOW.minus(Duration.ofHours(3)));
        var alreadyDeleted = objectInfo("ns-a/homelab-pilot/dead/agent/agree-b", true, NOW.minus(Duration.ofHours(3)));
        var unknownAge = objectInfo("ns-a/homelab-pilot/dead/agent/agree-c", false, null);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(otherCrew, alreadyDeleted, unknownAge));

        int reaped = DiscussionArtifacts.reapOrphans(conn, SCOPE, t -> false, GRACE, NOW);

        assertEquals(0, reaped);
        verify(store, never()).delete(anyString());
    }

    @Test
    void reapOrphans_nullConn_returnsZero() {
        assertEquals(0, DiscussionArtifacts.reapOrphans(null, SCOPE, t -> false, GRACE, NOW));
    }

    @Test
    void reapOrphans_perObjectDeleteFailure_continuesToNext() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        var bad = objectInfo("ns-a/homelab-pilot/dead/agent/agree-bad", false, NOW.minus(Duration.ofHours(3)));
        var good = objectInfo("ns-a/homelab-pilot/dead/agent/agree-good", false, NOW.minus(Duration.ofHours(3)));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenReturn(List.of(bad, good));
        when(store.delete("ns-a/homelab-pilot/dead/agent/agree-bad")).thenThrow(new RuntimeException("locked"));

        // One failing delete must not abort the rest of the pass (best-effort per object).
        int reaped = DiscussionArtifacts.reapOrphans(conn, SCOPE, t -> false, GRACE, NOW);

        assertEquals(1, reaped);
        verify(store).delete("ns-a/homelab-pilot/dead/agent/agree-good");
    }

    @Test
    void reapOrphans_swallowsFailure() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(store);
        when(store.getList()).thenThrow(new RuntimeException("nats down"));
        assertEquals(0, DiscussionArtifacts.reapOrphans(conn, SCOPE, t -> false, GRACE, NOW));
    }

    @Test
    void deleteThreadArtifacts_reachesTheWorkspaceBucketToo() throws Exception {
        var conn = mock(Connection.class);
        var oldStore = mock(ObjectStore.class);
        var newStore = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(newStore);
        var inOld = objectInfo("ns-a/homelab-pilot/t1/a/001-pods", false);
        var inNew = objectInfo("ns-a/homelab-pilot/t1/b/001-pods", false);
        when(oldStore.getList()).thenReturn(List.of(inOld));
        when(newStore.getList()).thenReturn(List.of(inNew));

        assertEquals(2, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));
        verify(oldStore).delete("ns-a/homelab-pilot/t1/a/001-pods");
        verify(newStore).delete("ns-a/homelab-pilot/t1/b/001-pods");
    }

    @Test
    void deleteThreadArtifacts_aWorkspaceBucketNoWriterCreatedYetDoesNotStopTheOldOne() throws Exception {
        var conn = mock(Connection.class);
        var oldStore = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenThrow(new java.io.IOException("stream not found"));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);
        var inOld = objectInfo("ns-a/homelab-pilot/t1/a/001-pods", false);
        when(oldStore.getList()).thenReturn(List.of(inOld));

        assertEquals(1, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));
        verify(oldStore).delete("ns-a/homelab-pilot/t1/a/001-pods");
    }

    @Test
    void deleteThreadArtifacts_aMissingBucketThatOnlyFailsOnTheFirstCallStillLetsTheOldOneBeCleaned() throws Exception {
        // The client binds a bucket lazily: objectStore() succeeds and getList() reports "stream not found".
        var conn = mock(Connection.class);
        var lazy = mock(ObjectStore.class);
        var oldStore = mock(ObjectStore.class);
        when(lazy.getList()).thenThrow(new java.io.IOException("stream not found [10059]"));
        var inOld = objectInfo("ns-a/homelab-pilot/t1/a/001-pods", false);
        when(oldStore.getList()).thenReturn(List.of(inOld));
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(lazy);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);

        assertEquals(1, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));
        verify(oldStore).delete("ns-a/homelab-pilot/t1/a/001-pods");
    }

    @Test
    void deleteThreadArtifacts_theOldBucketFailingStillFailsThePass() throws Exception {
        var conn = mock(Connection.class);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenThrow(new java.io.IOException("stream not found"));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenThrow(new java.io.IOException("nats down"));

        assertEquals(0, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));
    }

    @Test
    void storeHolding_answersWithTheFirstBucketThatHasTheObject() throws Exception {
        var conn = mock(Connection.class);
        var oldStore = mock(ObjectStore.class);
        var newStore = mock(ObjectStore.class);
        var info = objectInfo("k", false);
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(newStore);
        when(oldStore.getInfo("k")).thenReturn(info);

        assertSame(oldStore, DiscussionArtifacts.storeHolding(conn, "k").store());
        assertSame(info, DiscussionArtifacts.storeHolding(conn, "k").info());

        when(newStore.getInfo("k")).thenReturn(info);
        assertSame(newStore, DiscussionArtifacts.storeHolding(conn, "k").store());
    }

    @Test
    void storeHolding_skipsAMissingBucketAndAnAbsentObject() throws Exception {
        var conn = mock(Connection.class);
        var oldStore = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenThrow(new java.io.IOException("stream not found"));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);

        assertNull(DiscussionArtifacts.storeHolding(conn, "absent"));

        var gone = objectInfo("gone", true);
        when(oldStore.getInfo("gone")).thenReturn(gone);
        assertNull(DiscussionArtifacts.storeHolding(conn, "gone"));
    }

    @Test
    void storeHolding_aFailureThatIsNotAMissingBucketIsNotHidden() throws Exception {
        var conn = mock(Connection.class);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenThrow(new java.io.IOException("nats down"));

        assertThrows(java.io.IOException.class, () -> DiscussionArtifacts.storeHolding(conn, "k"));
    }

    @Test
    void isBucketMissing_recognizesTheMissingBucketInTheCauseChainOnly() {
        assertTrue(DiscussionArtifacts.isBucketMissing(new java.io.IOException("stream not found")));
        assertTrue(DiscussionArtifacts.isBucketMissing(new RuntimeException("x", new java.io.IOException("stream not found"))));
        assertFalse(DiscussionArtifacts.isBucketMissing(new java.io.IOException("timeout")));
        assertFalse(DiscussionArtifacts.isBucketMissing(new RuntimeException()));
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
