package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.signal.SignalCall;
import ai.kubemoot.agent.signal.SignalScope;
import ai.kubemoot.agent.signal.SignalTestKinds;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
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

/** The agent's side of the signal tool and of the clarifying-question round. */
class SignalToolSubscriberTest {

    private static final String SUBJECT = "kubemoot.discuss.ns-a.nocrew.broadcast.t1";
    private static final String AGENT = "k8s-nodes";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static DiscussionSubscriber subscriber(Connection conn, ChatService chat, String signals) {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        when(natsProvider.getConnection()).thenReturn(conn);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chat, metrics,
                DiscussionSubscriberHelpersTest.stubProperties(AGENT, "kubernetes", null, false, signals),
                "http://localhost:11434");
    }

    private static List<JsonNode> published(Connection conn) throws Exception {
        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn, atLeastOnce()).publish(anyString(), payload.capture());
        var out = new ArrayList<JsonNode>();
        for (byte[] p : payload.getAllValues()) {
            out.add(MAPPER.readTree(p));
        }
        return out;
    }

    private static JsonNode last(Connection conn) throws Exception {
        var all = published(conn);
        return all.get(all.size() - 1);
    }

    private static ChatService.ChatResult result(String reply) {
        return new ChatService.ChatResult("c", reply, "m", "t1", 10, 5, "s", "");
    }

    /** A chat whose model calls the signal tool with {@code arguments} (when not null) and replies {@code reply}. */
    private static ChatService chatThatSignals(String arguments, String reply) {
        var chat = mock(ChatService.class);
        when(chat.directChat(any(), anyBoolean(), any())).thenAnswer(inv -> {
            var scope = SignalScope.current();
            if (arguments != null) {
                assertNotNull(scope, "the signal tool is offered during the turn");
                scope.handle(arguments);
            }
            return result(reply);
        });
        return chat;
    }

    @Test
    void theVerdictIsTheSignalCallAndTheReplyStaysTheFinding() throws Exception {
        var conn = mock(Connection.class);
        var sub = subscriber(conn, mock(ChatService.class), SignalTestKinds.CONSENT_3);
        var call = new SignalCall("concern", "", "k8s-nodes counted 100 nodes", "the node list has 7", "k1");

        sub.classifyAndPublishResult(SUBJECT, "t1", result("The node list holds 7 nodes."),
                DiscussionSubscriber.SignalTiming.NONE, "static", call);

        var msg = last(conn);
        assertEquals("concern", msg.path("messageType").asText());
        assertTrue(msg.path("content").asText().startsWith("k8s-nodes counted 100 nodes\nReason: the node list has 7"));
        assertTrue(msg.path("content").asText().contains("The node list holds 7 nodes."));
        assertEquals("concern", msg.path("metadata").path("signalKind").asText());
        assertEquals("k1", msg.path("metadata").path("signalEvidence").asText());
    }

    @Test
    void aReplyThatOpensWithAnAgreeWordIsStillTheToolsVerdict() throws Exception {
        var conn = mock(Connection.class);
        var sub = subscriber(conn, mock(ChatService.class), SignalTestKinds.CONSENT_3);

        sub.classifyAndPublishResult(SUBJECT, "t1", result("NO_DATA: nothing"), DiscussionSubscriber.SignalTiming.NONE,
                "static", new SignalCall("agree", "", "7 nodes", "", ""));

        assertEquals("agree", last(conn).path("messageType").asText(), "the call wins over the reply's words");
    }

    @Test
    void noDataFromTheToolIsAFailureNotAnAgree() throws Exception {
        var conn = mock(Connection.class);
        var sub = subscriber(conn, mock(ChatService.class), SignalTestKinds.CONSENT_3);

        sub.classifyAndPublishResult(SUBJECT, "t1", result("The queries came back empty."),
                DiscussionSubscriber.SignalTiming.NONE, "static",
                new SignalCall("no_data", "", "every metric query returned an empty result", "", ""));

        var msg = last(conn);
        assertEquals("failure", msg.path("messageType").asText());
        assertEquals("gather_failed", msg.path("metadata").path("failureType").asText());
    }

    @Test
    void withoutASignalCallTheReplysOpeningWordsDecideAsBefore() throws Exception {
        var conn = mock(Connection.class);
        var sub = subscriber(conn, mock(ChatService.class), SignalTestKinds.CONSENT_3);

        sub.classifyAndPublishResult(SUBJECT, "t1", result("CONCERN: the count is wrong"),
                DiscussionSubscriber.SignalTiming.NONE, "static", null);
        sub.classifyAndPublishResult(SUBJECT, "t2", result("7 nodes, all Ready"),
                DiscussionSubscriber.SignalTiming.NONE, "static", null);

        var all = published(conn);
        assertEquals("concern", all.get(0).path("messageType").asText());
        assertEquals("agree", all.get(1).path("messageType").asText(), "no call and no sentinel: agree");
        assertTrue(all.get(1).path("metadata").path("signalKind").isMissingNode());
    }

    @Test
    void theToolIsBoundForTheEvaluationAndItsCallIsPublished() throws Exception {
        var conn = mock(Connection.class);
        var chat = chatThatSignals("{\"kind\":\"question\",\"to\":\"k8s-config\",\"statement\":\"recheck the count?\"}",
                "My review of the node count.");
        var sub = subscriber(conn, chat, SignalTestKinds.CONSENT_3);

        sub.recheckAndRespond(SUBJECT, answered("t1"));

        var msg = last(conn);
        assertEquals("question", msg.path("messageType").asText());
        assertEquals("k8s-config", msg.path("metadata").path("signalTo").asText());
        assertNull(SignalScope.current(), "the tool is unbound after the turn");
    }

    @Test
    void anArchetypeWithoutSignalsOffersNoTool() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.directChat(any(), anyBoolean(), any())).thenAnswer(inv -> {
            assertNull(SignalScope.current(), "no declared kinds: no signal tool");
            return result("7 nodes");
        });
        var sub = subscriber(conn, chat, null);

        sub.recheckAndRespond(SUBJECT, asked("t1", "q-1"));

        assertEquals("agree", last(conn).path("messageType").asText());
    }

    @Test
    void theAgentAskedRerunsAndItsRevisedFindingNamesTheQuestion() throws Exception {
        var conn = mock(Connection.class);
        var chat = chatThatSignals("{\"kind\":\"agree\",\"statement\":\"7 nodes\"}", "7 nodes; 100 was the pod count.");
        var sub = subscriber(conn, chat, SignalTestKinds.CONSENT_3);

        sub.recheckAndRespond(SUBJECT, asked("t1", "t1/0/q1"));

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chat).directChat(request.capture(), anyBoolean(), any());
        assertTrue(request.getValue().message().contains("[RECHECK] k8s-advisor asked you"),
                "the turn says who asked: " + request.getValue().message());
        var all = published(conn);
        assertEquals("evaluating", all.get(0).path("messageType").asText(), "the recheck runs the tools again");
        var revised = all.get(all.size() - 1);
        assertEquals("agree", revised.path("messageType").asText());
        assertEquals("t1/0/q1", revised.path("metadata").path("answers").asText());
        assertTrue(all.get(0).path("metadata").path("answers").isMissingNode(), "only the verdict answers");
    }

    @Test
    void aFailedRecheckIsAFailureThatNamesTheQuestion() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.directChat(any(), anyBoolean(), any())).thenThrow(new IllegalStateException("model server down"));
        var sub = subscriber(conn, chat, SignalTestKinds.CONSENT_3);

        sub.recheckAndRespond(SUBJECT, asked("t1", "t1/0/q1"));

        var msg = last(conn);
        assertEquals("failure", msg.path("messageType").asText());
        assertEquals("t1/0/q1", msg.path("metadata").path("answers").asText());
    }

    @Test
    void aRelayedQuestionWakesOnlyTheAgentAskedAndCarriesTheQuestion() throws Exception {
        var sub = subscriber(mock(Connection.class), mock(ChatService.class), SignalTestKinds.CONSENT_3);
        var mine = sub.admitForEvaluation(relay("m1", AGENT));
        assertNotNull(mine);
        assertEquals("question_asked", mine.trigger());
        assertEquals("t1/0/q1", mine.recheck().questionId());
        assertEquals("k8s-advisor", mine.recheck().askedBy());
        assertNull(sub.admitForEvaluation(relay("m2", "k8s-storage")), "another agent is not woken");
        for (int i = 0; i < 20; i++) {
            assertNotNull(sub.admitForEvaluation(relay("r" + i, AGENT)), "the rate limit never refuses a recheck");
        }
    }

    private static String relay(String id, String to) {
        return "{\"messageType\":\"question_asked\",\"threadId\":\"t1\",\"agentName\":\"coordinator\",\"messageId\":\""
                + id + "\",\"content\":\"@" + to + ", k8s-advisor asks: recheck?\",\"metadata\":{\"innerCircle\":[\""
                + to + "\"],\"questionId\":\"t1/0/q1\",\"askedBy\":\"k8s-advisor\",\"answeredBy\":\"" + to + "\"}}";
    }

    private static DiscussionSubscriber.PendingEvaluation asked(String threadId, String questionId) {
        return new DiscussionSubscriber.PendingEvaluation(threadId, "the thread", true, "question_asked", 0, "",
                new DiscussionSubscriber.Recheck(questionId, "k8s-advisor", AGENT));
    }

    private static DiscussionSubscriber.PendingEvaluation answered(String threadId) {
        return new DiscussionSubscriber.PendingEvaluation(threadId, "the thread", true, "question_answered", 0, "",
                new DiscussionSubscriber.Recheck("t1/0/q1", AGENT, "k8s-config"));
    }
}
