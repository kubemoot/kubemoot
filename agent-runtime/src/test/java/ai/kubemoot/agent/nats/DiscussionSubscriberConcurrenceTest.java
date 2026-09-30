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
 * name, so it answers without a triage call, in one tool-free turn (never the tool
 * loop). The reply is a verdict: CONCUR: is agreement, CONCERN: is a concern, and an
 * empty, failed, or verdict-less reply is a failure signal, which the coordinator
 * escalates to the full review.
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
        return reviewReadyWith(mode, "[k8s-config] default, kube-system", circle);
    }

    private static String reviewReadyWith(String mode, String results, String... circle) throws Exception {
        var meta = JSON.createObjectNode();
        meta.putArray("innerCircle").addAll(List.of(circle).stream()
                .map(JSON.getNodeFactory()::textNode).toList());
        if (mode != null) meta.put("reviewMode", mode);
        var msg = JSON.createObjectNode();
        msg.put("messageId", "rr-" + mode);
        msg.put("threadId", "t1");
        msg.put("agentName", "coordinator");
        msg.put("messageType", "review_ready");
        msg.put("content", ReviewDecision.CONCURRENCE_REQUEST + "\n\n" + results);
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

    /** The analyst's tool-loop reply (a full review's evaluation). */
    private void analystReplies(String reply) {
        when(chat.directChat(any(ChatService.ChatRequest.class), eq(false), any()))
                .thenReturn(new ChatService.ChatResult("c", reply, "qwen3:32b", null, 10, 5, "", ""));
    }

    /** The analyst's one tool-free turn (a concurrence reply). */
    private void concurrenceReplies(String reply) {
        when(chat.answerOnce(anyString(), anyString(), any()))
                .thenReturn(new ChatService.ChatResult("t1", reply, "qwen3:14b", "t1", 10, 5, "", ""));
    }

    private static JsonNode signal(List<JsonNode> signals, String type) {
        return signals.stream().filter(m -> type.equals(m.path("messageType").asText()))
                .findFirst().orElseThrow(() -> new AssertionError("no " + type + " signal in " + signals));
    }

    @Test
    void concurrenceRequest_isOneToolFreeTurn_neverTheToolLoop() throws Exception {
        concurrenceReplies("CONCUR: the listing is complete.");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        publishedSignals(2);
        verify(chat, timeout(3_000).times(1)).answerOnce(eq("t1"), anyString(), any());
        verify(chat, never()).directChat(any(ChatService.ChatRequest.class), anyBoolean(), any());
        verify(chat, never()).directChat(any(ChatService.ChatRequest.class), anyBoolean());
        verify(chat, never()).triageChat(anyString(), anyString());
    }

    @Test
    void concurrenceRequest_answersWithoutTriage_andAConcernIsPublishedAsAConcern() throws Exception {
        concurrenceReplies("CONCERN: the listing stops at 20 of 28 namespaces");
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
    void concurrenceRequest_agreementIsAnAgree_carryingOnlyTheCaveat() throws Exception {
        concurrenceReplies("CONCUR: the listing is complete.");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var agree = signal(publishedSignals(2), "agree");
        assertEquals("the listing is complete.", agree.path("content").asText());
    }

    @Test
    void concurrenceRequest_bareConcur_isAnAgreeWithTheStandardText() throws Exception {
        concurrenceReplies("CONCUR:");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var agree = signal(publishedSignals(2), "agree");
        assertEquals(ConcurrenceReply.CONCURS, agree.path("content").asText());
    }

    @Test
    void concurrenceRequest_freeFormAnswerIsNoVerdict_soTheCoordinatorEscalates() throws Exception {
        // An analyst that answers the question instead of giving a verdict is not
        // agreeing: its claims must not reach the synthesis as agreement.
        concurrenceReplies("The etcd leader pod is hosted on the Proxmox VM homelab-k8s-1-cp; "
                + "disk write is 1.68 MB/s.");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var signals = publishedSignals(2);
        var failure = signal(signals, "failure");
        assertEquals(ConcurrenceReply.NO_VERDICT, failure.path("content").asText());
        assertEquals("no_verdict", failure.path("metadata").path("failureType").asText());
        assertTrue(signals.stream().noneMatch(m -> "agree".equals(m.path("messageType").asText())),
                "a free-form answer is never published as agreement");
    }

    @Test
    void concurrenceRequest_toolGapIsAConcern() throws Exception {
        concurrenceReplies("TOOL_GAP: no tool lists the installed CNI");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        assertEquals("no tool lists the installed CNI",
                signal(publishedSignals(2), "concern").path("content").asText());
    }

    @Test
    void concurrenceRequest_threadClosedDuringTheTurn_standsAside_neverAVerdict() throws Exception {
        var sub = analyst();
        when(chat.answerOnce(anyString(), anyString(), any())).thenAnswer(inv -> {
            sub.handleMessageForTest(SUBJECT, """
                    {"messageId": "close-2", "threadId": "t1", "agentName": "coordinator",
                     "messageType": "thread_close", "content": ""}""");
            return new ChatService.ChatResult("t1", "CONCUR: fine", "qwen3:14b", "t1", 10, 5, "", "");
        });

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        verify(conn, after(300).atLeast(2)).publish(anyString(), any(byte[].class));
        var types = publishedSignals(2).stream().map(m -> m.path("messageType").asText()).toList();
        assertTrue(types.contains("stand_aside"), types.toString());
        assertFalse(types.contains("agree"), "a closed thread takes no verdict: " + types);
    }

    @Test
    void concurrenceRequest_lowercaseConcurIsNoVerdict() throws Exception {
        concurrenceReplies("Concur. The gathered data answers the question.");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        assertEquals("no_verdict", signal(publishedSignals(2), "failure").path("metadata")
                .path("failureType").asText());
    }

    @Test
    void concurrenceRequest_emptyReplyIsAFailure_soTheCoordinatorEscalates() throws Exception {
        concurrenceReplies("   ");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var failure = signal(publishedSignals(2), "failure");
        assertEquals(ConcurrenceReply.EMPTY_REPLY, failure.path("content").asText());
        assertEquals("empty_reply", failure.path("metadata").path("failureType").asText());
    }

    @Test
    void concurrenceRequest_nothingToAddIsNoVerdict_soTheCoordinatorEscalates() throws Exception {
        // The concurrer is asked by name for a verdict; standing aside would send the
        // thread to synthesis with no check at all.
        concurrenceReplies("NOTHING_TO_ADD");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        assertEquals("no_verdict", signal(publishedSignals(2), "failure").path("metadata")
                .path("failureType").asText());
    }

    @Test
    void fullReview_emptyReplyStaysAStandAside() throws Exception {
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChat(anyString(), anyString())).thenReturn("CONTRIBUTE");
        analystReplies("");
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("full", "k8s-advisor"));

        var types = publishedSignals(3).stream().map(m -> m.path("messageType").asText()).toList();
        assertTrue(types.contains("stand_aside"), types.toString());
        assertFalse(types.contains("failure"), "only a concurrence reply fails on empty: " + types);
    }

    @Test
    void concurrenceRequest_failedTurnIsAFailure() throws Exception {
        when(chat.answerOnce(anyString(), anyString(), any()))
                .thenThrow(new IllegalStateException("Connection refused"));
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var failure = signal(publishedSignals(2), "failure");
        assertEquals("provider_unreachable", failure.path("metadata").path("failureType").asText());
    }

    @Test
    void concurrenceRequest_noGpuFit_standsAsideWithTheReason() throws Exception {
        when(chat.answerOnce(anyString(), anyString(), any()))
                .thenThrow(ai.kubemoot.agent.provider.NoFitException.modelTooLarge("qwen3:14b", "too big"));
        var sub = analyst();

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        var standAside = signal(publishedSignals(2), "stand_aside");
        assertEquals("model-too-large", standAside.path("metadata").path("reason").asText());
    }

    @Test
    void concurrenceRequest_turnReadsTheSpilledArtifactContent() throws Exception {
        var os = mock(io.nats.client.ObjectStore.class);
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenReturn(os);
        String fullData = "namespaces: arc-runners, cert-manager, harbor (28 total)";
        doAnswer(inv -> {
            ((java.io.OutputStream) inv.getArgument(1))
                    .write(fullData.getBytes(java.nio.charset.StandardCharsets.UTF_8));
            return null;
        }).when(os).get(anyString(), any(java.io.OutputStream.class));
        concurrenceReplies("CONCUR:");
        var sub = analyst();
        String key = "ns-a/pilot/t1/k8s-config/agree-abc";

        sub.handleMessageForTest(SUBJECT, reviewReadyWith("concur",
                "[k8s-config] [ARTIFACT key=" + key + " bytes=64 - the FULL data is in /artifacts/" + key + "]",
                "k8s-advisor"));

        var message = ArgumentCaptor.forClass(String.class);
        verify(chat, timeout(3_000)).answerOnce(eq("t1"), message.capture(), any());
        assertTrue(message.getValue().contains(fullData), message.getValue());
        assertTrue(message.getValue().contains(ReviewDecision.CONCURRENCE_REQUEST), "the request itself is in the turn");
        assertFalse(message.getValue().contains("[ARTIFACT key="), "no marker the turn cannot open");
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

        verify(chat, after(300).never()).answerOnce(anyString(), anyString(), any());
        verify(chat, never()).directChat(any(ChatService.ChatRequest.class), anyBoolean(), any());
        verify(conn, never()).publish(anyString(), any(byte[].class));
    }

    @Test
    void concurrenceRequestOnAClosedThread_standsAside() throws Exception {
        var sub = analyst();
        sub.handleMessageForTest(SUBJECT, """
                {"messageId": "close-1", "threadId": "t1", "agentName": "coordinator",
                 "messageType": "thread_close", "content": ""}""");

        sub.handleMessageForTest(SUBJECT, reviewReady("concur", "k8s-advisor"));

        verify(chat, after(300).never()).answerOnce(anyString(), anyString(), any());
    }

    // ---- reply sentinels ----

    @Test
    void concurrenceMessage_withoutAMarker_isTheThreadUnchanged() throws Exception {
        var sub = analyst();
        assertEquals("the thread", sub.concurrenceMessage("t1", "the thread"));
        verify(conn, never()).objectStore(anyString());
    }

    @Test
    void concurrenceMessage_unreadableArtifact_isMarkedUnavailable() throws Exception {
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenThrow(new java.io.IOException("gone"));
        var sub = analyst();

        String out = sub.concurrenceMessage("t1", "[k8s-config] [ARTIFACT key=a/b bytes=9]");

        assertTrue(out.contains("ARTIFACT UNAVAILABLE key=a/b"), out);
    }

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
