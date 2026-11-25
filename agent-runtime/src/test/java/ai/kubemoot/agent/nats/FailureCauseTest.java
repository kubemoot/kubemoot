package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.modelserver.ModelNotReadyException;
import ai.kubemoot.agent.modelserver.NatsModelLifecycle;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FailureCauseTest {

    private static final String NOT_LISTENING =
            "Mulling failed: server server-a could not make model-a ready: no operator is listening for model lifecycle requests";
    private static final String NOT_READY =
            "Mulling failed: server server-a could not make model-a ready: the engine refused";
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
    void theTextsOfTheRealProducersAreRecognised() {
        // Built by the placement-core lifecycle client and driver, not typed here.
        String notListening = ModelNotReadyException.of("s", "m", NatsModelLifecycle.NOT_LISTENING).getMessage();
        String noAnswer = ModelNotReadyException.of("s", "m", NatsModelLifecycle.NO_ANSWER).getMessage();
        String couldNotAsk = ModelNotReadyException.of("s", "m", NatsModelLifecycle.COULD_NOT_ASK + "ready m: boom").getMessage();
        String unreadable = ModelNotReadyException.of("s", "m", NatsModelLifecycle.UNREADABLE_REPLY + ": bad json").getMessage();
        for (String text : List.of(notListening, noAnswer, couldNotAsk, unreadable)) {
            assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE), FailureCause.classify(text), text);
        }
        assertEquals(Optional.of(FailureCause.MODEL_NOT_READY),
                FailureCause.classify(ModelNotReadyException.of("s", "m", "the engine refused").getMessage()));
    }

    @Test
    void theAnswerNamesTheCauseInWordsAPersonUnderstands() {
        assertEquals("No agent could answer: the crew's models could not be made ready (the operator did not respond).",
                FailureCause.OPERATOR_UNREACHABLE.answer());
    }

    @Test
    void theCauseIsReadBackFromItsPublishedId() {
        for (FailureCause cause : FailureCause.values()) {
            assertEquals(Optional.of(cause), FailureCause.fromId(cause.id()));
        }
        assertTrue(FailureCause.fromId("model_timeout").isEmpty());
        assertTrue(FailureCause.fromId("").isEmpty());
        assertTrue(FailureCause.fromId(null).isEmpty());
    }

    @Test
    void unanimousNeedsEveryFailureTypeToNameTheSameCause() {
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE),
                FailureCause.unanimous(List.of("operator_unreachable", "operator_unreachable")));
        assertTrue(FailureCause.unanimous(List.of("operator_unreachable", "model_not_ready")).isEmpty(), "two causes");
        assertTrue(FailureCause.unanimous(List.of("operator_unreachable", "model_timeout")).isEmpty(),
                "one failure names no cause");
        assertTrue(FailureCause.unanimous(List.of("operator_unreachable", "")).isEmpty(), "one failure has no type");
        assertTrue(FailureCause.unanimous(List.of()).isEmpty(), "no failures");
    }

    @Test
    void dominantIsTheMostCommonCauseAndIgnoresFailuresThatNameNone() {
        assertEquals(Optional.of(FailureCause.MODEL_NOT_READY), FailureCause.dominant(
                List.of("model_not_ready", "model_not_ready", "operator_unreachable", "model_timeout")));
        assertEquals(Optional.of(FailureCause.OPERATOR_UNREACHABLE),
                FailureCause.dominant(List.of("server_unreachable", "operator_unreachable")), "a tie goes to the first declared");
        assertTrue(FailureCause.dominant(List.of("model_timeout")).isEmpty());
    }
}
