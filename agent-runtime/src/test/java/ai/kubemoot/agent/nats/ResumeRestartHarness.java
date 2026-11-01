package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;

/**
 * Runs a scripted thread to its end, then restarts the coordinator after every event the run
 * wrote and runs the rest of the script on the new coordinator. The bus and the log the new
 * coordinator finds are what the first one left at that point: the events written so far, the
 * messages agents sent so far, and (a test chooses) the messages the first coordinator had or
 * had not yet published after its last event. The model and the bus are fakes; the
 * orchestrator, the recorder, the reducer and the resume are real.
 */
abstract class ResumeRestartHarness extends CoordinatorThreadTestBase {

    protected static final String THREAD = "t-restart";
    private static final String START_META = "{\"crew\":\"crew\",\"namespace\":\"ns\"}";

    @Override
    protected boolean withBus() {
        return true;
    }

    // --- scripts ---

    /** One thing that happens to a thread: an agent sends a message, or the phase sweep runs. */
    protected interface Step {
    }

    protected record Send(String agent, String type, String content, String metadata) implements Step {
    }

    protected record Sweep(int secondsLater) implements Step {
    }

    /** What a thread came to, for comparing a run that was restarted with one that was not. */
    protected record Outcome(String phase, String closeReason, String synthesisOutcome, String answer,
                             Set<String> agree, Set<String> concern, Set<String> block, Set<String> standAside,
                             Set<String> failure, Set<String> innerCircle, int round) {
    }

    /** A finished, uninterrupted run: everything a restart needs to reproduce what the coordinator left. */
    protected record Recorded(List<Step> script, List<CoordinatorEvent> events, List<String> subjects,
                              List<Integer> eventPositions, List<Published> publishes, List<Integer> publishPositions,
                              List<Inbound> inbound, List<Integer> inboundSteps, List<Integer> stepStarts,
                              Outcome outcome) {
    }

    protected static List<Step> fullScript() {
        return List.of(
                new Send("human", "thread_start", "How many pods run?", START_META),
                new Sweep(0),
                new Send("k8s-metrics", "triaging", "", null),
                new Send("k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}"),
                new Send("k8s-workloads", "waiting", "", "{\"model\":\"qwen3:14b\"}"),
                new Send("k8s-metrics", "agree", "11 pods", "{\"role\":\"researcher\"}"),
                new Send("k8s-workloads", "agree", "12 pods", null),
                new Send("other-d", "contribution", "also 12", null),
                new Send("other-e", "block", "stop", null),
                new Sweep(60),
                new Send("k8s-analyst", "concern", "check restarts", null),
                new Sweep(60));
    }

    protected static List<Step> skipReviewScript() {
        return List.of(
                new Send("human", "thread_start", "Count nodes", START_META),
                new Sweep(0),
                new Send("k8s-metrics", "agree", "3 nodes", null),
                new Send("k8s-workloads", "stand_aside", "no gpu", "{\"reason\":\"gpu-busy\",\"model\":\"qwen3:14b\"}"),
                new Send("other-b", "failure", "tool down", "{\"failedTool\":\"pods_top\"}"),
                new Sweep(60));
    }

    protected static List<Step> directScript() {
        return List.of(
                new Send("human", "thread_start", "What is 2+2?", START_META),
                new Sweep(0));
    }

    // --- running ---

    /** Runs {@code script} on the current orchestrator from step {@code from}; returns where each step began. */
    protected void runSteps(List<Step> script, int from, List<Integer> stepStarts, List<Integer> inboundSteps)
            throws InterruptedException {
        for (int i = from; i < script.size(); i++) {
            if (stepStarts != null) {
                stepStarts.add(timeline.size());
            }
            int before = inbound.size();
            runStep(script.get(i));
            if (inboundSteps != null) {
                for (int j = before; j < inbound.size(); j++) {
                    inboundSteps.add(i);
                }
            }
        }
    }

    private void runStep(Step step) throws InterruptedException {
        if (step instanceof Send send) {
            try {
                send(THREAD, send.agent(), send.type(), send.content(), send.metadata());
            } catch (Exception e) {
                throw new IllegalStateException(e);
            }
        } else if (step instanceof Sweep sweep) {
            sweep(sweep.secondsLater());
        }
        quiesce();
    }

    protected void sweep(int secondsLater) {
        var thread = orchestrator.threadForTest(THREAD);
        if (thread != null) {
            orchestrator.checkThreadTransition(thread, Instant.now().plusSeconds(secondsLater));
        }
    }

    /** Waits until nothing has been written or published for a moment: asynchronous steps have finished. */
    protected void quiesce() throws InterruptedException {
        int last = -1;
        int stable = 0;
        Instant deadline = Instant.now().plus(Duration.ofSeconds(10));
        while (stable < 8 && Instant.now().isBefore(deadline)) {
            int size = timeline.size();
            stable = size == last ? stable + 1 : 0;
            last = size;
            Thread.sleep(15);
        }
    }

    /** Runs {@code script} from the start on a fresh orchestrator and records everything a restart needs. */
    protected Recorded record(List<Step> script, boolean hasAnalysts, String selectionJson) throws Exception {
        published.clear();
        timeline.clear();
        inbound.clear();
        build(hasAnalysts);
        selection = selectionJson;
        var stepStarts = new ArrayList<Integer>();
        var inboundSteps = new ArrayList<Integer>();
        runSteps(script, 0, stepStarts, inboundSteps);
        awaitTypes(THREAD, 1, "thread_closed");
        quiesce();

        var eventPositions = new ArrayList<Integer>();
        var publishPositions = new ArrayList<Integer>();
        synchronized (timeline) {
            for (int i = 0; i < timeline.size(); i++) {
                (timeline.get(i).startsWith("event:") ? eventPositions : publishPositions).add(i);
            }
        }
        List<CoordinatorEvent> events = forThread(THREAD);
        return new Recorded(script, events, eventLog.subjects(), eventPositions, List.copyOf(published),
                publishPositions, List.copyOf(inbound), inboundSteps, stepStarts, outcomeOf(events));
    }

    /** The result of a restart: the new coordinator's view of the end, and every message that reached the bus in both lives. */
    protected record Restarted(Outcome outcome, List<CoordinatorEvent> events, List<Published> allPublishes,
                               String action) {
    }

    /**
     * Restarts after the first {@code written} events. The new coordinator finds those events, the
     * messages agents sent up to the step the last of them belongs to, and the coordinator's own
     * messages published before the next event ({@code effectsDone}) or before the last one (not).
     */
    protected Restarted restartAfter(Recorded rec, int written, boolean effectsDone, boolean hasAnalysts,
                                     String selectionJson) throws Exception {
        return restartAfter(rec, written, effectsDone, hasAnalysts, selectionJson, 0, () -> { });
    }

    /**
     * {@link #restartAfter(Recorded, int, boolean, boolean, String)} where the agents sent the
     * messages of the next {@code sentWhileDown} steps while no coordinator ran, so they are on the
     * stream but not in the log; {@code beforeLaunch} runs after the log is read and before the
     * threads are resumed, when live messages are held back.
     */
    protected Restarted restartAfter(Recorded rec, int written, boolean effectsDone, boolean hasAnalysts,
                                     String selectionJson, int sentWhileDown, Runnable beforeLaunch) throws Exception {
        int lastPos = rec.eventPositions().get(written - 1);
        int nextPos = written < rec.events().size() ? rec.eventPositions().get(written) : Integer.MAX_VALUE;
        int stepOfLast = stepOf(rec, lastPos);
        var beforeCrash = new ArrayList<Published>();
        List<TimedMessage> bus = busAtCrash(rec, stepOfLast + sentWhileDown, effectsDone ? nextPos : lastPos,
                beforeCrash);

        published.clear();
        timeline.clear();
        inbound.clear();
        build(hasAnalysts);
        selection = selectionJson;
        eventLog.preload(rec.subjects().get(0), rec.events().subList(0, written));
        startResume(bus, beforeLaunch);

        Step stepOfLastEvent = rec.script().get(stepOfLast);
        boolean repeatsTheSweep = stepOfLastEvent instanceof Sweep && sentWhileDown == 0;
        int next = (repeatsTheSweep ? stepOfLast : stepOfLast + 1) + sentWhileDown;
        runSteps(rec.script(), next, null, null);
        if (written < rec.events().size()) {
            awaitTypes(THREAD, 1, "thread_closed");
        }
        quiesce();

        var all = new ArrayList<>(beforeCrash);
        all.addAll(published);
        List<CoordinatorEvent> events = forThread(THREAD);
        return new Restarted(outcomeOf(events), events, all, actionTaken());
    }

    /** The step that was running when the event at timeline position {@code position} was written. */
    private static int stepOf(Recorded rec, int position) {
        int step = 0;
        for (int i = 0; i < rec.stepStarts().size(); i++) {
            if (rec.stepStarts().get(i) <= position) {
                step = i;
            }
        }
        return step;
    }

    /**
     * What the discussion stream holds when the coordinator stops: the messages agents sent in the
     * steps up to {@code stepOfLast}, and the coordinator's own published before {@code publishedBefore}
     * (also added to {@code coordinatorSent}), in the order they happened.
     */
    private static List<TimedMessage> busAtCrash(Recorded rec, int stepOfLast, int publishedBefore,
                                                 List<Published> coordinatorSent) {
        var bus = new ArrayList<TimedMessage>();
        for (int i = 0; i < rec.inbound().size(); i++) {
            Inbound message = rec.inbound().get(i);
            if (rec.inboundSteps().get(i) <= stepOfLast) {
                bus.add(new TimedMessage(message.position(), 0, message.subject(), message.json()));
            }
        }
        for (int i = 0; i < rec.publishes().size(); i++) {
            Published message = rec.publishes().get(i);
            int position = rec.publishPositions().get(i);
            if (position < publishedBefore) {
                coordinatorSent.add(message);
                bus.add(new TimedMessage(position, 1, message.subject(), message.body().toString()));
            }
        }
        bus.sort(Comparator.comparingInt(TimedMessage::position).thenComparingInt(TimedMessage::kind));
        return bus;
    }

    /** Starts a resume on the current orchestrator over the log it holds and the given discussion stream. */
    protected void startResume(List<TimedMessage> bus) throws InterruptedException {
        startResume(bus, () -> { });
    }

    /** As {@link #startResume(List)}; {@code beforeLaunch} runs between reading the log and resuming the threads. */
    protected void startResume(List<TimedMessage> bus, Runnable beforeLaunch) throws InterruptedException {
        resumeRegistry = new SimpleMeterRegistry();
        resumeMetrics = new DiscussionMetrics(resumeRegistry);
        var reader = new FakeBusReader();
        eventLog.asMessages().forEach(m -> reader.put(CrewScope.COORDINATOR_STREAM, m));
        bus.forEach(m -> reader.publish(CoordinatorResume.DISCUSS_STREAM, m.subject(), m.json().getBytes()));
        var resume = new CoordinatorResume(true, orchestrator, reader, () -> CrewScope.of("ns", "crew"), MAPPER,
                resumeMetrics, System::currentTimeMillis);
        orchestrator.useResume(resume);
        resume.discover();
        beforeLaunch.run();
        resume.launch(Runnable::run);
        quiesce();
    }

    /** A message on the discussion stream: its place on the timeline, who sent it (0 an agent, 1 the coordinator), where, and what. */
    protected record TimedMessage(int position, int kind, String subject, String json) {
    }

    /** Counts what the resumes did, by action. */
    protected SimpleMeterRegistry resumeRegistry = new SimpleMeterRegistry();
    protected DiscussionMetrics resumeMetrics = new DiscussionMetrics(resumeRegistry);

    private String actionTaken() {
        return resumeRegistry.getMeters().stream()
                .filter(m -> "kubemoot_coordinator_resumes_total".equals(m.getId().getName()))
                .map(m -> m.getId().getTag("action")).findFirst().orElse("none");
    }

    protected static Outcome outcomeOf(List<CoordinatorEvent> events) {
        CoordinatorThreadState s = CoordinatorStateReducer.reduce(events);
        return new Outcome(s.phase, s.closeReason, s.synthesisOutcome, s.answer, new TreeSet<>(s.agree.keySet()),
                new TreeSet<>(s.concern.keySet()), new TreeSet<>(s.block.keySet()), new TreeSet<>(s.standAside),
                new TreeSet<>(s.failure.keySet()), new TreeSet<>(s.innerCircle), s.round);
    }

    /** The distinct message ids of the published messages of {@code type}. */
    protected static Set<String> idsOf(List<Published> messages, String type) {
        var ids = new TreeSet<String>();
        messages.stream().filter(m -> m.type().equals(type)).forEach(m -> ids.add(m.messageId()));
        return ids;
    }

    protected static JsonNode parse(String json) throws Exception {
        return MAPPER.readTree(json);
    }
}
