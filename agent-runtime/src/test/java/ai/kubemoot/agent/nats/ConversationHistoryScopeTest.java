package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import org.junit.jupiter.api.Test;

import java.util.HashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** Conversation history is keyed by namespace and crew, with a read fallback to the old key. */
class ConversationHistoryScopeTest {

    private final Map<String, byte[]> store = new HashMap<>();

    private KeyValue fakeKv() throws Exception {
        var kv = mock(KeyValue.class);
        when(kv.get(anyString())).thenAnswer(inv -> {
            byte[] value = store.get(inv.<String>getArgument(0));
            if (value == null) {
                return null;
            }
            var entry = mock(KeyValueEntry.class);
            when(entry.getValue()).thenReturn(value);
            return entry;
        });
        when(kv.put(anyString(), any(byte[].class))).thenAnswer(inv -> {
            store.put(inv.getArgument(0), inv.getArgument(1));
            return 1L;
        });
        return kv;
    }

    private DiscussionOrchestrator orchestrator(String namespace, String crew) throws Exception {
        var provider = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        var kv = fakeKv();
        when(conn.keyValue(anyString())).thenReturn(kv);
        when(provider.getConnection()).thenReturn(conn);
        when(provider.scope()).thenReturn(CrewScope.of(namespace, crew));
        return new DiscussionOrchestrator(provider, DiscussionOrchestratorTest.stubProperties("general"),
                mock(ChatService.class), mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
    }

    @Test
    void twoCrewsWithTheSameConversationIdDoNotShareHistory() throws Exception {
        var alpha = orchestrator("team-a", "ops");
        var beta = orchestrator("team-b", "ops");
        var sameNamespace = orchestrator("team-a", "billing");

        alpha.recordConversationTurn("c-1", "alpha question", "alpha answer", "t-1", false);

        assertEquals(1, alpha.buildConversationContext("c-1").size());
        assertTrue(beta.buildConversationContext("c-1").isEmpty());
        assertTrue(sameNamespace.buildConversationContext("c-1").isEmpty());
        assertTrue(store.containsKey("conv.team-a.ops.c-1"));
        assertFalse(store.containsKey("conv.c-1"));
    }

    @Test
    void oldKeyIsReadAsFallbackAndNeverWritten() throws Exception {
        var seed = orchestrator("team-a", "ops");
        seed.recordConversationTurn("c-2", "old question", "old answer", "t-1", false);
        store.put("conv.c-2", store.remove("conv.team-a.ops.c-2"));

        var restarted = orchestrator("team-a", "ops");
        var context = restarted.buildConversationContext("c-2");
        assertEquals("old question", context.get(0).get("query"));

        restarted.recordConversationTurn("c-2", "next question", "next answer", "t-2", false);
        assertTrue(store.containsKey("conv.team-a.ops.c-2"));
        assertTrue(store.containsKey("conv.c-2"));
        assertEquals(2, DiscussionOrchestrator.deserializeHistory(store.get("conv.team-a.ops.c-2")).getTurns().size());
    }

    @Test
    void scopedKeyWinsOverTheOldKey() throws Exception {
        var crew = orchestrator("team-a", "ops");
        crew.recordConversationTurn("c-3", "scoped question", "scoped answer", "t-1", false);
        var old = orchestrator("team-b", "other");
        old.recordConversationTurn("c-3", "old question", "old answer", "t-9", false);
        store.put("conv.c-3", store.get("conv.team-b.other.c-3"));

        var restarted = orchestrator("team-a", "ops");
        assertEquals("scoped question", restarted.buildConversationContext("c-3").get(0).get("query"));
    }

    @Test
    void noEntryUnderEitherKeyGivesNoContext() throws Exception {
        assertTrue(orchestrator("team-a", "ops").buildConversationContext("missing").isEmpty());
    }
}
