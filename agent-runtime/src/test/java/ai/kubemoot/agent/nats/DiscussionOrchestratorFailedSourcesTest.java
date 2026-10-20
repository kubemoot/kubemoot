package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The closing note an answer carries when an agent reported failure. */
class DiscussionOrchestratorFailedSourcesTest {

    @Test
    void noFailure_addsNothing() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("curator", "ok");
        assertEquals("", DiscussionOrchestrator.failedSourcesNote(state));
    }

    @Test
    void aFailedAgent_isNamedWithItsReason_evenWhenAnotherAgentAgreed() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("curator", "ok");
        state.failureSignals.put("library-search",
                "Tools unavailable: agent library-search declares search_oreilly_content but the tool list from"
                        + " the MCP gateway at http://gw:8080 does not include it. The model was not called.");

        var note = DiscussionOrchestrator.failedSourcesNote(state);

        assertTrue(note.contains("Not every source answered"), note);
        assertTrue(note.contains("library-search"), note);
        assertTrue(note.contains("search_oreilly_content"), note);
        assertTrue(note.contains("does not include what these sources would have provided"), note);
    }

    @Test
    void severalFailedAgents_areListedByName() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("zeta", "z reason");
        state.failureSignals.put("alpha", "a reason");

        var note = DiscussionOrchestrator.failedSourcesNote(state);

        assertTrue(note.indexOf("- alpha: a reason") < note.indexOf("- zeta: z reason"), note);
    }

    @Test
    void theSynthesisInputCountsFailuresAndNamesThem() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("zeta", "x");
        state.failureSignals.put("alpha", "y");

        var context = DiscussionOrchestrator.buildSignalContext(state, 0);

        assertTrue(context.contains("- Failures: 2 (alpha, zeta)"), context);
    }

    @Test
    void theSynthesisInputReportsZeroFailures() {
        var context = DiscussionOrchestrator.buildSignalContext(new DiscussionOrchestrator.ThreadState("t1"), 0);

        assertTrue(context.contains("- Failures: 0\n"), context);
    }

    @Test
    void aLongReason_isCut() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("a", "x".repeat(5000));
        assertTrue(DiscussionOrchestrator.failedSourcesNote(state).length() < 1000);
    }
}
