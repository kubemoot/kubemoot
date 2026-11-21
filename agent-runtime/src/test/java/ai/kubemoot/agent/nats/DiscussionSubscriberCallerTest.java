package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** A thread keeps the conversation and principal its thread_start carried, and its model turn gets them. */
class DiscussionSubscriberCallerTest {

    private static final CrewScope SCOPE = CrewScope.of("ns-test", "crew-x");
    private static final String ID_A = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111";
    private static final String ID_B = "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222";

    private static String message(String type, String threadId, String agent, String metadata) {
        return "{\"messageType\":\"" + type + "\",\"threadId\":\"" + threadId
                + "\",\"agentName\":\"" + agent + "\",\"content\":\"What is new?\","
                + "\"metadata\":" + metadata + ",\"messageId\":\"m-" + type + "-" + threadId + "\"}";
    }

    private static String threadStart(String threadId, String metadata) {
        return message("thread_start", threadId, "human", metadata);
    }

    private static String subscriberMeta(String conversation, String id) {
        return "{\"userQuery\":\"q\",\"conversationId\":\"" + conversation + "\","
                + "\"principal\":{\"kind\":\"subscriber\",\"issuer\":\"oreilly\",\"id\":\"" + id + "\"}}";
    }

    private static DiscussionSubscriber subscriber(ChatService chatService) {
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn("tooler");
        var discuss = mock(AgentProperties.Discuss.class);
        when(props.discuss()).thenReturn(discuss);
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.getConnection()).thenReturn(mock(Connection.class));
        when(natsProvider.scope()).thenReturn(SCOPE);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chatService, metrics, props, "http://localhost:11434");
    }

    @Test
    void aThreadStartWithAPrincipalIsRemembered() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t1"),
                threadStart("t1", subscriberMeta("conv-1", ID_A)));
        var caller = subscriber.callerFor("t1");
        assertEquals("conv-1", caller.conversationId());
        assertEquals("subscriber", caller.principal().kind());
        assertEquals(ID_A, caller.principal().id());
    }

    @Test
    void aThreadStartWithoutAPrincipalKeepsTheConversationOnly() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t2"),
                threadStart("t2", "{\"userQuery\":\"q\",\"conversationId\":\"conv-2\"}"));
        var caller = subscriber.callerFor("t2");
        assertEquals("conv-2", caller.conversationId());
        assertNull(caller.principal());
    }

    @Test
    void aThreadStartWithNeitherKeepsNothing() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t3"), threadStart("t3", "{\"userQuery\":\"q\"}"));
        assertNull(subscriber.callerFor("t3"));
    }

    @Test
    void aMalformedPrincipalReadsAsNone() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t4"), threadStart("t4",
                "{\"conversationId\":\"conv-4\",\"principal\":\"subscriber\"}"));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t5"), threadStart("t5",
                "{\"conversationId\":\"conv-5\",\"principal\":{\"kind\":\"subscriber\",\"id\":{\"x\":1}}}"));
        assertNull(subscriber.callerFor("t4").principal());
        assertNull(subscriber.callerFor("t5").principal());
    }

    @Test
    void onlyAThreadStartCanSetThePrincipal() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t6"),
                message("advisory_ready", "t6", "coord", subscriberMeta("conv-6", ID_B)));
        assertNull(subscriber.callerFor("t6"));
    }

    @Test
    void aLaterMessageCannotRebindTheThreadToAnotherPerson() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t7"),
                threadStart("t7", subscriberMeta("conv-7", ID_A)));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t7"),
                message("thread_start", "t7", "intruder", subscriberMeta("conv-7", ID_B)));
        assertEquals(ID_A, subscriber.callerFor("t7").principal().id());
    }

    @Test
    void theModelTurnReceivesTheThreadsCaller() {
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean(), any()))
                .thenReturn(new ChatService.ChatResult("c", "out", "model", null));
        var subscriber = subscriber(chatService);
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t8"),
                threadStart("t8", subscriberMeta("conv-8", ID_A)));

        subscriber.runMullingInferenceForTest("conversation", "t8");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chatService).directChat(request.capture(), anyBoolean(), any());
        assertEquals("conv-8", request.getValue().caller().conversationId());
        assertEquals(ID_A, request.getValue().caller().principal().id());
    }

    @Test
    void twoThreadsOnTheSameAgentEachGetTheirOwnPerson() {
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean(), any()))
                .thenReturn(new ChatService.ChatResult("c", "out", "model", null));
        var subscriber = subscriber(chatService);
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("ta"),
                threadStart("ta", subscriberMeta("conv-a", ID_A)));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("tb"),
                threadStart("tb", subscriberMeta("conv-b", ID_B)));

        subscriber.runMullingInferenceForTest("conversation", "ta");
        subscriber.runMullingInferenceForTest("conversation", "tb");
        subscriber.runMullingInferenceForTest("conversation", "ta");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chatService, times(3)).directChat(request.capture(), anyBoolean(), any());
        var seen = request.getAllValues();
        assertEquals(ID_A, seen.get(0).caller().principal().id());
        assertEquals(ID_B, seen.get(1).caller().principal().id());
        assertEquals(ID_A, seen.get(2).caller().principal().id());
        assertNotNull(seen.get(1).caller().conversationId());
        assertEquals("conv-b", seen.get(1).caller().conversationId());
    }
}
