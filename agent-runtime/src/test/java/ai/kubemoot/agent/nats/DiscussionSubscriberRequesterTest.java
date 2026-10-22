package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The thread keeps the requester its thread_start carried and passes it to the agent's model turn. */
class DiscussionSubscriberRequesterTest {

    private static final CrewScope SCOPE = CrewScope.of("ns-test", "crew-x");

    private static String threadStart(String threadId, String metadata) {
        return "{\"messageType\":\"thread_start\",\"threadId\":\"" + threadId
                + "\",\"agentName\":\"human\",\"content\":\"What is new?\","
                + "\"metadata\":" + metadata + ",\"messageId\":\"m-" + threadId + "\"}";
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
    void aThreadStartWithARequesterIsRemembered() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t1"), threadStart("t1",
                "{\"userQuery\":\"q\",\"requester\":{\"timeZone\":\"America/Los_Angeles\","
                        + "\"localDateTime\":\"2026-10-24T09:00:00\"}}"));
        var requester = subscriber.requesterFor("t1");
        assertEquals("America/Los_Angeles", requester.timeZone());
        assertEquals("2026-10-24", requester.requestDate());
    }

    @Test
    void aThreadStartWithoutARequesterHasNone() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t2"), threadStart("t2", "{\"userQuery\":\"q\"}"));
        assertNull(subscriber.requesterFor("t2"));
    }

    @Test
    void anInvalidRequesterIsIgnored() {
        var subscriber = subscriber(mock(ChatService.class));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t3"), threadStart("t3",
                "{\"requester\":{\"timeZone\":\"Not/AZone\",\"localDateTime\":\"tomorrow\"}}"));
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t4"), threadStart("t4",
                "{\"requester\":\"America/New_York\"}"));
        assertNull(subscriber.requesterFor("t3"));
        assertNull(subscriber.requesterFor("t4"));
    }

    @Test
    void theModelTurnReceivesTheThreadsRequester() {
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean(), any()))
                .thenReturn(new ChatService.ChatResult("c", "out", "model", null));
        var subscriber = subscriber(chatService);
        subscriber.handleMessageForTest(SCOPE.broadcastSubject("t5"), threadStart("t5",
                "{\"requester\":{\"timeZone\":\"Europe/Paris\"}}"));

        subscriber.runMullingInferenceForTest("conversation", "t5");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chatService).directChat(request.capture(), anyBoolean(), any());
        assertEquals("Europe/Paris", request.getValue().requester().timeZone());
    }
}
