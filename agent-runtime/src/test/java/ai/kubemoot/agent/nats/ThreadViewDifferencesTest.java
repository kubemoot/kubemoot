package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Naming the fields in which the log's view of a thread and its live view differ. Each
 * test takes the "log" view of a thread, changes the thread, and takes the "live" view.
 */
class ThreadViewDifferencesTest {

    private final ThreadState thread = new ThreadState("t1");

    @Test
    void equalViewsDifferInNothing() {
        assertEquals(List.of(), ThreadView.of(thread).differences(ThreadView.of(thread)));
    }

    @Test
    void aDifferingFieldIsNamedWithBothValues() {
        var logged = ThreadView.of(thread);
        thread.agreeSignals.put("metrics", "11 pods");
        thread.advisoryInputTokens = 5;

        List<String> differences = logged.differences(ThreadView.of(thread));

        assertEquals(List.of("signals.agree: log=[] live=[metrics]", "tokens.advisory: log=0/0 live=5/0"),
                differences);
    }

    @Test
    void aMovedDeadlineIsNotADifferenceButAMissingPendingAgentIs() {
        thread.pendingEvaluations.put("metrics", 1000L);
        var logged = ThreadView.of(thread);

        thread.pendingEvaluations.put("metrics", 31_000L);
        assertTrue(logged.differences(ThreadView.of(thread)).isEmpty(), "a heartbeat moved it");

        thread.pendingEvaluations.put("workloads", 5L);
        assertEquals(List.of("roster.pending: log=[metrics] live=[metrics, workloads]"),
                logged.differences(ThreadView.of(thread)));
    }

    @Test
    void withoutDeadlinesZeroesEachPendingDeadline() {
        thread.pendingEvaluations.put("metrics", 1000L);

        assertEquals(Map.of("metrics", 0L), ThreadView.of(thread).withoutDeadlines().roster().pending());
    }
}
