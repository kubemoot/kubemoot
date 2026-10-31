package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

/**
 * The orchestrator and the reducer agree. The real orchestrator runs threads through
 * their phases (the model and the bus are faked, nothing else), every decision it makes
 * is written to a fake publisher, and after each step the state the orchestrator holds
 * must equal the state the reducer rebuilds from the events written so far.
 */
class CoordinatorEventRoundTripTest extends CoordinatorThreadTestBase {

    // --- the threads ---

    @Test
    void aThreadThroughAdvisoryEvaluationReviewSynthesisReopenPauseAndStop() throws Exception {
        String t = "t-full";
        send(t, "human", "thread_start", "How many pods run?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        assertEquals("ADVISORY", orchestrator.threadForTest(t).phase.name());
        agreesNow(t, "an external thread_start opened the thread and entered ADVISORY");

        advanceThroughAdvisory(t);
        agreesNow(t, "advisory convened the subcommittee and started the evaluation");

        send(t, "k8s-metrics", "triaging", "", null);
        agreesNow(t, "a triaging signal opens a deadline");
        send(t, "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}");
        agreesNow(t, "an evaluating signal sets the evaluation deadline");
        send(t, "k8s-workloads", "waiting", "", "{\"model\":\"qwen3:14b\"}");
        agreesNow(t, "a waiting signal records the model it waits for");
        send(t, "k8s-metrics", "agree", "11 pods", "{\"role\":\"researcher\"}");
        send(t, "k8s-workloads", "agree", "12 pods", null);
        send(t, "other-d", "contribution", "also 12", null);
        send(t, "other-e", "block", "stop", null);
        agreesNow(t, "both toolers agreed, a contribution counted as an agree, and one agent blocked");

        settle(t, EVALUATION_TO_REVIEW);
        agreesNow(t, "evaluation settled into review with the analyst on the roster");

        send(t, "k8s-analyst", "concern", "check restarts", null);
        agreesNow(t, "the analyst signalled in review");
        settle(t, REVIEW_TO_SYNTHESIS);
        awaitTypes(t, 1, "thread_closed");
        agreesNow(t, "synthesis closed the thread");

        send(t, "human", "reply", "And in kube-system?", null);
        agreesNow(t, "a human reply reopened the thread");
        send(t, "k8s-metrics", "evaluating", "", null);
        send(t, "dashboard", "thread_pause", "", null);
        agreesNow(t, "paused with the earlier phase saved");
        send(t, "dashboard", "thread_resume", "", null);
        agreesNow(t, "resumed to the saved phase");
        send(t, "dashboard", "stop_requested", "", null);
        awaitTypes(t, 2, "thread_closed");
        agreesNow(t, "a stop forced synthesis and the thread closed again");
        assertEquals(1, orchestrator.threadForTest(t).trace.round(), "one reopen, so round 1");
    }

    @Test
    void aThreadWithOneAgreeSkipsReviewAndRecordsFailureCapacityAndStandAside() throws Exception {
        build(false);
        String t = "t-skip";
        selection = SELECT_TWO;
        send(t, "human", "thread_start", "Count nodes", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        agreesNow(t, "evaluation started with two toolers");

        send(t, "k8s-metrics", "agree", "3 nodes", null);
        send(t, "k8s-workloads", "stand_aside", "no gpu", "{\"reason\":\"gpu-busy\",\"model\":\"qwen3:14b\"}");
        send(t, "other-a", "stand_aside", "too big", "{\"reason\":\"model-too-large\",\"model\":\"qwen3:32b\"}");
        send(t, "other-b", "failure", "tool down", "{\"failedTool\":\"pods_top, resources_list\"}");
        send(t, "other-c", "decline", "n/a", null);
        agreesNow(t, "agree, capacity stand-asides, a failure and a decline");

        settle(t, EVALUATION_TO_REVIEW);
        awaitTypes(t, 1, "thread_closed");
        agreesNow(t, "a lone tooler agree skipped review and closed the thread");
        assertTrue(eventLog.events().stream().anyMatch(e -> "skip_review".equals(e.data().path("reason").asText())),
                "the skip is recorded as the reason synthesis began");
    }

    @Test
    void aDirectAnswerIsRecordedAsASubcommitteeOfNobodyAndClosesTheThread() throws Exception {
        String t = "t-direct";
        selection = SELECT_NONE;
        send(t, "human", "thread_start", "What is 2+2?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitTypes(t, 1, "thread_closed");
        agreesNow(t, "the coordinator answered directly");
        var convened = eventLog.events().stream().filter(e -> "subcommittee_convened".equals(e.type())).findFirst();
        assertEquals("direct", convened.orElseThrow().data().path("path").asText());
    }

    @Test
    void anAgentPastItsDeadlineGetsAFailureTheCoordinatorRecords() throws Exception {
        String t = "t-timeout";
        send(t, "human", "thread_start", "Slow question", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        when(triageModel.timeoutSeconds()).thenReturn(-5);
        send(t, "k8s-metrics", "triaging", "", null);
        send("", "k8s-workloads", "waking", "", null);
        agreesNow(t, "triaging and waking set deadlines");

        settle(t, EVALUATION_TO_REVIEW);
        agreesNow(t, "the expired agents were failed by the coordinator");
        assertTrue(eventLog.events().stream().anyMatch(e -> "failure_recorded".equals(e.type())
                && e.data().path("synthesized").asBoolean()), "a synthesized failure is in the log");
    }

    @Test
    void aHeartbeatWritesNoEventAndMovesOnlyTheDeadline() throws Exception {
        String t = "t-beat";
        send(t, "human", "thread_start", "Long question", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "evaluating", "", null);
        int before = eventLog.events().size();

        send(t, "k8s-metrics", "heartbeat", "", null);

        assertEquals(before, eventLog.events().size(), "a heartbeat writes nothing");
        assertEquals(reduced(t).withoutDeadlines(), ThreadView.of(orchestrator.threadForTest(t)).withoutDeadlines());
    }

    @Test
    void replayingTheWholeLogTwiceGivesTheSameStateAsOnce() throws Exception {
        String t = "t-replay";
        selection = SELECT_TWO;
        send(t, "human", "thread_start", "Count", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "agree", "1", null);
        send(t, "k8s-workloads", "agree", "1", null);
        settle(t, EVALUATION_TO_REVIEW);
        send(t, "k8s-analyst", "agree", "checked", null);
        settle(t, REVIEW_TO_SYNTHESIS);
        awaitTypes(t, 1, "thread_closed");

        var once = CoordinatorStateReducer.reduce(forThread(t));
        var twice = CoordinatorStateReducer.reduce(concat(forThread(t), forThread(t)));

        assertEquals(once.view(), twice.view());
        assertEquals(ThreadView.of(orchestrator.threadForTest(t)), once.view());
    }

    @Test
    void everyEventIsWrittenToTheThreadsOwnSubject() throws Exception {
        String t = "t-subject";
        send(t, "human", "thread_start", "Count", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");

        assertEquals(List.of("kubemoot.coordinator.ns.crew." + t, "kubemoot.coordinator.ns.crew." + t),
                eventLog.subjects(), "thread_opened and phase_entered");
    }
}
