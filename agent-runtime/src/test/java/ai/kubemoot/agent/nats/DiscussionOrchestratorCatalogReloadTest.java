package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import java.util.List;
import java.util.Optional;
import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import java.nio.charset.StandardCharsets;

/**
 * The coordinator's crew capability catalog follows the operator's KV entry: a new
 * revision (an agent or skill added or removed) is reloaded at the next discussion,
 * an unchanged revision is served from cache, and a failed read keeps the last catalog.
 */
class DiscussionOrchestratorCatalogReloadTest {

    private DiscussionOrchestrator orchestrator;
    private ChatService chatService;
    private NatsConnectionProvider natsProvider;
    private KeyValue kv;
    private AgentProperties properties;

    private static final String ONE_AGENT = "[{\"name\":\"k8s-nodes\",\"description\":\"nodes\",\"role\":\"tooler\"}]";
    private static final String TWO_AGENTS = "[{\"name\":\"k8s-nodes\",\"description\":\"nodes\",\"role\":\"tooler\"},"
            + "{\"name\":\"web-search\",\"description\":\"web\",\"role\":\"researcher\"}]";

    @BeforeEach
    void setUp() throws Exception {
        natsProvider = mock(NatsConnectionProvider.class);
        chatService = mock(ChatService.class);
        var resumeSearchClient = mock(ResumeSearchClient.class);
        properties = mock(AgentProperties.class);
        when(properties.crew()).thenReturn(Optional.of("demo"));
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);

        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("test-coordinator");
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("kubernetes,proxmox"));
        when(discuss.settleSeconds()).thenReturn(5);
        when(discuss.minEvalSeconds()).thenReturn(20);
        when(discuss.minReviewSeconds()).thenReturn(10);
        when(discuss.advisoryTimeoutSeconds()).thenReturn(10);
        when(discuss.evaluationTimeoutSeconds()).thenReturn(15);
        when(discuss.reviewTimeoutSeconds()).thenReturn(15);
        when(discuss.synthesisTimeoutSeconds()).thenReturn(90);
        when(discuss.evalGraceSeconds()).thenReturn(10);
        when(discuss.conversationMaxTurns()).thenReturn(10);
        when(discuss.conversationTtlMinutes()).thenReturn(1440);
        when(discuss.conversationKvBucket()).thenReturn("kubemoot_conversations");
        when(discuss.resumePreFilterTopK()).thenReturn(5);
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);

        var metrics = new DiscussionMetrics(new SimpleMeterRegistry());
        var latencyTracker = mock(LatencyTracker.class);

        orchestrator = new DiscussionOrchestrator(
                natsProvider, properties, chatService, metrics, latencyTracker, resumeSearchClient);
        wireKv();
    }

    private void kvHolds(String json, long revision) throws Exception {
        var entry = mock(KeyValueEntry.class);
        when(entry.getValue()).thenReturn(json.getBytes(StandardCharsets.UTF_8));
        when(entry.getRevision()).thenReturn(revision);
        when(kv.get(anyString())).thenReturn(entry);
    }

    private void wireKv() throws Exception {
        kv = mock(KeyValue.class);
        var conn = mock(Connection.class);
        when(conn.keyValue(anyString())).thenReturn(kv);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
    }

    @Test
    void anAddedAgentIsSeenAtTheNextDiscussion() throws Exception {
        kvHolds(ONE_AGENT, 1);
        assertEquals(ONE_AGENT, orchestrator.loadCrewResumes());
        kvHolds(TWO_AGENTS, 2);
        assertEquals(TWO_AGENTS, orchestrator.loadCrewResumes());
    }

    @Test
    void anUnchangedRevisionIsServedFromCache() throws Exception {
        kvHolds(ONE_AGENT, 7);
        orchestrator.loadCrewResumes();
        kvHolds(TWO_AGENTS, 7);
        assertEquals(ONE_AGENT, orchestrator.loadCrewResumes(), "same revision, no reload");
    }

    @Test
    void aFailedOrEmptyReadKeepsTheLastCatalog() throws Exception {
        kvHolds(ONE_AGENT, 1);
        orchestrator.loadCrewResumes();
        when(kv.get(anyString())).thenReturn(null);
        assertEquals(ONE_AGENT, orchestrator.loadCrewResumes());
        when(kv.get(anyString())).thenThrow(new RuntimeException("nats down"));
        assertEquals(ONE_AGENT, orchestrator.loadCrewResumes());
    }

    @Test
    void noCrewConfiguredMeansNoCatalog() {
        when(properties.crew()).thenReturn(Optional.empty());
        assertNull(orchestrator.loadCrewResumes());
    }
}
