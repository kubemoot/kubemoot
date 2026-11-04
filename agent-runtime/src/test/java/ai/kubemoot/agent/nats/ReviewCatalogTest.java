package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallHeaders;
import ai.kubemoot.agent.chat.ChatService;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import io.nats.client.impl.Headers;
import java.util.List;
import java.util.OptionalLong;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

class ReviewCatalogTest {

    private final NatsConnectionProvider natsProvider = mock(NatsConnectionProvider.class);
    private final ChatService chatService = mock(ChatService.class);
    private final ReviewCatalog catalog = new ReviewCatalog(natsProvider, chatService);

    ReviewCatalogTest() {
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns", "crew"));
        when(chatService.simpleCallContextTokens()).thenReturn(OptionalLong.of(32_768));
    }

    private static ObjectInfo toolCall(String key) {
        var h = new Headers();
        h.put(CallHeaders.OPERATION_NAME, CallHeaders.OPERATION_EXECUTE_TOOL);
        h.put(CallHeaders.TOOL_NAME, "pods_list");
        h.put(CallHeaders.AGENT_NAME, "k8s");
        h.put(CallHeaders.BYTES, "20");
        var info = mock(ObjectInfo.class);
        when(info.isDeleted()).thenReturn(false);
        when(info.getObjectName()).thenReturn(key);
        when(info.getHeaders()).thenReturn(h);
        when(info.getSize()).thenReturn(20L);
        return info;
    }

    @Test
    void withoutABusThereIsNoCatalog() {
        when(natsProvider.getConnection()).thenReturn(null);

        assertEquals("", catalog.forReview("t1"));
    }

    @Test
    void aThreadWithNoStoredFilesHasNoCatalog() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenReturn(store);
        when(store.getList()).thenReturn(List.of());

        assertEquals("", catalog.forReview("t1"));
    }

    @Test
    void theCatalogListsOnlyTheFilesOfTheThread() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenReturn(store);
        var stored = List.of(toolCall("ns/crew/t1/k8s/001-pods_list"), toolCall("ns/crew/other/k8s/001-pods_list"));
        when(store.getList()).thenReturn(stored);

        String text = catalog.forReview("t1");

        assertTrue(text.contains("[CATALOG calls=1"), text);
        assertTrue(text.contains("[CATALOG-CALL key=k8s/001-pods_list"), "keys are relative to the thread: " + text);
        assertFalse(text.contains("ns/crew/"), text);
    }

    @Test
    void anUnreadableBucketGivesNoCatalogInsteadOfAFailure() throws Exception {
        var conn = mock(Connection.class);
        var store = mock(ObjectStore.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenReturn(store);
        when(store.getList()).thenThrow(new java.io.IOException("bucket gone"));

        assertEquals("", catalog.forReview("t1"));
    }
}
