package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Pins every namespace-scoped NATS format the agent-runtime builds and parses. The
 * operator, discussion gateway, and dashboard produce and parse the same strings, so
 * a change here is a cross-component protocol change.
 */
class CrewScopeTest {

    private static final CrewScope A = CrewScope.of("team-a", "homelab-pilot");
    private static final CrewScope B = CrewScope.of("team-b", "homelab-pilot");
    private static final CrewScope CREWLESS = CrewScope.of("team-a", null);

    // --- build ---

    @Test
    void discussionSubjects_carryNamespaceThenCrew() {
        assertEquals("kubemoot.discuss.team-a.homelab-pilot.", A.discussPrefix());
        assertEquals("kubemoot.discuss.team-a.homelab-pilot.>", A.discussWildcard());
        assertEquals("kubemoot.discuss.team-a.homelab-pilot.kubernetes.>", A.channelWildcard(" kubernetes "));
        assertEquals("kubemoot.discuss.team-a.homelab-pilot.kubernetes.t1", A.discussSubject("kubernetes", "t1"));
        assertEquals("kubemoot.discuss.team-a.homelab-pilot.broadcast.t1", A.broadcastSubject("t1"));
    }

    @Test
    void requestSubjectAndConsumer() {
        assertEquals("kubemoot.request.team-a.homelab-pilot", A.requestSubject());
        assertEquals("request-team-a-homelab-pilot", A.requestConsumer());
    }

    @Test
    void lifecycleSubjects() {
        assertEquals("kubemoot.lifecycle.team-a.homelab-pilot.waking.k8s-config",
                A.lifecycleSubject("waking", "k8s-config"));
        assertEquals("kubemoot.lifecycle.team-a.homelab-pilot.>", A.lifecycleWildcard());
    }

    @Test
    void artifactSubjectAndKeyPrefix() {
        assertEquals("kubemoot.artifacts.team-a.homelab-pilot.t1", A.artifactSubject("t1"));
        assertEquals("team-a/homelab-pilot/", A.artifactKeyPrefix());
    }

    @Test
    void chatSubject_underscoresAgentHyphens() {
        assertEquals("kubemoot.chat.team-a.k8s_config", A.chatSubject("k8s-config"));
    }

    @Test
    void kvKeys() {
        assertEquals("team-a.homelab-pilot", A.resumesKey());
        assertEquals("team-a.homelab-pilot.", A.memoryPrefix());
        assertEquals("team-a.homelab-pilot.gpu-topology.rig0", A.memoryKey("gpu-topology", "rig0"));
        assertEquals("team-a.homelab-pilot.gpu_topology.a_b", A.memoryKey("gpu topology", "a.b"));
        assertEquals("team-a.k8s-config", A.agentStateKey("k8s-config"));
        assertEquals("latency.team-a.k8s-config.RTX_5090", A.latencyKey("k8s-config", "RTX 5090"));
    }

    @Test
    void sameCrewInTwoNamespaces_producesDistinctSubjectsAndKeys() {
        assertNotEquals(A.discussWildcard(), B.discussWildcard());
        assertNotEquals(A.broadcastSubject("t1"), B.broadcastSubject("t1"));
        assertNotEquals(A.requestSubject(), B.requestSubject());
        assertNotEquals(A.requestConsumer(), B.requestConsumer());
        assertNotEquals(A.lifecycleWildcard(), B.lifecycleWildcard());
        assertNotEquals(A.artifactSubject("t1"), B.artifactSubject("t1"));
        assertNotEquals(A.artifactKeyPrefix(), B.artifactKeyPrefix());
        assertNotEquals(A.chatSubject("x"), B.chatSubject("x"));
        assertNotEquals(A.resumesKey(), B.resumesKey());
        assertNotEquals(A.memoryPrefix(), B.memoryPrefix());
        assertNotEquals(A.agentStateKey("x"), B.agentStateKey("x"));
        assertNotEquals(A.latencyKey("x", "p"), B.latencyKey("x", "p"));
        // Neither namespace's wildcard matches the other's subjects.
        assertFalse(B.broadcastSubject("t1").startsWith(A.discussPrefix()));
    }

    @Test
    void crewless_scopesByNamespaceAlone() {
        assertFalse(CREWLESS.hasCrew());
        assertEquals("kubemoot.discuss.team-a.>", CREWLESS.discussWildcard());
        assertEquals("kubemoot.discuss.team-a.broadcast.t1", CREWLESS.broadcastSubject("t1"));
        assertEquals("kubemoot.lifecycle.team-a.ready.solo", CREWLESS.lifecycleSubject("ready", "solo"));
        assertEquals("kubemoot.artifacts.team-a.nocrew.t1", CREWLESS.artifactSubject("t1"));
        assertEquals("team-a.default.", CREWLESS.memoryPrefix());
    }

    @Test
    void crewless_hasNoRequestQueueOrResumes() {
        assertThrows(IllegalStateException.class, CREWLESS::requestSubject);
        assertThrows(IllegalStateException.class, CREWLESS::requestConsumer);
        assertThrows(IllegalStateException.class, CREWLESS::resumesKey);
    }

    @Test
    void forCrew_keepsNamespace() {
        assertEquals(A, CREWLESS.forCrew("homelab-pilot"));
        assertEquals(CREWLESS, A.forCrew(""));
        assertEquals(CREWLESS, A.forCrew(null));
    }

    // --- validation ---

    @Test
    void blankNamespace_isRejected() {
        assertThrows(IllegalArgumentException.class, () -> CrewScope.of(null, "c"));
        assertThrows(IllegalArgumentException.class, () -> CrewScope.of("  ", "c"));
    }

    @Test
    void multiTokenNamesAreRejected() {
        assertThrows(IllegalArgumentException.class, () -> CrewScope.of("a.b", "c"));
        assertThrows(IllegalArgumentException.class, () -> CrewScope.of("a", "c.d"));
        assertThrows(IllegalArgumentException.class, () -> CrewScope.of("a", "c>"));
        assertThrows(IllegalArgumentException.class, () -> CrewScope.of("a*", "c"));
    }

    // --- parse ---

    @Test
    void channelOf_readsTheTokenAfterNamespaceAndCrew() {
        // parts[2] of this subject is the namespace; the channel is further in.
        assertEquals("kubernetes", A.channelOf("kubemoot.discuss.team-a.homelab-pilot.kubernetes.t1", "general"));
        assertEquals("broadcast", A.channelOf(A.broadcastSubject("t1"), "general"));
        assertEquals("broadcast", CREWLESS.channelOf(CREWLESS.broadcastSubject("t1"), "general"));
    }

    @Test
    void channelOf_fallsBackOutsideScopeOrWhenMalformed() {
        assertEquals("general", A.channelOf(B.broadcastSubject("t1"), "general"));
        assertEquals("general", A.channelOf("kubemoot.discuss.team-a.homelab-pilot.onlychannel", "general"));
        assertEquals("general", A.channelOf(null, "general"));
        assertEquals("general", A.channelOf("kubemoot.request.team-a.homelab-pilot", "general"));
    }

    @Test
    void parseDiscuss_roundTrips() {
        var parsed = CrewScope.parseDiscuss(B.discussSubject("kubernetes", "t-1")).orElseThrow();
        assertEquals(new CrewScope.DiscussSubject("team-b", "homelab-pilot", "kubernetes", "t-1"), parsed);
    }

    @Test
    void parseDiscuss_rejectsForeignOrShortSubjects() {
        assertEquals(Optional.empty(), CrewScope.parseDiscuss("kubemoot.discuss.homelab-pilot.broadcast"));
        assertEquals(Optional.empty(), CrewScope.parseDiscuss("kubemoot.chat.team-a.x"));
        assertEquals(Optional.empty(), CrewScope.parseDiscuss("kubemoot.discuss.a..b.c"));
        assertEquals(Optional.empty(), CrewScope.parseDiscuss(null));
    }

    @Test
    void parseLifecycle_roundTrips() {
        var parsed = CrewScope.parseLifecycle(A.lifecycleSubject("ready", "k8s-config")).orElseThrow();
        assertEquals(new CrewScope.LifecycleSubject("team-a", "homelab-pilot", "ready", "k8s-config"), parsed);
        assertEquals(Optional.empty(), CrewScope.parseLifecycle("kubemoot.lifecycle.homelab-pilot.ready"));
    }

    // --- namespace resolution ---

    @Test
    void resolveNamespace_prefersConfiguredValue(@TempDir Path dir) throws Exception {
        Path file = Files.writeString(dir.resolve("namespace"), "from-file");
        assertEquals("from-env", CrewScope.resolveNamespace(" from-env ", file));
    }

    @Test
    void resolveNamespace_fallsBackToServiceAccountFile(@TempDir Path dir) throws Exception {
        Path file = Files.writeString(dir.resolve("namespace"), "from-file\n");
        assertEquals("from-file", CrewScope.resolveNamespace(null, file));
        assertEquals("from-file", CrewScope.resolveNamespace("  ", file));
    }

    @Test
    void resolveNamespace_failsWhenBothAbsent(@TempDir Path dir) throws Exception {
        Path missing = dir.resolve("missing");
        var e = assertThrows(IllegalStateException.class, () -> CrewScope.resolveNamespace(null, missing));
        assertTrue(e.getMessage().contains("KUBEMOOT_NAMESPACE"));
        Path empty = Files.writeString(dir.resolve("empty"), "  ");
        assertThrows(IllegalStateException.class, () -> CrewScope.resolveNamespace("", empty));
    }

    @Test
    void fromProperties_usesNamespaceAndCrew() {
        var props = mock(AgentProperties.class);
        when(props.namespace()).thenReturn(Optional.of("team-a"));
        when(props.crew()).thenReturn(Optional.of("homelab-pilot"));
        assertEquals(A, CrewScope.fromProperties(props));
    }
}
