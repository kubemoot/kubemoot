package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.function.Consumer;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** What the reducer keeps so that a restarted coordinator can carry on: the request, the technologies, the bus messages applied, the takeover. */
class CoordinatorStateReducerResumeTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Instant T0 = Instant.parse("2026-10-31T10:00:00Z");

    private static CoordinatorEvent event(String id, String type, Instant at, Consumer<ObjectNode> data) {
        ObjectNode fields = MAPPER.createObjectNode();
        data.accept(fields);
        return new CoordinatorEvent(id, type, "t", 0, at, "c", fields);
    }

    private static CoordinatorEvent opened() {
        return event("t/opened", "thread_opened", T0, d -> {
            d.put("openedAt", T0.toString());
            d.put("phaseStartedAt", T0.toString());
            d.put("requestSeq", 42);
        });
    }

    @Test
    void theRequestThatStartedTheThreadIsKept() {
        assertEquals(42, CoordinatorStateReducer.reduce(List.of(opened())).requestSeq);
    }

    @Test
    void aThreadNoRequestStartedHasRequestSequenceZero() {
        var external = event("t/opened", "thread_opened", T0, d -> d.put("source", "external"));

        assertEquals(0, CoordinatorStateReducer.reduce(List.of(external)).requestSeq);
    }

    @Test
    void theTechnologiesOfTheConvenedSubcommitteeAreKept() {
        var convened = event("t/0/subcommittee/1", "subcommittee_convened", T0, d -> {
            d.put("path", "reasoning");
            d.putArray("technologies").add("kubernetes").add("prometheus");
        });

        var state = CoordinatorStateReducer.reduce(List.of(opened(), convened));

        assertEquals(List.of("kubernetes", "prometheus"), state.technologies);
    }

    @Test
    void theReviewRosterIsWrittenUntilTheThreadEntersAPhase() {
        var review = event("t/0/review/1", "review_started", T0, d -> d.putArray("roster").add("analyst"));
        var entered = event("t/0/phase/REVIEW/1", "phase_entered", T0, d -> d.put("phase", "REVIEW"));

        assertTrue(CoordinatorStateReducer.reduce(List.of(opened(), review)).reviewRosterWritten);
        assertFalse(CoordinatorStateReducer.reduce(List.of(opened(), review, entered)).reviewRosterWritten);
    }

    @Test
    void theMessagesWhoseSignalsWereLoggedAreRemembered() {
        var agree = event("t/0/signal/a/m1", "signal_applied", T0, d -> {
            d.put("agent", "a");
            d.put("signal", "agree");
            d.put("messageId", "m1");
        });
        var failed = event("t/0/failure/b/m2", "failure_recorded", T0, d -> {
            d.put("agent", "b");
            d.put("messageId", "m2");
        });
        var timedOut = event("t/0/failure/c/timeout", "failure_recorded", T0, d -> {
            d.put("agent", "c");
            d.put("synthesized", true);
            d.put("messageId", "");
        });

        var state = CoordinatorStateReducer.reduce(List.of(opened(), agree, failed, timedOut));

        assertEquals(java.util.Set.of("m1", "m2"), state.appliedMessageIds);
    }

    @Test
    void theTimeOfTheLastEventIsKept() {
        var later = event("t/0/phase/ADVISORY/1", "phase_entered", T0.plusSeconds(5), d -> d.put("phase", "ADVISORY"));

        assertEquals(T0.plusSeconds(5), CoordinatorStateReducer.reduce(List.of(opened(), later)).lastEventAt);
    }

    @Test
    void aTakeoverRestartsTheClocksAndIsCounted() {
        var later = event("t/0/phase/EVALUATING/1", "phase_entered", T0.plusSeconds(5), d -> {
            d.put("phase", "EVALUATING");
            d.put("phaseStartedAt", T0.plusSeconds(5).toString());
        });
        Instant resumedAt = T0.plus(java.time.Duration.ofHours(1));
        var resumed = event("t/0/resumed/1", "coordinator_resumed", resumedAt, d -> {
            d.put("fromSeq", 2);
            d.put("resumedAt", resumedAt.toString());
        });

        var state = CoordinatorStateReducer.reduce(List.of(opened(), later, resumed));

        assertEquals(1, state.resumes);
        assertEquals(resumedAt, state.openedAt, "the ceiling counts from the takeover");
        assertEquals(resumedAt, state.phaseStartedAt, "the phase clock restarts, so downtime is not charged to the phase");
        assertEquals("EVALUATING", state.phase, "the takeover does not move the thread");
    }

    @Test
    void aRepeatedTakeoverEventIsIgnored() {
        Instant resumedAt = T0.plusSeconds(60);
        var resumed = event("t/0/resumed/1", "coordinator_resumed", resumedAt, d -> d.put("resumedAt", resumedAt.toString()));

        assertEquals(1, CoordinatorStateReducer.reduce(List.of(opened(), resumed, resumed)).resumes);
    }
}
