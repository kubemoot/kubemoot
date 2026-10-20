package ai.kubemoot.agent.util;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

/**
 * GpuLabels returns a server IDENTIFIER derived from the endpoint/name, with no
 * hardcoded GPU-model or cluster-topology assumptions.
 */
class GpuLabelsTest {

    @Test
    void endpointMapsToNamespaceServerId() {
        // host "ollama.ollama-rig1" -> namespace component "ollama-rig1".
        assertEquals("ollama-rig1",
                GpuLabels.fromEndpoint("http://ollama.ollama-rig1:11434"));
        assertEquals("ollama-rig0",
                GpuLabels.fromEndpoint("http://ollama.ollama-rig0:11434"));
    }

    @Test
    void fqdnEndpointMapsToNamespaceComponent() {
        assertEquals("ollama-rig1",
                GpuLabels.fromEndpoint("http://ollama-rig1.ollama-rig1.svc.cluster.local:11434"));
    }

    @Test
    void nullOrBlankEndpointFallsBackToGenericLabel() {
        assertEquals("server", GpuLabels.fromEndpoint(null));
        assertEquals("server", GpuLabels.fromEndpoint(""));
    }

    @Test
    void hostWithoutNamespaceReturnsHost() {
        assertEquals("some-other-service", GpuLabels.fromEndpoint("http://some-other-service:8080"));
    }

    // --- fromServer: the CR name IS the server identifier ---

    @Test
    void serverNameReturnedAsIs() {
        assertEquals("ollama-gpu", GpuLabels.fromServer("ollama-gpu"));
        assertEquals("ollama-rig1", GpuLabels.fromServer("ollama-rig1"));
        assertEquals("any-future-provider", GpuLabels.fromServer("any-future-provider"));
    }

    @Test
    void emptyOrNullServerReturnsNull_soCallerKeepsStaticLabel() {
        // null/empty = no JIT pick (selector unwired / static fallback); the
        // caller must keep its endpoint-derived label rather than mislabel.
        assertNull(GpuLabels.fromServer(null));
        assertNull(GpuLabels.fromServer(""));
    }
}
