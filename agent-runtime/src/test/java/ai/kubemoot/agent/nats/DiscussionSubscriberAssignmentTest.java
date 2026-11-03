package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * A second review that assigns an analyst stored calls gives it the assignment as its whole task: not the
 * thread of the first review, and not the conversation it kept from answering that review.
 */
class DiscussionSubscriberAssignmentTest {

    private static final CrewScope SCOPE = CrewScope.of("ns-test", "crew-x");
    private static final String TASK = "DEFINE DOMAIN review-second\n[ASSIGN analyst=compute pattern=p/*-pods_list calls=2]";

    private static DiscussionSubscriber subscriber(ChatService chatService) {
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn("compute");
        var discuss = mock(AgentProperties.Discuss.class);
        when(discuss.role()).thenReturn("analyst");
        when(discuss.maxInferencesPerMinute()).thenReturn(100);
        when(props.discuss()).thenReturn(discuss);
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.getConnection()).thenReturn(mock(Connection.class));
        when(natsProvider.scope()).thenReturn(SCOPE);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chatService, metrics, props, "http://localhost:11434");
    }

    private static String message(String type, String agent, String id, String content, String metadata) {
        return "{\"messageType\":\"" + type + "\",\"threadId\":\"t1\",\"agentName\":\"" + agent
                + "\",\"messageId\":\"" + id + "\",\"content\":" + json(content)
                + (metadata == null ? "" : ",\"metadata\":" + metadata) + "}";
    }

    private static String json(String text) {
        return "\"" + text.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n") + "\"";
    }

    private static void firstReview(DiscussionSubscriber sub) throws Exception {
        sub.admitForEvaluation(message("thread_start", "human", "m0", "Which node is out of disk?", "{\"userQuery\":\"q\"}"));
        sub.admitForEvaluation(message("agree", "k8s-workloads", "m1", "pods and events", null));
    }

    private static String secondReviewMetadata(String analyst) {
        return "{\"innerCircle\":[\"" + analyst + "\"],\"tasks\":{\"" + analyst + "\":" + json(TASK) + "}}";
    }

    @Test
    void theFirstReviewShowsTheThread() throws Exception {
        var sub = subscriber(mock(ChatService.class));
        firstReview(sub);

        var evaluation = sub.admitForEvaluation(message("review_ready", "coordinator", "m2", "[k8s-workloads] pods", null));

        assertTrue(evaluation.conversation().contains("Which node is out of disk?"), evaluation.conversation());
        assertTrue(evaluation.conversation().contains("--- Thread conversation ---"));
    }

    @Test
    void aSecondReviewThatAssignsTheAgentIsItsWholeTaskAndNotTheThread() throws Exception {
        var sub = subscriber(mock(ChatService.class));
        firstReview(sub);
        sub.admitForEvaluation(message("review_ready", "coordinator", "m2", "[k8s-workloads] pods", null));

        var evaluation = sub.admitForEvaluation(
                message("review_ready", "coordinator", "m3", TASK, secondReviewMetadata("compute")));

        assertEquals(TASK, evaluation.conversation());
        assertFalse(evaluation.conversation().contains("Which node is out of disk?"));
        assertTrue(evaluation.selected());
    }

    @Test
    void aSecondReviewThatAssignsOnlyOtherAnalystsShowsTheThread() throws Exception {
        var sub = subscriber(mock(ChatService.class));
        firstReview(sub);

        var evaluation = sub.admitForEvaluation(message("review_ready", "coordinator", "m3", TASK,
                "{\"tasks\":{\"someone-else\":" + json(TASK) + "}}"));

        assertTrue(evaluation.conversation().contains("--- Thread conversation ---"));
        assertTrue(evaluation.conversation().contains("Which node is out of disk?"));
    }

    @Test
    void anEmptyTaskIsNotATask() throws Exception {
        var sub = subscriber(mock(ChatService.class));
        firstReview(sub);

        var evaluation = sub.admitForEvaluation(message("review_ready", "coordinator", "m3", "x",
                "{\"tasks\":{\"compute\":\"  \"}}"));

        assertTrue(evaluation.conversation().contains("--- Thread conversation ---"));
    }

    @Test
    void theAssignmentRunsInItsOwnConversationAndTheFirstReviewInTheThreads() throws Exception {
        var chat = mock(ChatService.class);
        when(chat.directChat(any(ChatService.ChatRequest.class), anyBoolean(), any()))
                .thenReturn(new ChatService.ChatResult("c", "out", "model", null));
        var sub = subscriber(chat);
        firstReview(sub);
        sub.admitForEvaluation(message("review_ready", "coordinator", "m2", "first", null));
        sub.runMullingInferenceForTest("first review", "t1");
        sub.admitForEvaluation(message("review_ready", "coordinator", "m3", TASK, secondReviewMetadata("compute")));
        sub.runMullingInferenceForTest(TASK, "t1");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chat, times(2)).directChat(request.capture(), anyBoolean(), any());
        List<ChatService.ChatRequest> sent = request.getAllValues();
        assertEquals("t1", sent.get(0).conversationId());
        assertEquals("t1#assignment", sent.get(1).conversationId());
        assertEquals("t1", sent.get(1).discussionThreadId(), "tools and the thread's artifacts still belong to the thread");
    }

    @Test
    void aClosedThreadForgetsItsAssignment() throws Exception {
        var chat = mock(ChatService.class);
        when(chat.directChat(any(ChatService.ChatRequest.class), anyBoolean(), any()))
                .thenReturn(new ChatService.ChatResult("c", "out", "model", null));
        var sub = subscriber(chat);
        firstReview(sub);
        sub.admitForEvaluation(message("review_ready", "coordinator", "m3", TASK, secondReviewMetadata("compute")));
        sub.admitForEvaluation(message("synthesis", "coordinator", "m4", "answer", null));

        sub.runMullingInferenceForTest("x", "t1");

        var request = ArgumentCaptor.forClass(ChatService.ChatRequest.class);
        verify(chat).directChat(request.capture(), anyBoolean(), any());
        assertEquals("t1", request.getValue().conversationId());
    }
}
