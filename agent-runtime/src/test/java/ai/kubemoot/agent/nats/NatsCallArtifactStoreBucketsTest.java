package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.ObjectStoreManagement;
import io.nats.client.api.ObjectInfo;
import io.nats.client.api.ObjectMeta;
import io.nats.client.api.ObjectStoreConfiguration;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

/**
 * The call store writes to the workspace bucket and creates it with its TTL, and keeps numbering a
 * thread's calls across the move from the old bucket. Plain JUnit 5 with a mocked connection.
 */
class NatsCallArtifactStoreBucketsTest {

    private static final CrewScope SCOPE = CrewScope.of("ns-a", "homelab-pilot");

    private final NatsConnectionProvider provider = mock(NatsConnectionProvider.class);
    private final Connection conn = mock(Connection.class);
    private final NatsCallArtifactStore store = new NatsCallArtifactStore(provider, new ObjectMapper());

    NatsCallArtifactStoreBucketsTest() throws Exception {
        when(provider.scope()).thenReturn(SCOPE);
        when(provider.getConnection()).thenReturn(conn);
        when(conn.objectStoreManagement()).thenReturn(mock(ObjectStoreManagement.class));
    }

    private static ObjectInfo stored(String key) {
        var info = mock(ObjectInfo.class);
        when(info.getObjectName()).thenReturn(key);
        when(info.isDeleted()).thenReturn(false);
        return info;
    }

    @Test
    void aResultIsWrittenToTheWorkspaceBucketAndTheReferenceNamesIt() throws Exception {
        var workspace = mock(ObjectStore.class);
        var legacy = mock(ObjectStore.class);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(workspace);
        when(conn.objectStore(DiscussionArtifacts.LEGACY_BUCKET)).thenReturn(legacy);

        assertTrue(store.put("t1", "k8s", "pods_list", "ns-a/homelab-pilot/t1/k8s/001-pods_list",
                "abc".getBytes(), Map.of()));

        verify(workspace).put(any(ObjectMeta.class), any(java.io.InputStream.class));
        verify(legacy, never()).put(any(ObjectMeta.class), any(java.io.InputStream.class));
        var body = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(any(String.class), body.capture());
        assertTrue(new String(body.getValue()).contains("\"bucket\":\"kubemoot_workspace\""));
    }

    @Test
    void theBucketIsCreatedWithTheTtlUnderTheWorkspaceName() throws Exception {
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(mock(ObjectStore.class));

        store.put("t1", "k8s", "pods_list", "k", "abc".getBytes(), Map.of());

        var config = ArgumentCaptor.forClass(ObjectStoreConfiguration.class);
        verify(conn.objectStoreManagement()).create(config.capture());
        assertEquals(DiscussionArtifacts.WORKSPACE_BUCKET, config.getValue().getBucketName());
        assertEquals(DiscussionArtifacts.TTL, config.getValue().getTtl());
    }

    @Test
    void numberingContinuesPastCallsStoredInEitherBucket() throws Exception {
        String prefix = DiscussionArtifacts.threadPrefix(SCOPE, "t1") + "k8s/";
        var legacy = mock(ObjectStore.class);
        var workspace = mock(ObjectStore.class);
        var fourth = stored(prefix + "004-pods_list");
        var second = stored(prefix + "002-pods_list");
        when(legacy.getList()).thenReturn(List.of(fourth));
        when(workspace.getList()).thenReturn(List.of(second));
        when(conn.objectStore(DiscussionArtifacts.LEGACY_BUCKET)).thenReturn(legacy);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(workspace);

        assertEquals(prefix + "005-events_list", store.reserveKey("t1", "k8s", "events_list"));
    }

    @Test
    void numberingStartsAtOneWhenNeitherBucketExistsYet() throws Exception {
        when(conn.objectStore(any(String.class))).thenThrow(new java.io.IOException("stream not found"));

        String prefix = DiscussionArtifacts.threadPrefix(SCOPE, "t1") + "k8s/";
        assertEquals(prefix + "001-pods_list", store.reserveKey("t1", "k8s", "pods_list"));
    }

    @Test
    void aFailureListingABucketThatExistsMeansTheCallIsNotStoredNotNumberedFromOne() throws Exception {
        var workspace = mock(ObjectStore.class);
        when(workspace.getList()).thenThrow(new java.io.IOException("permission violation"));
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(workspace);
        when(conn.objectStore(DiscussionArtifacts.LEGACY_BUCKET)).thenReturn(mock(ObjectStore.class));

        assertNull(store.reserveKey("t1", "k8s", "pods_list"));
    }

    @Test
    void theNumbersAreReadAgainAfterAFailedListing() throws Exception {
        var workspace = mock(ObjectStore.class);
        when(workspace.getList()).thenThrow(new java.io.IOException("nats down"))
                .thenReturn(List.of());
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(workspace);
        when(conn.objectStore(DiscussionArtifacts.LEGACY_BUCKET)).thenReturn(mock(ObjectStore.class));
        String prefix = DiscussionArtifacts.threadPrefix(SCOPE, "t1") + "k8s/";

        assertNull(store.reserveKey("t1", "k8s", "pods_list"));
        assertEquals(prefix + "001-pods_list", store.reserveKey("t1", "k8s", "pods_list"));
    }
}
