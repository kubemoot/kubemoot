package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The orchestrator's state rebuilt from a thread's log is what the log says, at every point
 * of a thread's life: for every prefix of a recorded thread, the rebuilt state's view equals
 * the view the reducer gives for the same events plus the takeover.
 */
class ThreadRebuilderTest extends ResumeRestartHarness {

    @Test
    void theRebuiltStateMatchesTheLogAtEveryPointOfAThread() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);

        for (int written = 1; written < rec.events().size(); written++) {
            var prefix = rec.events().subList(0, written);
            CoordinatorThreadState log = CoordinatorStateReducer.reduce(prefix);
            if (log.closed()) {
                continue;
            }
            var state = ThreadRebuilder.build(log);
            orchestrator.rearmDeadlines(state, log.pending, log.lastEventAt.toEpochMilli(), System.currentTimeMillis());

            ObjectNode data = MAPPER.createObjectNode();
            data.put("resumedAt", state.threadCreated.toString());
            var resumed = new CoordinatorEvent(THREAD + "/0/resumed/1", "coordinator_resumed", THREAD, 0, Instant.now(),
                    "c", data);
            var withTakeover = new ArrayList<>(prefix);
            withTakeover.add(resumed);

            assertEquals(CoordinatorStateReducer.reduce(withTakeover).view().withoutDeadlines(),
                    ThreadView.of(state).withoutDeadlines(),
                    "after event " + written + " (" + prefix.get(written - 1).type() + ")");
        }
    }

    @Test
    void theTextOfASignalComesFromTheMessageTheLogNamed() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterAgrees = 0;
        for (int i = 0; i < rec.events().size(); i++) {
            if (rec.events().get(i).data().path("signal").asText().equals("agree")) {
                afterAgrees = i + 1;
            }
        }
        CoordinatorThreadState log = CoordinatorStateReducer.reduce(rec.events().subList(0, afterAgrees));
        var state = ThreadRebuilder.build(log);
        String loggedId = log.agree.get("k8s-workloads");

        ThreadRebuilder.fillContent(state, log, MAPPER.readTree("{\"agentName\":\"k8s-workloads\",\"messageId\":\""
                + loggedId + "\",\"messageType\":\"agree\",\"content\":\"12 pods\"}"));
        ThreadRebuilder.fillContent(state, log, MAPPER.readTree("{\"agentName\":\"k8s-metrics\",\"messageId\":\"other\","
                + "\"messageType\":\"agree\",\"content\":\"stale\"}"));

        assertEquals("12 pods", state.agreeSignals.get("k8s-workloads"));
        assertEquals("", state.agreeSignals.get("k8s-metrics"), "a message the log did not name is not its text");
    }

    @Test
    void anAgentTheCoordinatorFailedForItsDeadlineGetsTheCoordinatorsWordsNotAMessagesText() throws Exception {
        var data = MAPPER.createObjectNode();
        data.put("agent", "slow");
        data.put("cause", "evaluation_timeout");
        data.put("synthesized", true);
        data.put("messageId", "");
        var opened = MAPPER.createObjectNode();
        opened.put("openedAt", Instant.now().toString());
        var events = List.of(
                new CoordinatorEvent("t/opened", "thread_opened", "t", 0, Instant.now(), "c", opened),
                new CoordinatorEvent("t/0/failure/slow/timeout", "failure_recorded", "t", 0, Instant.now(), "c", data));

        var state = ThreadRebuilder.build(CoordinatorStateReducer.reduce(events));

        assertTrue(state.failureSignals.get("slow").startsWith("Evaluation did not complete"));
    }
}
