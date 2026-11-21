package ai.kubemoot.agent.mcp;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.gateway.GatewayClient;
import ai.kubemoot.agent.requester.Caller;
import ai.kubemoot.agent.requester.CallerScope;
import ai.kubemoot.agent.requester.Principal;
import ai.kubemoot.agent.requester.Requester;
import ai.kubemoot.agent.requester.RequesterScope;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * Every gateway tool call carries the turn's conversation and principal ids in {@code _meta}, set
 * by the runtime from the bound caller and never from anything the model supplies.
 */
class McpClientServiceCallerTest {

    private static final String PERSON_ID = "4f9c0b1e7a2d83c65b1f0e9d8c7b6a5948372615c4d3e2f1a0b9c8d7e6f50413";

    private GatewayClient gateway;
    private McpClientService service;

    @BeforeEach
    void setUp() {
        var props = mock(AgentProperties.class);
        var gatewayProps = mock(AgentProperties.Gateway.class);
        when(props.gateway()).thenReturn(gatewayProps);
        when(gatewayProps.enabled()).thenReturn(true);
        when(gatewayProps.endpoint()).thenReturn(Optional.of("http://gateway:8080"));
        when(props.mcpServers()).thenReturn(Optional.empty());
        when(props.enabledTools()).thenReturn(Optional.empty());
        when(props.disabledTools()).thenReturn(Optional.empty());
        gateway = mock(GatewayClient.class);
        when(gateway.isConfigured()).thenReturn(true);
        when(gateway.listTools()).thenReturn(List.of());
        when(gateway.callTool(any(), any(), any()))
                .thenReturn(new GatewayClient.ToolCallResult("t", true, "ok", null));
        service = new McpClientService(props);
        service.gatewayClient = gateway;
        service.initialize();
    }

    private static Caller subscriber(String conversation, String id) {
        return new Caller(conversation, new Principal("subscriber", "oreilly", id, null));
    }

    private Map<String, Object> lastMeta() {
        return lastCall().meta();
    }

    private record Sent(Map<String, Object> arguments, Map<String, Object> meta) {}

    @SuppressWarnings("unchecked")
    private Sent lastCall() {
        ArgumentCaptor<Map<String, Object>> args = ArgumentCaptor.forClass(Map.class);
        ArgumentCaptor<Map<String, Object>> meta = ArgumentCaptor.forClass(Map.class);
        verify(gateway, atLeastOnce()).callTool(eq("t"), args.capture(), meta.capture());
        return new Sent(args.getValue(), meta.getValue());
    }

    @Test
    void aSubscribersCallCarriesTheConversationAndPrincipalIds() {
        try (var scope = CallerScope.bind(subscriber("conv-1", PERSON_ID))) {
            service.callTool("t", Map.of("q", "kubernetes"));
        }
        assertEquals(Map.of("conversationId", "conv-1", "principalId", PERSON_ID), lastMeta());
    }

    @Test
    void anAnonymousTurnSendsTheConversationAndNoPrincipalId() {
        var anonymous = new Caller("conv-2", new Principal("anonymous", null, null, null));
        try (var scope = CallerScope.bind(anonymous)) {
            service.callTool("t", Map.of());
        }
        assertEquals(Map.of("conversationId", "conv-2"), lastMeta());
    }

    @Test
    void aKubernetesPrincipalCarriesItsIdTheSameWay() {
        var caller = new Caller("conv-3", new Principal("kubernetes", "kubernetes", "abc123", "ci-bot"));
        try (var scope = CallerScope.bind(caller)) {
            service.callTool("t", Map.of());
        }
        assertEquals(Map.of("conversationId", "conv-3", "principalId", "abc123"), lastMeta());
    }

    @Test
    void aTurnWithoutACallerSendsNoMeta() {
        service.callTool("t", Map.of());
        assertTrue(lastMeta().isEmpty());
    }

    @Test
    void theIdsAreSentOnlyForTheTurnTheyWereBoundFor() {
        try (var scope = CallerScope.bind(subscriber("conv-1", PERSON_ID))) {
            service.callTool("t", Map.of());
        }
        service.callTool("t", Map.of());
        assertTrue(lastMeta().isEmpty(), "the call after the turn carries nothing");
    }

    @Test
    void argumentsNamedLikeTheIdsStayArgumentsAndNeverReachMeta() {
        Map<String, Object> modelArguments = Map.of(
                "conversationId", "someone-elses-conversation",
                "principalId", "someone-elses-id",
                "_meta", Map.of("principalId", "forged"));
        try (var scope = CallerScope.bind(subscriber("conv-1", PERSON_ID))) {
            service.callTool("t", modelArguments);
        }
        var sent = lastCall();
        assertEquals(Map.of("conversationId", "conv-1", "principalId", PERSON_ID), sent.meta());
        assertEquals(modelArguments, sent.arguments(), "the model's arguments go through as they are");
    }

    @Test
    void aModelCannotAddIdsToATurnThatHasNoCaller() {
        service.callTool("t", Map.of("conversationId", "x", "principalId", "y"));
        var sent = lastCall();
        assertTrue(sent.meta().isEmpty());
        assertEquals(Map.of("conversationId", "x", "principalId", "y"), sent.arguments());
    }

    @Test
    void theCallersKeysWinOverAnyOtherMetaEntry() {
        var merged = McpClientService.mergeMeta(
                Map.of("principalId", "other", "timeZone", "UTC"),
                subscriber("conv-1", PERSON_ID).toolMeta());
        assertEquals(PERSON_ID, merged.get("principalId"));
        assertEquals("UTC", merged.get("timeZone"));
    }

    @Test
    void theRequestersFieldsAndTheCallersIdsRideTogether() {
        var requester = Requester.of("America/Los_Angeles", "2026-10-24T09:00:00").orElseThrow();
        try (var r = RequesterScope.bind(requester); var c = CallerScope.bind(subscriber("conv-1", PERSON_ID))) {
            service.callTool("t", Map.of());
        }
        var meta = lastMeta();
        assertEquals("America/Los_Angeles", meta.get("timeZone"));
        assertEquals("2026-10-24", meta.get("requestDate"));
        assertEquals("conv-1", meta.get("conversationId"));
        assertEquals(PERSON_ID, meta.get("principalId"));
    }

    @Test
    void noTokenAppearsInAnyCallsMeta() {
        try (var scope = CallerScope.bind(subscriber("conv-1", PERSON_ID))) {
            service.callTool("t", Map.of());
        }
        assertFalse(lastMeta().toString().toLowerCase().contains("token"));
    }

    @Test
    void twoInterleavedTurnsEachCarryTheirOwnIds() throws Exception {
        var bothBound = new CountDownLatch(2);
        ExecutorService pool = Executors.newFixedThreadPool(2);
        try {
            Future<?> a = pool.submit(() -> turn("conv-a", "person-a", bothBound, "arg-a"));
            Future<?> b = pool.submit(() -> turn("conv-b", "person-b", bothBound, "arg-b"));
            a.get(10, TimeUnit.SECONDS);
            b.get(10, TimeUnit.SECONDS);
        } finally {
            pool.shutdownNow();
        }
        @SuppressWarnings("unchecked")
        ArgumentCaptor<Map<String, Object>> args = ArgumentCaptor.forClass(Map.class);
        @SuppressWarnings("unchecked")
        ArgumentCaptor<Map<String, Object>> meta = ArgumentCaptor.forClass(Map.class);
        verify(gateway, org.mockito.Mockito.times(2)).callTool(eq("t"), args.capture(), meta.capture());
        for (int i = 0; i < 2; i++) {
            String tag = (String) args.getAllValues().get(i).get("who");
            String suffix = tag.substring(tag.length() - 1);
            assertEquals(Map.of("conversationId", "conv-" + suffix, "principalId", "person-" + suffix),
                    meta.getAllValues().get(i), "the call for " + tag + " carries its own person");
        }
    }

    private void turn(String conversation, String person, CountDownLatch bothBound, String tag) {
        try (var scope = CallerScope.bind(subscriber(conversation, person))) {
            bothBound.countDown();
            if (!bothBound.await(5, TimeUnit.SECONDS)) {
                throw new IllegalStateException("the other turn never started");
            }
            service.callTool("t", Map.of("who", tag));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
