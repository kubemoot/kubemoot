package ai.kubemoot.agent.util;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

/**
 * AcceleratorLabels returns a server IDENTIFIER derived from the endpoint/name, with no
 * hardcoded GPU-model or cluster-topology assumptions.
 */
class AcceleratorLabelsTest {

    @Test
    void endpointMapsToNamespaceServerId() {
        // host "ollama.ollama-rig1" -> namespace component "ollama-rig1".
        assertEquals("ollama-rig1",
                AcceleratorLabels.fromEndpoint("http://ollama.ollama-rig1:11434"));
        assertEquals("ollama-rig0",
                AcceleratorLabels.fromEndpoint("http://ollama.ollama-rig0:11434"));
    }

    @Test
    void fqdnEndpointMapsToNamespaceComponent() {
        assertEquals("ollama-rig1",
                AcceleratorLabels.fromEndpoint("http://ollama-rig1.ollama-rig1.svc.cluster.local:11434"));
    }

    @Test
    void nullOrBlankEndpointFallsBackToGenericLabel() {
        assertEquals("server", AcceleratorLabels.fromEndpoint(null));
        assertEquals("server", AcceleratorLabels.fromEndpoint(""));
    }

    @Test
    void hostWithoutNamespaceReturnsHost() {
        assertEquals("some-other-service", AcceleratorLabels.fromEndpoint("http://some-other-service:8080"));
    }

    // --- fromServer: the CR name IS the server identifier ---

    @Test
    void serverNameReturnedAsIs() {
        assertEquals("ollama-gpu", AcceleratorLabels.fromServer("ollama-gpu"));
        assertEquals("ollama-rig1", AcceleratorLabels.fromServer("ollama-rig1"));
        assertEquals("any-future-provider", AcceleratorLabels.fromServer("any-future-provider"));
    }

    @Test
    void emptyOrNullServerReturnsNull_soCallerKeepsStaticLabel() {
        // null/empty = no JIT pick (selector unwired / static fallback); the
        // caller must keep its endpoint-derived label rather than mislabel.
        assertNull(AcceleratorLabels.fromServer(null));
        assertNull(AcceleratorLabels.fromServer(""));
    }

    @Test
    void putSendsTheNewKeyAndTheDeprecatedOne() {
        var meta = new java.util.HashMap<String, Object>();
        AcceleratorLabels.put(meta, "server-b");
        assertEquals("server-b", meta.get("acceleratorLabel"));
        assertEquals("server-b", meta.get("gpuLabel"));
    }

    @Test
    void readPrefersTheNewKeyAndFallsBackToTheOldOne() {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        assertEquals("new", AcceleratorLabels.read(mapper.createObjectNode().put("acceleratorLabel", "new").put("gpuLabel", "old")));
        assertEquals("old", AcceleratorLabels.read(mapper.createObjectNode().put("gpuLabel", "old")));
        assertNull(AcceleratorLabels.read(mapper.createObjectNode()));
        assertNull(AcceleratorLabels.read(null));
    }
}
