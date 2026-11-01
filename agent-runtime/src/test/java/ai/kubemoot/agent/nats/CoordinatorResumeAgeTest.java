package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A thread whose last logged event is older than the resume age limit is closed, not resumed:
 * its asker stopped waiting long ago and resuming it would spend accelerator time for nobody.
 */
class CoordinatorResumeAgeTest extends ResumeRestartHarness {

    private static final Duration LIMIT = Duration.ofMinutes(30);
    private static final long REQUEST_SEQ = 42;
    /** Strings the chart and the runtime both refuse (the chart's test-kubemootconfig-images.sh lists the same). */
    private static final String[] NOT_DURATIONS = {"soon", "30", "-5m", "5d", "30 m", ".5s", "1.s", "\u00b5s", "1h-30m"};

    /** The log as a coordinator that stopped after the evaluation started left it, and when that coordinator last wrote. */
    private long logAfterEvaluationStarted(boolean withRequest) throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int written = 0;
        for (int i = 0; i < rec.events().size(); i++) {
            if (rec.events().get(i).type().equals("evaluation_started")) {
                written = i + 1;
            }
        }
        published.clear();
        timeline.clear();
        build(true);
        List<CoordinatorEvent> events = new ArrayList<>(rec.events().subList(0, written));
        if (withRequest) {
            events = CoordinatorResumeTest.withRequestSeq(events, REQUEST_SEQ);
        }
        eventLog.preload(rec.subjects().get(0), events);
        return events.get(events.size() - 1).at().toEpochMilli();
    }

    private void restartAt(long lastLoggedMillis, Duration afterLast, Duration limit) throws Exception {
        startResume(List.of(), () -> { }, limit, () -> lastLoggedMillis + afterLast.toMillis());
    }

    private double count(String action) {
        var counter = resumeRegistry.find("kubemoot_coordinator_resumes_total").tag("action", action).counter();
        return counter == null ? 0 : counter.count();
    }

    @Test
    void aThreadJustUnderTheLimitIsResumed() throws Exception {
        long last = logAfterEvaluationStarted(false);

        restartAt(last, LIMIT.minusSeconds(1), LIMIT);

        assertTrue(forThread(THREAD).stream().anyMatch(e -> e.type().equals("coordinator_resumed")));
        assertEquals(0, count("expired"));
        assertFalse(forThread(THREAD).stream().anyMatch(e -> e.type().equals("thread_closed")));
    }

    @Test
    void aThreadExactlyAtTheLimitIsResumed() throws Exception {
        long last = logAfterEvaluationStarted(false);

        restartAt(last, LIMIT, LIMIT);

        assertTrue(forThread(THREAD).stream().anyMatch(e -> e.type().equals("coordinator_resumed")));
        assertEquals(0, count("expired"));
    }

    @Test
    void aThreadJustOverTheLimitIsClosedNotResumed() throws Exception {
        long last = logAfterEvaluationStarted(false);

        restartAt(last, LIMIT.plusSeconds(1), LIMIT);

        List<CoordinatorEvent> events = forThread(THREAD);
        CoordinatorEvent end = events.get(events.size() - 1);
        assertEquals("thread_closed", end.type());
        assertEquals("expired", end.data().path("reason").asText());
        assertFalse(events.stream().anyMatch(e -> e.type().equals("coordinator_resumed")), "the thread was not taken over");
        assertEquals(1, idsOf(published, "thread_close").size(), "its agents are told to stop");
        assertEquals("CLOSED", orchestrator.threadForTest(THREAD).phase.name());
        assertEquals(1.0, count("expired"));
    }

    @Test
    void withTheLimitOffAThreadOfAnyAgeIsResumed() throws Exception {
        long last = logAfterEvaluationStarted(false);

        restartAt(last, Duration.ofDays(10), Duration.ZERO);

        assertTrue(forThread(THREAD).stream().anyMatch(e -> e.type().equals("coordinator_resumed")));
        assertEquals(0, count("expired"));
    }

    @Test
    void aRequestHandedAgainForAnExpiredThreadStartsFresh() throws Exception {
        long last = logAfterEvaluationStarted(true);

        restartAt(last, LIMIT.plusSeconds(1), LIMIT);

        assertNull(orchestrator.resume().matchRequest(REQUEST_SEQ), "the expired thread does not answer for its request");
    }

    @Test
    void aThreadThatExpiredBeforeAnEarlierRestartIsNotRememberedForItsRequest() throws Exception {
        long last = logAfterEvaluationStarted(true);
        restartAt(last, LIMIT.plusSeconds(1), LIMIT);
        var closedLog = new ArrayList<>(eventLog.events());
        String subject = eventLog.subjects().get(0);

        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(subject, closedLog);
        startResume(List.of());

        assertNull(orchestrator.resume().matchRequest(REQUEST_SEQ));
    }

    // --- the setting ---

    @Test
    void goDurationsAreParsed() {
        assertEquals(Optional.of(Duration.ofMinutes(30)), ResumeAge.parse("30m"));
        assertEquals(Optional.of(Duration.ofMinutes(90)), ResumeAge.parse("1h30m"));
        assertEquals(Optional.of(Duration.ofSeconds(90)), ResumeAge.parse("90s"));
        assertEquals(Optional.of(Duration.ofMillis(1500)), ResumeAge.parse("1.5s"));
        assertEquals(Optional.of(Duration.ofMillis(250)), ResumeAge.parse("250ms"));
        assertEquals(Optional.of(Duration.ofSeconds(5400)), ResumeAge.parse("1.5h"));
        assertEquals(Optional.of(Duration.ZERO), ResumeAge.parse("0s"), "a zero with a unit is also no limit");
    }

    @Test
    void theChartAndTheRuntimeAcceptTheSameDurations() {
        // The chart's test-kubemootconfig-images.sh refuses these same strings.
        for (String bad : NOT_DURATIONS) {
            assertEquals(Optional.empty(), ResumeAge.parse(bad), bad);
        }
        for (String good : new String[] {"30m", "1h30m", "1.5h", "0s", "250ms", "90s", "1h1m1s", "1.5ns"}) {
            assertTrue(ResumeAge.parse(good).isPresent(), good);
        }
    }

    @Test
    void aValueTooLargeToHoldIsNotADuration() {
        assertEquals(Optional.empty(), ResumeAge.parse("99999999999999h"));
        assertEquals(Optional.empty(), ResumeAge.parse("9999999999999h9999999999999h"));
    }

    @Test
    void theSettingIsThirtyMinutesWhenUnsetAndWhenNotADurationAndWhatItNamesOtherwise() {
        var told = new ArrayList<String>();

        assertEquals(ResumeAge.DEFAULT, ResumeAge.from(Optional.empty(), told::add));
        assertEquals(Duration.ofMinutes(45), ResumeAge.from(Optional.of("45m"), told::add));
        assertEquals(Duration.ZERO, ResumeAge.from(Optional.of("0"), told::add), "0 turns the limit off");
        assertEquals(Duration.ZERO, ResumeAge.from(Optional.of(""), told::add), "empty turns the limit off");
        assertTrue(told.isEmpty(), "nothing was reported for valid values");
        assertEquals(ResumeAge.DEFAULT, ResumeAge.from(Optional.of("soon"), told::add));
        assertEquals(List.of("soon"), told, "a value that is not a duration is reported, and the default is used");
    }

    @Test
    void zeroOrEmptyMeansNoLimit() {
        assertEquals(Optional.of(Duration.ZERO), ResumeAge.parse("0"));
        assertEquals(Optional.of(Duration.ZERO), ResumeAge.parse(""));
        assertEquals(Optional.of(Duration.ZERO), ResumeAge.parse("  "));
        assertEquals(Optional.of(Duration.ZERO), ResumeAge.parse(null));
    }

    @Test
    void anythingElseIsNotADuration() {
        for (String bad : NOT_DURATIONS) {
            assertEquals(Optional.empty(), ResumeAge.parse(bad), bad);
        }
        assertEquals(Optional.empty(), ResumeAge.parse("m30"));
    }
}
