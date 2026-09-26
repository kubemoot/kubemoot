package ai.kubemoot.agent.util;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

/**
 * GpuLabels returns a provider IDENTIFIER derived from the endpoint/name, with no
 * hardcoded GPU-model or cluster-topology assumptions.
 */
class GpuLabelsTest {

    @Test
    void endpointMapsToNamespaceProviderId() {
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
        assertEquals("provider", GpuLabels.fromEndpoint(null));
        assertEquals("provider", GpuLabels.fromEndpoint(""));
    }

    @Test
    void hostWithoutNamespaceReturnsHost() {
        assertEquals("some-other-service", GpuLabels.fromEndpoint("http://some-other-service:8080"));
    }

    // --- fromProvider: the CR name IS the provider identifier ---

    @Test
    void providerNameReturnedAsIs() {
        assertEquals("ollama-gpu", GpuLabels.fromProvider("ollama-gpu"));
        assertEquals("ollama-rig1", GpuLabels.fromProvider("ollama-rig1"));
        assertEquals("any-future-provider", GpuLabels.fromProvider("any-future-provider"));
    }

    @Test
    void emptyOrNullProviderReturnsNull_soCallerKeepsStaticLabel() {
        // null/empty = no JIT pick (selector unwired / static fallback); the
        // caller must keep its endpoint-derived label rather than mislabel.
        assertNull(GpuLabels.fromProvider(null));
        assertNull(GpuLabels.fromProvider(""));
    }
}
