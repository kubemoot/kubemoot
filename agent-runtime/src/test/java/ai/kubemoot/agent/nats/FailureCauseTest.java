package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FailureCauseTest {

    private static final String NOT_LISTENING =
            "Mulling failed: server vllm-rig0 could not make qwen3:14b ready: no operator is listening for model lifecycle requests";
    private static final String NOT_READY =
            "Mulling failed: server vllm-rig0 could not make qwen3:14b ready: the engine refused";
    private static final String REFUSED = "Mulling failed: java.net.ConnectException: Connection refused";

    @Test
    void anOperatorThatDoesNotRespondIsNamedAsSuch() {
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE), FailureCause.classify(NOT_LISTENING));
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE),
                FailureCause.classify("could not make m ready: the operator did not answer before the safety limit"));
    }

    @Test
    void aServerThatCouldNotMakeTheModelReadyIsNotTheOperatorsFault() {
        assertEquals(Optional.of(FailureCause.MODEL_NOT_READY), FailureCause.classify(NOT_READY));
    }

    @Test
    void aRefusedConnectionIsAnUnreachableServer() {
        assertEquals(Optional.of(FailureCause.SERVER_UNREACHABLE), FailureCause.classify(REFUSED));
    }

    @Test
    void otherFailuresAndNullNameNoCause() {
        assertTrue(FailureCause.classify("Mulling failed: the tool returned an error").isEmpty());
        assertTrue(FailureCause.classify("").isEmpty());
        assertTrue(FailureCause.classify(null).isEmpty());
        assertTrue(FailureCause.classify("Evaluation did not complete within estimated window").isEmpty());
    }

    @Test
    void aTieGoesToTheFirstDeclaredCause() {
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE), FailureCause.dominant(List.of(REFUSED, NOT_LISTENING)));
    }

    @Test
    void theTextsOfTheRealProducersAreRecognised() {
        // The messages the placement-core lifecycle client and driver build.
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE), FailureCause.classify(
                "server s could not make m ready: the operator could not be asked to ready m: boom"));
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE), FailureCause.classify(
                "server s could not make m ready: the operator's reply could not be read: bad json"));
    }

    @Test
    void theAnswerNamesTheCauseInWordsAPersonUnderstands() {
        assertEquals("No agent could answer: the crew's models could not be made ready (the operator did not respond).",
                FailureCause.OPERATOR_UNREACHABLE.answer());
    }

    @Test
    void unanimousNeedsEveryFailureToNameTheSameCause() {
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE),
                FailureCause.unanimous(List.of(NOT_LISTENING, NOT_LISTENING)));
        assertTrue(FailureCause.unanimous(List.of(NOT_LISTENING, NOT_READY)).isEmpty(), "two causes");
        assertTrue(FailureCause.unanimous(List.of(NOT_LISTENING, "the tool returned an error")).isEmpty(),
                "one failure names no cause");
        assertTrue(FailureCause.unanimous(List.of()).isEmpty(), "no failures");
    }

    @Test
    void dominantIsTheMostCommonCauseAndIgnoresFailuresThatNameNone() {
        assertEquals(Optional.of(FailureCause.MODEL_NOT_READY),
                FailureCause.dominant(List.of(NOT_READY, NOT_READY, NOT_LISTENING, "a tool error")));
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE),
                FailureCause.dominant(List.of(NOT_LISTENING, NOT_READY)), "a tie goes to the first declared");
        assertTrue(FailureCause.dominant(List.of("a tool error")).isEmpty());
    }
}
