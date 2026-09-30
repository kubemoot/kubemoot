package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/**
 * An analyst's side of the concurrence check: the request is addressed to it by
 * name, so it answers without a triage call, and a reply that starts with
 * CONCERN: is published as a concern.
 */
class DiscussionSubscriberConcurrenceTest {

    private static final ObjectMapper JSON = new ObjectMapper();
    private static final String SUBJECT = "kubemoot.discuss.ns-a.pilot.broadcast.t1";

    private final Connection conn = mock(Connection.class);
    private final ChatService chat = mock(ChatService.class);

    private DiscussionSubscriber analyst() {
        var properties = mock(AgentProperties.class, RETURNS_DEEP_STUBS);
        when(properties.agentName()).thenReturn("k8s-advisor");
        when(properties.discuss().role()).thenReturn("analyst");
        when(properties.discuss().tooler()).thenReturn(true);
        when(properties.discuss().priority()).thenReturn("medium");
        when(properties.discuss().channels()).thenReturn(Optional.of("kubernetes"));
        when(properties.discuss().jetstreamConsumer()).thenReturn(Optional.empty());
        when(properties.discuss().maxInferencesPerMinute()).thenReturn(10);
        when(properties.discuss().maxContributionsPerThread()).thenReturn(3);
        when(properties.discuss().triagePrompt()).thenReturn(Optional.empty());
        when(properties.enabledTools()).thenReturn(Optional.empty());
        when(properties.model().model()).thenReturn("qwen3:32b");
        var nats = mock(NatsConnectionProvider.class);
        when(nats.scope()).thenReturn(CrewScope.of("ns-a", "pilot"));
        when(nats.getConnection()).thenReturn(conn);
        return new DiscussionSubscriber(nats, chat, new DiscussionMetrics(new SimpleMeterRegistry()),
                properties, "http://localhost:11434");
    }

    private static String reviewReady(String mode, String... circle) throws Exception {
        var meta = JSON.createObjectNode();
        meta.putArray("innerCircle").addAll(List.of(circle).stream()
                .map(JSON.getNodeFactory()::textNode).toList());
        if (mode != null) meta.put("reviewMode", mode);
        var msg = JSON.createObjectNode();
        msg.put("messageId", "rr-" + mode);
        msg.put("threadId", "t1");
        msg.put("agentName", "coordinator");
        msg.put("messageType", "review_ready");
        msg.put("content", ReviewDecision.CONCURRENCE_REQUEST + "\n\n[k8s-config] default, kube-system");
        msg.set("metadata", meta);
        return JSON.writeValueAsString(msg);
    }

    private List<JsonNode> publishedSignals(int atLeast) throws Exception {
        var captor = ArgumentCaptor.forClass(byte[].class);
        verify(conn, timeout(3_000).atLeast(atLeast)).publish(anyString(), captor.capture());
        var out = new ArrayList<JsonNode>();
        for (byte[] b : captor.getAllValues()) out.add(JSON.readTree(b));
        return out;
    }

    private void analystReplies(String reply) {
        when(chat.directChat(any(ChatService.ChatRequest.class), eq(false), any()))
                .thenReturn(new ChatService.ChatResult("c", reply, "qwen3:32b", null, 10, 5, "", ""));
    }

    @Test
    void concurrenceRequest_answersWithoutTriage_andAConcernIsPublishedAsAConcern() throws Exception {
        analystReplies("CONCERN: the listing stops at 20 of 28 namespaces");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var signals = publishedSignals(2);
        var types = signals.stream().map(m -> m.path("messageType").asText()).toList();
        assertFalse(types.contains("triaging"), "no triage for a request addressed by name: " + types);
        assertTrue(types.contains("evaluating"), types.toString());
        var concern = signals.stream().filter(m -> "concern".equals(m.path("messageType").asText()))
                .findFirst().orElseThrow();
        assertEquals("the listing stops at 20 of 28 namespaces", concern.path("content").asText());
        verify(chat, never()).triageChat(anyString(), anyString());
    }

    @Test
    void concurrenceRequest_agreementIsAnAgree() throws Exception {
        analystReplies("Concur: the listing is complete.");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var signals = publishedSignals(2);
        assertTrue(signals.stream().anyMatch(m -> "agree".equals(m.path("messageType").asText())));
    }

    @Test
    void fullReview_stillTriagesFirst() throws Exception {
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChat(anyString(), anyString())).thenReturn("CONTRIBUTE");
        analystReplies("The namespaces are default and kube-system.");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("full", "k8s-advisor"));

        var types = publishedSignals(3).stream().map(m -> m.path("messageType").asText()).toList();
        assertTrue(types.contains("triaging"), types.toString());
    }

    @Test
    void aConcurrenceRequestForAnotherAnalyst_isIgnored() throws Exception {
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "obs-advisor"));

        verify(chat, after(300).never()).directChat(any(ChatService.ChatRequest.class), anyBoolean(), any());
        verify(conn, never()).publish(anyString(), any(byte[].class));
    }

    @Test
    void concurrenceRequestOnAClosedThread_standsAside() throws Exception {
        var sub = analyst();
        sub.handleMessageForTest(SUBJECT, """
                {"messageId": "close-1", "threadId": "t1", "agentName": "coordinator",
                 "messageType": "thread_close", "content": ""}""");

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        verify(chat, after(300).never()).directChat(any(ChatService.ChatRequest.class), anyBoolean(), any());
    }

    // ---- reply sentinels ----

    @Test
    void concernIn_readsBothConcernSentinels() {
        assertEquals("needs a Prometheus tool", DiscussionSubscriber.concernIn("TOOL_GAP: needs a Prometheus tool"));
        assertEquals("count is wrong", DiscussionSubscriber.concernIn("  CONCERN: count is wrong\n"));
        assertNull(DiscussionSubscriber.concernIn("I concur. No CONCERN: here"), "only a leading sentinel counts");
        assertNull(DiscussionSubscriber.concernIn("concern: lowercase is prose"), "case-sensitive");
        assertNull(DiscussionSubscriber.concernIn(null));
    }

    @Test
    void isConcurrenceRequest_readsTheReviewMode() throws Exception {
        var sub = analyst();
        assertTrue(sub.isConcurrenceRequest(reviewReady("concur", "k8s-advisor")));
        assertFalse(sub.isConcurrenceRequest(reviewReady("full", "k8s-advisor")));
        assertFalse(sub.isConcurrenceRequest(reviewReady(null, "k8s-advisor")));
        assertFalse(sub.isConcurrenceRequest("not json"));
    }
}
