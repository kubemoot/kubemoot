package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.function.ToLongFunction;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Ranking the candidates of one call, with and without preferFaster. */
class ModelCandidatesRankTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static ModelCandidates candidates(String json) {
        return ModelCandidates.parse(Optional.of(json), MAPPER);
    }

    private static ToLongFunction<String> speeds(Map<String, Long> bySpeed) {
        return m -> bySpeed.getOrDefault(m, 0L);
    }

    private static final String BIG_AND_SMALL = "[{\"model\":\"big\",\"score\":70},{\"model\":\"small\",\"score\":60}]";

    @Test
    void preferFasterZeroKeepsTheBoundModelAndTheStaticAlternatives() {
        var c = candidates(BIG_AND_SMALL);
        var r = c.rank("big", 10, 0, speeds(Map.of("big", 10L, "small", 100L)));
        assertEquals("big", r.preferred());
        assertEquals(List.of("small"), r.alternatives());
        assertEquals(List.of("big", "small"), r.acceptable());
    }

    @Test
    void aFasterModelBecomesPreferredWhenItsRaisedScoreIsHigher() {
        var c = candidates(BIG_AND_SMALL);
        // big: 70 + round(50 * 20/60) = 87; small: 60 + 50 = 110.
        var r = c.rank("big", 10, 50, speeds(Map.of("big", 20L, "small", 60L)));
        assertEquals("small", r.preferred());
        assertTrue(r.alternatives().isEmpty(), "big is 23 below small: out of tolerance");
    }

    @Test
    void theToleranceAppliesToTheRaisedScores() {
        var c = candidates(BIG_AND_SMALL);
        var r = c.rank("big", 30, 50, speeds(Map.of("big", 20L, "small", 60L)));
        assertEquals("small", r.preferred());
        assertEquals(List.of("big"), r.alternatives(), "big is 23 below small: within 30");
    }

    @Test
    void aTieKeepsTheBoundModel() {
        var c = candidates("[{\"model\":\"a\",\"score\":50},{\"model\":\"b\",\"score\":50}]");
        var r = c.rank("b", 10, 40, speeds(Map.of("a", 50L, "b", 50L)));
        assertEquals("b", r.preferred(), "equal speeds and scores: the bound model stays");
        assertEquals(List.of("a"), r.alternatives());
    }

    @Test
    void anUnmeasuredCandidateEarnsNothingAndIsNotGuessedSlow() {
        var c = candidates("[{\"model\":\"bound\",\"score\":50},{\"model\":\"mystery\",\"score\":60},{\"model\":\"quick\",\"score\":20}]");
        // bound: 50 + 0 (unmeasured); mystery: 60 + 0; quick: 20 + 30. mystery is best on score alone.
        var r = c.rank("bound", 10, 30, speeds(Map.of("quick", 80L)));
        assertEquals("mystery", r.preferred());
        assertEquals(List.of("bound", "quick"), r.alternatives());
    }

    @Test
    void nothingMeasuredMeansTheStaticRanking() {
        var c = candidates(BIG_AND_SMALL);
        var r = c.rank("big", 10, 80, speeds(Map.of()));
        assertEquals("big", r.preferred());
        assertEquals(List.of("small"), r.alternatives());
    }

    @Test
    void aBoundModelThatIsNotListedKeepsTheOldBehavior() {
        var c = candidates(BIG_AND_SMALL);
        var r = c.rank("elsewhere", 10, 50, speeds(Map.of("big", 10L, "small", 100L)));
        assertEquals("elsewhere", r.preferred());
        assertTrue(r.alternatives().isEmpty());
    }

    @Test
    void anEmptyListLeavesOnlyTheBoundModel() {
        var r = ModelCandidates.none().rank("bound", 10, 50, speeds(Map.of("bound", 10L)));
        assertEquals(List.of("bound"), r.acceptable());
    }

    @Test
    void aSingleCandidateIsItsOwnRanking() {
        var r = candidates("[{\"model\":\"only\",\"score\":40}]").rank("only", 10, 100, speeds(Map.of("only", 5L)));
        assertEquals("only", r.preferred());
        assertTrue(r.alternatives().isEmpty());
    }

    @Test
    void speedPointsAreProportionalToTheFastest() {
        assertEquals(100, ModelCandidates.speedPoints(60, 60, 100));
        assertEquals(50, ModelCandidates.speedPoints(30, 60, 100));
        assertEquals(0, ModelCandidates.speedPoints(0, 60, 100));
        assertEquals(0, ModelCandidates.speedPoints(30, 0, 100));
        assertEquals(17, ModelCandidates.speedPoints(20, 60, 50));
    }

    @Test
    void aNegativeToleranceActsAsZero() {
        var c = candidates("[{\"model\":\"a\",\"score\":50},{\"model\":\"b\",\"score\":50}]");
        var r = c.rank("a", -20, 10, speeds(Map.of("a", 10L, "b", 10L)));
        assertEquals(List.of("b"), r.alternatives());
    }

    @Test
    void thePolicyHasAListAndAPreferFasterPerPhase() {
        var policy = new CandidatePolicy(MAPPER, Optional.of(BIG_AND_SMALL),
                Optional.of("[{\"model\":\"t-big\",\"score\":70},{\"model\":\"t-small\",\"score\":60}]"), 10, 0, 50);
        var speed = speeds(Map.of("big", 20L, "small", 60L, "t-big", 20L, "t-small", 60L));
        assertEquals("big", policy.mullingRanking("big", speed).preferred(), "mulling asks for no preferFaster");
        assertEquals("t-small", policy.triageRanking("t-big", speed).preferred(), "triage favors the faster model");
        assertEquals(List.of("t-small"), new CandidatePolicy(MAPPER, Optional.empty(), Optional.empty(), 10, 0, 0).triageRanking("t-small", speed).acceptable().subList(0, 1),
                "a policy built without a triage list ranks only the bound model");
    }

    /**
     * Triage with every model scored 0 (a rule with preferFaster alone) and the bound
     * model the slower of two measured models: the call prefers the measured-faster
     * model, and the bound model stays an alternative only within the tolerance (here
     * the default of 10 points: the slower model earns 39 of 50, 11 below).
     */
    @Test
    void thePilotTriageStatePrefersTheMeasuredFasterModelOverTheBoundOne() {
        var c = candidates("[{\"model\":\"qwen3:32b\",\"score\":0},{\"model\":\"qwen3:14b\",\"score\":0},"
                + "{\"model\":\"qwen3:8b\",\"score\":0}]");
        var r = c.rank("qwen3:32b", 10, 50, speeds(Map.of("qwen3:14b", 100L, "qwen3:32b", 78L, "qwen3:8b", 7L)));
        assertEquals("qwen3:14b", r.preferred());
        assertTrue(r.alternatives().isEmpty(), "14B earns 50, 32B 39, 8B 4: the 32B is 11 below, outside a tolerance of 10");
    }
}
