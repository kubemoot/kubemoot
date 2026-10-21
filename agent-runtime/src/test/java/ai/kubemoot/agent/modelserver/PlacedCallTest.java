package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/** A placed call is scored against its placement's prediction on its first request, and reported in the contract's shape. */
class PlacedCallTest {

    private static final String SERVER = "ollama-gpu";
    private static final String MODEL = "qwen3:14b";
    /** Predicts 0.5 s wait, 2 s load, 1 s prompt, 3 s generation: 6.5 s in all, 3.5 s to the first token. */
    private static final Prediction PREDICTION = new Prediction(0.5, 2.0, 1.0, 3.0);

    private static PlacementDecision decision(Prediction predicted) {
        return new PlacementDecision(PlacementDecision.LEARNED, "predicted 6.5s",
                List.of(new PlacementDecision.Alternative("ollama-cpu", MODEL, true, true,
                        new Prediction(0, 0, 4, 60)),
                        new PlacementDecision.Alternative("ollama-rig", MODEL, false, false, null)),
                predicted);
    }

    private static CallObservation request(boolean ok, long durationMs, long firstTokenMs) {
        return new CallObservation(SERVER, MODEL, CallObservation.KIND_SIMPLE, ok, true, 800, 120, durationMs,
                -1, -1, -1, 1, 1_000L, firstTokenMs);
    }

    @Test
    void theFirstCompletedRequestIsScoredAgainstThePrediction() {
        var call = new PlacedCall(SERVER, MODEL, decision(PREDICTION));

        var score = call.observe(request(true, 8_000, 4_000)).orElseThrow();

        assertEquals(PlacementDecision.LEARNED, score.policy());
        assertEquals(6_500, score.predictedTotalMs());
        assertEquals(8_000, score.actualTotalMs());
        assertEquals(1_500, score.totalAbsMs());
        assertEquals(1_500, score.totalSignedMs(), "positive: the call took longer than predicted");
        assertEquals(1_500 / 8_000.0, score.totalRelative(), 1e-9);
        assertTrue(score.hasFirstToken());
        assertEquals(500, score.firstTokenAbsMs(), "3.5 s predicted, 4 s measured");
        assertEquals(500 / 4_000.0, score.firstTokenRelative(), 1e-9);
    }

    @Test
    void aFasterThanPredictedCallHasANegativeSignedErrorAndAPositiveAbsoluteOne() {
        var score = new PlacedCall(SERVER, MODEL, decision(PREDICTION)).observe(request(true, 5_000, -1)).orElseThrow();

        assertEquals(-1_500, score.totalSignedMs());
        assertEquals(1_500, score.totalAbsMs());
        assertFalse(score.hasFirstToken(), "a request that was not streamed has no first token to score");
    }

    @Test
    void laterRequestsOfTheSamePlacementAreCountedButNotScored() {
        var call = new PlacedCall(SERVER, MODEL, decision(PREDICTION));

        assertTrue(call.observe(request(true, 8_000, -1)).isPresent());
        assertTrue(call.observe(request(true, 2_000, -1)).isEmpty());
        assertTrue(call.observe(request(true, 2_000, -1)).isEmpty());

        @SuppressWarnings("unchecked")
        var actual = (Map<String, Object>) call.snapshot(false).get("actual");
        assertEquals(3, actual.get("requests"));
        assertEquals(8_000L, actual.get("totalMs"), "the figures are the first request's");
    }

    @Test
    void aFailedRequestIsNotScored() {
        var call = new PlacedCall(SERVER, MODEL, decision(PREDICTION));

        assertTrue(call.observe(request(false, 300_000, -1)).isEmpty());

        var snapshot = call.snapshot(false);
        assertFalse(snapshot.containsKey("error"));
        @SuppressWarnings("unchecked")
        var actual = (Map<String, Object>) snapshot.get("actual");
        assertEquals(false, actual.get("ok"));
    }

    @Test
    void aPlacementWithoutAPredictionIsReportedButNeverScored() {
        var call = new PlacedCall(SERVER, MODEL, decision(null));

        assertTrue(call.observe(request(true, 8_000, 1_000)).isEmpty());

        var snapshot = call.snapshot(true);
        assertFalse(snapshot.containsKey("predicted"));
        assertFalse(snapshot.containsKey("error"));
        assertTrue(snapshot.containsKey("actual"));
        assertEquals("learned", snapshot.get("policy"));
    }

    @Test
    void aZeroLengthActualDoesNotDivideByZero() {
        var score = new PlacedCall(SERVER, MODEL, decision(PREDICTION)).observe(request(true, 0, 0)).orElseThrow();

        assertEquals(6_500, score.totalAbsMs());
        assertEquals(6_500.0, score.totalRelative(), 1e-9, "an actual under a millisecond counts as one");
        assertTrue(Double.isFinite(score.firstTokenRelative()));
    }

    @Test
    void theDetailedSnapshotCarriesTheReasonTheAlternativesAndEveryPart() {
        var call = new PlacedCall(SERVER, MODEL, decision(PREDICTION));
        call.waited(4_200);
        call.observe(request(true, 8_000, 4_000));

        var s = call.snapshot(true);

        assertEquals("evaluation", s.get("phase"));
        assertEquals("simple", s.get("kind"));
        assertEquals(MODEL, s.get("model"));
        assertEquals(SERVER, s.get("server"));
        assertEquals(true, s.get("coldStart"));
        assertEquals("predicted 6.5s", s.get("reason"));
        @SuppressWarnings("unchecked")
        var predicted = (Map<String, Object>) s.get("predicted");
        assertEquals(500L, predicted.get("waitMs"));
        assertEquals(2_000L, predicted.get("loadMs"));
        assertEquals(1_000L, predicted.get("promptMs"));
        assertEquals(3_000L, predicted.get("generateMs"));
        assertEquals(6_500L, predicted.get("totalMs"));
        @SuppressWarnings("unchecked")
        var actual = (Map<String, Object>) s.get("actual");
        assertEquals(4_200L, actual.get("waitMs"));
        assertEquals(4_000L, actual.get("firstTokenMs"));
        assertEquals(800L, actual.get("inputTokens"));
        @SuppressWarnings("unchecked")
        var alternatives = (List<Map<String, Object>>) s.get("alternatives");
        assertEquals(2, alternatives.size());
        assertEquals("ollama-cpu", alternatives.get(0).get("server"));
        assertEquals(64_000L, ((Map<?, ?>) alternatives.get(0).get("predicted")).get("totalMs"));
        assertFalse(alternatives.get(1).containsKey("predicted"), "an unmeasured server has no prediction");
        assertFalse(alternatives.get(0).containsKey("model"), "the model is named only when it differs from the call's");
    }

    @Test
    void theCompactSnapshotLeavesOutTheReasonAndTheAlternatives() {
        var call = new PlacedCall(SERVER, MODEL, decision(PREDICTION));
        call.observe(request(true, 8_000, -1));

        var s = call.snapshot(false);

        assertFalse(s.containsKey("reason"));
        assertFalse(s.containsKey("alternatives"));
        assertTrue(s.containsKey("predicted"));
        assertTrue(s.containsKey("error"));
    }

    @Test
    void aTriageCallIsInTheTriagePhase() {
        var call = new PlacedCall(SERVER, MODEL, decision(PREDICTION));
        call.observe(new CallObservation(SERVER, MODEL, CallObservation.KIND_TRIAGE, true, false, 10, 5, 100,
                -1, -1, -1, 1, 5L));

        assertEquals("triage", call.snapshot(false).get("phase"));
    }

    @Test
    void aReasonLongerThanTheCapIsCut() {
        var call = new PlacedCall(SERVER, MODEL, new PlacementDecision("algorithm", "x".repeat(2_000), null, null));

        assertEquals(PlacedCall.REASON_MAX_CHARS, ((String) call.snapshot(true).get("reason")).length());
    }

    @Test
    void aCallThatNeverSentARequestStillReportsItsPlacement() {
        var s = new PlacedCall(SERVER, MODEL, decision(PREDICTION)).snapshot(true);

        assertEquals("learned", s.get("policy"));
        assertFalse(s.containsKey("actual"));
        assertFalse(s.containsKey("kind"));
    }

    @Test
    void aPlacementWithoutADecisionHasNoCall() {
        assertNull(PlacedCall.of(null));
        assertNull(PlacedCall.of(new CallPlanner.Placement(null, MODEL, 1, List.of())));
    }

    @Test
    void aCallPlannedAtSelectionIsPredictedWithoutTheWaitAndLoadThatOverlappedTriage() {
        var planned = new PlacedCall(SERVER, MODEL, decision(PREDICTION)).plannedAtSelection();

        var score = planned.observe(request(true, 4_500, -1)).orElseThrow();

        assertEquals(4_000, score.predictedTotalMs(), "1 s prompt and 3 s generation only");
        assertEquals(500, score.totalAbsMs());
        assertTrue(((String) planned.snapshot(true).get("reason")).contains("planned at selection"));
        assertNull(new PlacedCall(SERVER, MODEL, decision(null)).plannedAtSelection().snapshot(false).get("predicted"));
    }

    @Test
    void theSnapshotCarriesThePredictedTimeToTheFirstTokenOnce() {
        @SuppressWarnings("unchecked")
        var predicted = (Map<String, Object>) new PlacedCall(SERVER, MODEL, decision(PREDICTION)).snapshot(false)
                .get("predicted");

        assertEquals(3_500L, predicted.get("firstTokenMs"));
    }

    @Test
    void explainingAPlacementNeverFailsIt() {
        var broken = new CallPlanner.Placement(null, MODEL, 1, List.of());

        var explained = PlacementDecisions.explained(broken, java.util.Optional.empty(), PlacementMode.ALGORITHM);

        assertEquals(PlacementDecision.ALGORITHM, explained.decision().policy());
        assertNull(explained.decision().predicted());
        assertSame(explained, PlacementDecisions.explained(explained, java.util.Optional.empty(), PlacementMode.SHADOW),
                "a placement a policy already explained is left as it is");
    }
}
