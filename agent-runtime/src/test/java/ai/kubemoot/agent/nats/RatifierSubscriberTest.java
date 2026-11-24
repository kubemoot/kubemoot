package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The agent's side of ratification: it wakes on the draft, checks it, and publishes a verdict for the round. */
class RatifierSubscriberTest {

    private static final String SUBJECT = "kubemoot.discuss.ns-a.nocrew.broadcast.t1";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static String draft(String agent, String id, String round, String circle) {
        return "{\"messageType\":\"draft\",\"threadId\":\"t1\",\"agentName\":\"" + agent + "\",\"messageId\":\"" + id
                + "\",\"content\":\"14 nodes, all Ready.\",\"metadata\":{\"draftId\":\"d-7\",\"round\":" + round
                + ",\"innerCircle\":" + circle + "}}";
    }

    private static DiscussionSubscriber subscriber(Connection conn, ChatService chat) {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        when(natsProvider.getConnection()).thenReturn(conn);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chat, metrics,
                DiscussionSubscriberHelpersTest.stubProperties("k8s-nodes", "kubernetes", null, false),
                "http://localhost:11434");
    }

    private static JsonNode lastPublished(Connection conn) throws Exception {
        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn, atLeastOnce()).publish(anyString(), payload.capture());
        return MAPPER.readTree(payload.getValue());
    }

    private static ChatService chatReplying(String reply) {
        var chat = mock(ChatService.class);
        when(chat.directChat(any(), anyBoolean(), any())).thenReturn(
                new ChatService.ChatResult("c", reply, "m", "t1", 10, 5, "s", ""));
        return chat;
    }

    @Test
    void everyRoleWakesOnTheDraft() {
        assertTrue(DiscussionSubscriber.triggerTypesFor("tooler").contains("draft"));
        assertTrue(DiscussionSubscriber.triggerTypesFor("analyst").contains("draft"));
    }

    @Test
    void aDraftWakesOnlyTheRosterAndCarriesItsRound() throws Exception {
        var sub = subscriber(mock(Connection.class), mock(ChatService.class));

        var evaluation = sub.admitForEvaluation(draft("coordinator", "d1", "2", "[\"k8s-nodes\",\"compute\"]"));

        assertNotNull(evaluation);
        assertEquals("draft", evaluation.trigger());
        assertEquals(2, evaluation.round());
        assertEquals("d-7", evaluation.draftId());
        assertTrue(evaluation.conversation().contains("Draft Answer To Ratify"), evaluation.conversation());
        assertNull(sub.admitForEvaluation(draft("coordinator", "d2", "1", "[\"someone-else\"]")),
                "an agent off the roster stays silent");
    }

    @Test
    void theDraftIsNotACountedContributionSoTheRateLimitCannotRefuseIt() throws Exception {
        var sub = subscriber(mock(Connection.class), mock(ChatService.class));
        for (int i = 0; i < 6; i++) {
            assertNotNull(sub.admitForEvaluation(draft("coordinator", "d" + i, "1", "[]")), "draft " + i);
        }
        assertEquals(0, sub.rateLimitPermits("t1"));
    }

    @Test
    void aReplyToADraftIsNotAFindingInTheThreadTheAgentsReadAfterwards() throws Exception {
        var sub = subscriber(mock(Connection.class), mock(ChatService.class));
        sub.admitForEvaluation("{\"messageType\":\"concern\",\"threadId\":\"t1\",\"agentName\":\"compute\","
                + "\"messageId\":\"r1\",\"content\":\"the objection\",\"metadata\":{\"ratifies\":1}}");

        var evaluation = sub.admitForEvaluation(draft("coordinator", "d1", "2", "[]"));

        assertFalse(evaluation.conversation().contains("the objection"), evaluation.conversation());
    }

    @Test
    void aVerdictInTheReplyIsPublishedForTheRound() throws Exception {
        var conn = mock(Connection.class);
        var sub = subscriber(conn, chatReplying("CONCERN: my results hold 14 nodes, the draft says 15"));

        sub.ratifyAndRespond(SUBJECT, "t1", "conversation", 2, "draft-2");

        var msg = lastPublished(conn);
        assertEquals("concern", msg.path("messageType").asText());
        assertEquals("my results hold 14 nodes, the draft says 15", msg.path("content").asText());
        assertEquals(2, msg.path("metadata").path("ratifies").asInt());
        assertEquals("draft-2", msg.path("metadata").path("draftId").asText(), "the reply names the draft it answers");
    }

    @Test
    void theAgentIsAskedToCheckTheDraftAndTheInstructionNamesNoTool() throws Exception {
        var conn = mock(Connection.class);
        var chat = chatReplying("AGREE");
        var sub = subscriber(conn, chat);

        sub.ratifyAndRespond(SUBJECT, "t1", "the thread", 1, "draft-1");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chat).directChat(request.capture(), anyBoolean(), any());
        String message = request.getValue().message();
        assertTrue(message.startsWith("the thread"));
        assertTrue(message.contains("[RATIFY]"), message);
        assertEquals("agree", lastPublished(conn).path("messageType").asText());
    }

    @Test
    void aCrewThatShipsTheRatifyPromptModuleGetsOnlyTheMarker() throws Exception {
        var conn = mock(Connection.class);
        var chat = chatReplying("AGREE");
        when(chat.systemPromptMentions("DEFINE COMPONENT ratify-prompt")).thenReturn(true);
        var sub = subscriber(conn, chat);

        sub.ratifyAndRespond(SUBJECT, "t1", "the thread", 1, "draft-1");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chat).directChat(request.capture(), anyBoolean(), any());
        assertEquals("the thread\n\n[RATIFY]", request.getValue().message());
    }

    @Test
    void aReplyWithNoVerdictIsAFailureNotAnAgreement() throws Exception {
        var conn = mock(Connection.class);
        var sub = subscriber(conn, chatReplying("Looks fine to me."));

        sub.ratifyAndRespond(SUBJECT, "t1", "conversation", 1, "draft-1");

        var msg = lastPublished(conn);
        assertEquals("failure", msg.path("messageType").asText());
        assertEquals("no_verdict", msg.path("metadata").path("failureType").asText());
        assertEquals(1, msg.path("metadata").path("ratifies").asInt());
    }

    @Test
    void aFailedCallIsAFailureForTheRound() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.directChat(any(), anyBoolean(), any())).thenThrow(new IllegalStateException("model server down"));
        var sub = subscriber(conn, chat);

        sub.ratifyAndRespond(SUBJECT, "t1", "conversation", 2, "draft-2");

        var msg = lastPublished(conn);
        assertEquals("failure", msg.path("messageType").asText());
        assertTrue(msg.path("content").asText().contains("model server down"));
        assertEquals(2, msg.path("metadata").path("ratifies").asInt());
        assertEquals("draft-2", msg.path("metadata").path("draftId").asText());
    }
}
