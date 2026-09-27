package ai.kubemoot.agent.memory;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.nats.CrewScope;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.Optional;
import java.util.regex.Matcher;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Unit tests for CrewMemoryClient parsing/sanitisation — no NATS. The KV I/O
 * path is a thin adapter (mirrors ProviderSelector) covered by integration
 * smoke after deploy; these lock the REMEMBER: directive grammar, key
 * sanitisation, and the no-op-when-NATS-down behaviour.
 */
class CrewMemoryClientTest {

    private static CrewMemoryClient clientWithoutNats() {
        return clientWithoutNats(CrewScope.of("ns-a", "homelab-pilot"));
    }

    private static CrewMemoryClient clientWithoutNats(CrewScope scope) {
        var nats = mock(NatsConnectionProvider.class);
        when(nats.isAvailable()).thenReturn(false); // memory ops become no-ops
        when(nats.scope()).thenReturn(scope);
        var props = mock(AgentProperties.class);
        when(props.crew()).thenReturn(Optional.of("homelab-pilot"));
        var mem = mock(AgentProperties.Memory.class);
        when(mem.enabled()).thenReturn(true);
        when(mem.maxFacts()).thenReturn(5000);
        when(mem.ttlDays()).thenReturn(365);
        when(mem.injectLimit()).thenReturn(8);
        when(mem.verifyOnAdd()).thenReturn(true);
        when(props.memory()).thenReturn(mem);
        return new CrewMemoryClient(nats, new ObjectMapper(), props);
    }

    @Test
    void rememberLine_parsesTopicKeyValue() {
        Matcher m = CrewMemoryClient.REMEMBER_LINE.matcher(
                "REMEMBER: gpu-topology | rig0 | exported_namespace=ollama-rig0, RTX 5090");
        assertTrue(m.find());
        assertEquals("gpu-topology", m.group(1));
        assertEquals("rig0", m.group(2));
        assertEquals("exported_namespace=ollama-rig0, RTX 5090", m.group(3));
    }

    @Test
    void rememberLine_ignoresNonDirectiveText() {
        assertFalse(CrewMemoryClient.REMEMBER_LINE.matcher(
                "The GPU on rig0 is at 0% utilization.").find());
    }

    @Test
    void persistFromResponse_stripsDirectivesFromUserText() {
        var c = clientWithoutNats();
        String resp = "GPU on rig0 is at 0% (idle).\n"
                + "REMEMBER: gpu-topology | rig0 | exported_namespace=ollama-rig0\n"
                + "Let me know if you need history.";
        String cleaned = c.persistFromResponse(resp, "obs-metrics");
        assertFalse(cleaned.contains("REMEMBER:"), "directive must be stripped from user-facing text");
        assertTrue(cleaned.contains("GPU on rig0 is at 0%"));
        assertTrue(cleaned.contains("Let me know if you need history."));
    }

    @Test
    void persistFromResponse_noDirective_returnsUnchanged() {
        var c = clientWithoutNats();
        String resp = "GPU on rig0 is at 0% utilization.";
        assertEquals(resp, c.persistFromResponse(resp, "obs-metrics"));
    }

    @Test
    void persistFromResponse_nullSafe() {
        var c = clientWithoutNats();
        assertNull(c.persistFromResponse(null, "a"));
        assertEquals("", c.persistFromResponse("", "a"));
    }

    @Test
    void sanitize_keepsSafeKeyChars_replacesRest() {
        // NATS KV keys allow [A-Za-z0-9-_/=.]; we keep [A-Za-z0-9_=-], replace others.
        assertEquals("ollama-rig0", CrewScope.kvToken("ollama-rig0"));
        assertEquals("gpu_topology", CrewScope.kvToken("gpu topology"));
        assertEquals("a_b_c", CrewScope.kvToken("a/b.c"));
        assertEquals("_", CrewScope.kvToken(""));
        assertEquals("_", CrewScope.kvToken(null));
    }

    @Test
    void natsKey_isNamespaceAndCrewScopedAndDotSeparated() {
        var c = clientWithoutNats();
        assertEquals("ns-a.homelab-pilot.gpu-topology.rig0", c.natsKey("gpu-topology", "rig0"));
    }

    @Test
    void natsKey_sameCrewInTwoNamespaces_differs() {
        var a = clientWithoutNats(CrewScope.of("ns-a", "homelab-pilot"));
        var b = clientWithoutNats(CrewScope.of("ns-b", "homelab-pilot"));
        assertNotEquals(a.natsKey("gpu-topology", "rig0"), b.natsKey("gpu-topology", "rig0"));
        assertEquals("ns-b.homelab-pilot.gpu-topology.rig0", b.natsKey("gpu-topology", "rig0"));
    }

    @Test
    void natsKey_crewlessAgent_usesDefaultCrewSegment() {
        var c = clientWithoutNats(CrewScope.of("ns-a", null));
        assertEquals("ns-a.default.t.k", c.natsKey("t", "k"));
    }

    @Test
    void recallForContext_emptyWhenNatsUnavailable() {
        assertEquals("", clientWithoutNats().recallForContext("what's GPU utilization on rig0?"));
    }

    @Test
    void tokens_lowercasesAndDropsShort() {
        var t = CrewMemoryClient.tokens("What's GPU utilization on rig0?");
        assertTrue(t.contains("gpu"));
        assertTrue(t.contains("utilization"));
        assertTrue(t.contains("rig0"));
        assertFalse(t.contains("on"), "short tokens (<3 chars) dropped");
    }

    @Test
    void tokens_nullOrBlank_returnsEmptySet() {
        assertTrue(CrewMemoryClient.tokens(null).isEmpty());
        assertTrue(CrewMemoryClient.tokens("   ").isEmpty());
        assertTrue(CrewMemoryClient.tokens("a b").isEmpty(), "all tokens shorter than 3 chars");
    }

    @Test
    void persistFromResponse_stripsMultipleDirectives() {
        var c = clientWithoutNats();
        String resp = "Summary line.\n"
                + "REMEMBER: gpu-topology | rig0 | exported_namespace=ollama-rig0\n"
                + "REMEMBER: gpu-topology | rig1 | exported_namespace=ollama-rig1\n"
                + "Tail line.";
        String cleaned = c.persistFromResponse(resp, "obs-metrics");
        assertFalse(cleaned.contains("REMEMBER:"), "all directives stripped");
        assertTrue(cleaned.contains("Summary line."));
        assertTrue(cleaned.contains("Tail line."));
        assertFalse(cleaned.contains("rig0"));
        assertFalse(cleaned.contains("rig1"));
    }

    @Test
    void recallForContext_nullQuery_emptyWhenNatsUnavailable() {
        // null query is the recency-only path; still empty with NATS down.
        assertEquals("", clientWithoutNats().recallForContext(null));
    }
}
