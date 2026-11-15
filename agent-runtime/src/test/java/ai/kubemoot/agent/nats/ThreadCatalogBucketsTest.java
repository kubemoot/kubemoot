package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import ai.kubemoot.agent.chat.CallHeaders;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import io.nats.client.impl.Headers;
import java.util.List;
import org.junit.jupiter.api.Test;

/** The catalog of a thread reads the workspace bucket and the bucket it was stored in before the rename. */
class ThreadCatalogBucketsTest {

    private static final String PREFIX = "ns/crew/t1/";

    private static ObjectInfo call(String key) {
        var h = new Headers();
        h.put(CallHeaders.OPERATION_NAME, CallHeaders.OPERATION_EXECUTE_TOOL);
        h.put(CallHeaders.TOOL_NAME, "pods_list");
        h.put(CallHeaders.AGENT_NAME, "k8s");
        h.put(CallHeaders.BYTES, "20");
        var info = mock(ObjectInfo.class);
        when(info.getObjectName()).thenReturn(key);
        when(info.isDeleted()).thenReturn(false);
        when(info.getSize()).thenReturn(20L);
        when(info.getHeaders()).thenReturn(h);
        return info;
    }

    @Test
    void callsInEitherBucketAreListedOnceAndInKeyOrder() throws Exception {
        var conn = mock(Connection.class);
        var oldStore = mock(ObjectStore.class);
        var newStore = mock(ObjectStore.class);
        var inBoth = call(PREFIX + "k8s/001-pods_list");
        var onlyOld = call(PREFIX + "k8s/002-pods_list");
        var onlyNew = call(PREFIX + "k8s/003-pods_list");
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenReturn(newStore);
        when(oldStore.getList()).thenReturn(List.of(inBoth, onlyOld));
        when(newStore.getList()).thenReturn(List.of(inBoth, onlyNew));

        var keys = ThreadCatalog.load(conn, PREFIX).entries().stream().map(ThreadCatalog.Entry::key).toList();

        assertEquals(List.of(PREFIX + "k8s/001-pods_list", PREFIX + "k8s/002-pods_list",
                PREFIX + "k8s/003-pods_list"), keys);
    }

    @Test
    void aWorkspaceBucketNoWriterCreatedYetLeavesTheOldBucketsCalls() throws Exception {
        var conn = mock(Connection.class);
        var oldStore = mock(ObjectStore.class);
        var onlyOld = call(PREFIX + "k8s/001-pods_list");
        when(conn.objectStore(DiscussionArtifacts.WORKSPACE_BUCKET)).thenThrow(new java.io.IOException("stream not found"));
        when(conn.objectStore(DiscussionArtifacts.BUCKET)).thenReturn(oldStore);
        when(oldStore.getList()).thenReturn(List.of(onlyOld));

        assertEquals(1, ThreadCatalog.load(conn, PREFIX).calls());
    }
}
