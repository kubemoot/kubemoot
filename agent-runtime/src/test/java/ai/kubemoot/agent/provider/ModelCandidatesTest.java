package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/** Parsing the operator's candidate list and choosing in-tolerance alternatives. */
class ModelCandidatesTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static ModelCandidates parse(String json) {
        return ModelCandidates.parse(Optional.ofNullable(json), MAPPER);
    }

    @Test
    void parse_readsRankedList() {
        var c = parse("[{\"model\":\"qwen3:32b\",\"score\":70},{\"model\":\"qwen3:14b\",\"score\":60}]");
        assertEquals(List.of(new ModelCandidates.Candidate("qwen3:32b", 70),
                new ModelCandidates.Candidate("qwen3:14b", 60)), c.ranked());
    }

    @Test
    void parse_toleratesAbsentBlankMalformedAndPartialInput() {
        assertTrue(parse(null).ranked().isEmpty());
        assertTrue(parse("  ").ranked().isEmpty());
        assertTrue(parse("not json").ranked().isEmpty());
        assertTrue(parse("{\"model\":\"x\"}").ranked().isEmpty(), "an object is not a list");
        var c = parse("[{\"score\":5},{\"model\":\"\"},{\"model\":\"qwen3:8b\"}]");
        assertEquals(List.of(new ModelCandidates.Candidate("qwen3:8b", 0)), c.ranked());
    }

    @Test
    void warmAlternatives_keepsOnlyCandidatesWithinTolerance_bestFirst() {
        var c = parse("[{\"model\":\"big\",\"score\":70},{\"model\":\"tiny\",\"score\":30},"
                + "{\"model\":\"mid\",\"score\":60},{\"model\":\"peer\",\"score\":75}]");
        assertEquals(List.of("peer", "mid"), c.warmAlternatives("big", 10));
        assertEquals(List.of("peer"), c.warmAlternatives("big", 0));
        assertEquals(List.of("peer", "mid", "tiny"), c.warmAlternatives("big", 40));
    }

    @Test
    void warmAlternatives_emptyWhenPreferredUnknownOrListEmpty() {
        var c = parse("[{\"model\":\"a\",\"score\":70},{\"model\":\"b\",\"score\":70}]");
        assertTrue(c.warmAlternatives("not-listed", 100).isEmpty());
        assertTrue(ModelCandidates.none().warmAlternatives("a", 100).isEmpty());
    }

    @Test
    void warmAlternatives_negativeToleranceActsAsZero() {
        var c = parse("[{\"model\":\"a\",\"score\":70},{\"model\":\"b\",\"score\":70},{\"model\":\"c\",\"score\":69}]");
        assertEquals(List.of("b"), c.warmAlternatives("a", -20));
    }

    @Test
    void candidatePolicy_readsJsonAndTolerance() {
        var policy = new CandidatePolicy(MAPPER,
                Optional.of("[{\"model\":\"a\",\"score\":70},{\"model\":\"b\",\"score\":55}]"), 15);
        assertEquals(List.of("b"), policy.mullingAlternatives("a"));
        assertTrue(new CandidatePolicy(MAPPER, Optional.empty(), 15).mullingAlternatives("a").isEmpty());
    }
}
