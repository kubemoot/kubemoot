package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** Scope resolution in the shared NATS server; no NATS server is contacted. */
class NatsConnectionProviderTest {

    private static AgentProperties props(String natsUrl, String namespace, String crew) {
        var props = mock(AgentProperties.class);
        var nats = mock(AgentProperties.Nats.class);
        when(nats.url()).thenReturn(Optional.ofNullable(natsUrl));
        when(props.nats()).thenReturn(nats);
        when(props.agentName()).thenReturn("k8s-config");
        when(props.namespace()).thenReturn(Optional.ofNullable(namespace));
        when(props.crew()).thenReturn(Optional.ofNullable(crew));
        return props;
    }

    @Test
    void configured_scopeCarriesNamespaceAndCrew() {
        var server = new NatsConnectionProvider(props("nats://nats:4222", "team-a", "homelab-pilot"));
        assertEquals(CrewScope.of("team-a", "homelab-pilot"), server.scope());
    }

    @Test
    void notConfigured_hasNoScope() {
        var server = new NatsConnectionProvider(props(null, "team-a", "homelab-pilot"));
        assertFalse(server.isConfigured());
        assertThrows(IllegalStateException.class, server::scope);
    }
}
