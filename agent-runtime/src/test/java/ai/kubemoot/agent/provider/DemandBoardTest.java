package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/** The shared demand view: intents, waits, recent use, selection frequency, and expiry. */
class DemandBoardTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static DemandBoard board(InMemoryKv bucket, String agent) {
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn(agent);
        when(props.namespace()).thenReturn(Optional.of("crew-ns"));
        when(props.crew()).thenReturn(Optional.of("pilot"));
        return new DemandBoard(bucket.provider(DemandBoard.BUCKET), MAPPER, props);
    }

    @Test
    void intentIsSetOnSelection_andClearedOnFirstCall() {
        var bucket = new InMemoryKv();
        var rulesKeeper = board(bucket, "rules-keeper");

        rulesKeeper.intend("t1", List.of("qwen3:14b", "qwen3:32b"));
        assertEquals(1, rulesKeeper.snapshot().get("qwen3:14b").intents());
        assertEquals(1, rulesKeeper.snapshot().get("qwen3:32b").intents());
        assertTrue(bucket.data.keySet().stream().allMatch(k -> k.matches("[A-Za-z0-9_.=-]+")), "keys are KV-safe");

        rulesKeeper.clearIntents("t1");
        rulesKeeper.recordUse("qwen3:14b");
        var after = rulesKeeper.snapshot();
        assertNull(after.get("qwen3:32b"));
        assertEquals(0, after.get("qwen3:14b").intents());
        assertTrue(after.get("qwen3:14b").useRate() > 0.99, "the call start counts as recent use");
    }

    @Test
    void intentIsClearedWhenTheThreadEnds_andOtherAgentsKeepTheirs() {
        var bucket = new InMemoryKv();
        var a = board(bucket, "a");
        var b = board(bucket, "b");
        a.intend("t1", List.of("qwen3:14b"));
        b.intend("t1", List.of("qwen3:14b"));

        a.clearIntents("t1");

        assertEquals(1, a.snapshot().get("qwen3:14b").intents());
    }

    @Test
    void expiredIntentIsIgnored_theTtlIsTheSafetyNet() {
        long now = System.currentTimeMillis();
        JsonNode stale = MAPPER.createObjectNode().put("kind", "intent").put("model", "m").put("expiresAt", now - 1);
        JsonNode live = MAPPER.createObjectNode().put("kind", "intent").put("model", "m").put("expiresAt", now + 60_000);
        assertEquals(1, DemandBoard.aggregate(List.of(stale, live), now).get("m").intents());
    }

    @Test
    void waitIsSetAndCleared() {
        var bucket = new InMemoryKv();
        var agent = board(bucket, "researcher");
        agent.waitStarted("qwen3:32b");
        assertEquals(1, agent.snapshot().get("qwen3:32b").waiters());
        agent.waitEnded("qwen3:32b");
        assertNull(agent.snapshot().get("qwen3:32b"));
    }

    @Test
    void recentUseDecaysWithTheHalfLife() {
        long now = System.currentTimeMillis();
        long halfLife = DemandBoard.USE_HALF_LIFE.toMillis();
        assertEquals(2.0, DemandBoard.decayed(4.0, now - halfLife, now), 0.001);
        assertEquals(4.0, DemandBoard.decayed(4.0, now + 5_000, now), 0.001, "clock skew never inflates");
    }

    @Test
    void selectionFrequencyPredictsDemandForTheAgentsCandidates() {
        var bucket = new InMemoryKv();
        var agent = board(bucket, "analyst");
        agent.recordSelection(List.of("qwen3:32b", "qwen3:14b"));
        agent.recordSelection(List.of("qwen3:32b", "qwen3:14b"));
        var demand = agent.snapshot();
        assertEquals(2.0, demand.get("qwen3:32b").predicted(), 0.01);
        assertEquals(0, demand.get("qwen3:32b").intents(), "a forecast is not an intent");
    }

    @Test
    void unavailableBucket_isEmptyAndSilent() {
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn("a");
        when(props.namespace()).thenReturn(Optional.empty());
        when(props.crew()).thenReturn(Optional.empty());
        var nats = mock(ai.kubemoot.agent.nats.NatsConnectionProvider.class);
        var agent = new DemandBoard(nats, MAPPER, props);
        agent.intend("t", List.of("m"));
        agent.recordUse("m");
        assertTrue(agent.snapshot().isEmpty());
    }

    @Test
    void modelKeys_areReversibleTokens() {
        assertEquals("qwen2_2e5_3a14b", ModelKeys.token("qwen2.5:14b"));
        assertNotEquals(ModelKeys.token("a_b"), ModelKeys.token("a.b"));
        assertEquals("_", ModelKeys.token(""));
    }
}
