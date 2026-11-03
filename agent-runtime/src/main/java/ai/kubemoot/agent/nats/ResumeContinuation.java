package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;

import java.time.Instant;
import java.util.Collection;
import java.util.List;
import java.util.stream.Stream;

/**
 * What a coordinator does with a thread it took over, from the last decision its log holds.
 * A decision is written before its effect is carried out, so for each decision the effect
 * may or may not have happened; every effect here is repeatable, because the messages it
 * publishes carry ids made from the decision's event id and every consumer drops a repeat.
 *
 * <p>The phase sweep (every second) carries the rest: it settles EVALUATING and REVIEW from
 * the rebuilt roster and deadlines and starts the advisory step of a thread in ADVISORY, so
 * for those the continuation has nothing to start.
 *
 * <table>
 *   <caption>Last logged decision to action</caption>
 *   <tr><td>opened, phase SUBMITTED</td><td>{@value #NOTHING}: the request handed again starts it; an external thread waits for its thread_start</td></tr>
 *   <tr><td>phase ADVISORY</td><td>{@value #SWEEP}: the sweep starts the advisory step</td></tr>
 *   <tr><td>EVALUATING, advisory running, no subcommittee logged</td><td>{@value #RERUN_ADVISORY}</td></tr>
 *   <tr><td>EVALUATING, advisory running, subcommittee logged</td><td>{@value #ANNOUNCE_SUBCOMMITTEE}: same members, no second model call</td></tr>
 *   <tr><td>EVALUATING, roster written, no one has signalled</td><td>{@value #REPUBLISH_ADVISORY_READY}</td></tr>
 *   <tr><td>EVALUATING, roster written, signals in</td><td>{@value #SWEEP}</td></tr>
 *   <tr><td>EVALUATING, review roster written, REVIEW not entered</td><td>{@value #ENTER_REVIEW}</td></tr>
 *   <tr><td>REVIEW, no analyst has signalled</td><td>{@value #REPUBLISH_REVIEW_READY}</td></tr>
 *   <tr><td>REVIEW, analysts signalled</td><td>{@value #SWEEP}</td></tr>
 *   <tr><td>PAUSED</td><td>{@value #STAY_PAUSED}: a thread_resume message resumes it</td></tr>
 *   <tr><td>SYNTHESIZING, no answer logged</td><td>{@value #RESTART_SYNTHESIS}</td></tr>
 *   <tr><td>SYNTHESIZING, the coordinator's own answer (direct) not logged</td><td>{@value #ANSWER_DIRECTLY}</td></tr>
 *   <tr><td>SYNTHESIZING, answer logged</td><td>{@value #FINISH_CLOSE}: publish it, close, log the close</td></tr>
 *   <tr><td>CLOSED</td><td>not resumed</td></tr>
 * </table>
 */
final class ResumeContinuation {

    static final String NOTHING = "nothing";
    /** A message applied from the stream moved the thread out of the phase its log ended in; that move carries it on. */
    static final String MOVED_BY_REPLAY = "moved_by_replay";
    static final String SWEEP = "sweep";
    static final String RERUN_ADVISORY = "rerun_advisory";
    static final String ANNOUNCE_SUBCOMMITTEE = "announce_subcommittee";
    static final String REPUBLISH_ADVISORY_READY = "republish_advisory_ready";
    static final String ENTER_REVIEW = "enter_review";
    static final String REPUBLISH_REVIEW_READY = "republish_review_ready";
    static final String STAY_PAUSED = "stay_paused";
    static final String RESTART_SYNTHESIS = "restart_synthesis";
    static final String ANSWER_DIRECTLY = "answer_directly";
    static final String FINISH_CLOSE = "finish_close";

    private static final String BROADCAST_PATH = "broadcast";
    private static final String DIRECT_PATH = "direct";
    private static final String ADVISORY_FAILED = "Advisory generation failed";

    private ResumeContinuation() {
    }

    /** Carries on {@code state}, rebuilt from {@code log}; returns the name of the action taken. */
    static String run(DiscussionOrchestrator orchestrator, ThreadState state, CoordinatorThreadState log) {
        return switch (state.phase) {
            case SUBMITTED -> NOTHING;
            case ADVISORY -> SWEEP;
            case EVALUATING -> evaluating(orchestrator, state, log);
            case REVIEW -> review(orchestrator, state, log);
            case PAUSED -> STAY_PAUSED;
            case SYNTHESIZING -> synthesizing(orchestrator, state, log);
            case CLOSED -> NOTHING;
        };
    }

    private static String evaluating(DiscussionOrchestrator orchestrator, ThreadState state, CoordinatorThreadState log) {
        if (log.reviewRosterWritten) {
            orchestrator.enterReview(state);
            orchestrator.publishReviewReady(state, orchestrator.analystCircleFor(state));
            return ENTER_REVIEW;
        }
        if (log.advisoryPending) {
            return advisoryUnfinished(orchestrator, state, log);
        }
        if (noSignalsYet(log)) {
            orchestrator.publishAdvisoryReady(state, log.technologies, wisdom(log));
            return REPUBLISH_ADVISORY_READY;
        }
        return SWEEP;
    }

    /** The advisory step was running: carry out the convening that was logged, or run the step again. */
    private static String advisoryUnfinished(DiscussionOrchestrator orchestrator, ThreadState state,
                                             CoordinatorThreadState log) {
        if (log.selectionPath == null) {
            orchestrator.rerunAdvisory(state);
            return RERUN_ADVISORY;
        }
        if (DIRECT_PATH.equals(log.selectionPath)) {
            orchestrator.finishDirectAnswer(state, log.brief, List.copyOf(log.technologies));
            return ANSWER_DIRECTLY;
        }
        orchestrator.announceSubcommittee(state, List.copyOf(log.technologies), List.of(), wisdom(log), 0);
        return ANNOUNCE_SUBCOMMITTEE;
    }

    private static String review(DiscussionOrchestrator orchestrator, ThreadState state, CoordinatorThreadState log) {
        if (log.rosterSignalled.isEmpty()) {
            if (log.convenings > 0) {
                orchestrator.publishSecondReview(state, log.convenedAnalysts, log.convenedAssignments);
            } else {
                orchestrator.publishReviewReady(state, orchestrator.analystCircleFor(state));
            }
            return REPUBLISH_REVIEW_READY;
        }
        return SWEEP;
    }

    private static String synthesizing(DiscussionOrchestrator orchestrator, ThreadState state, CoordinatorThreadState log) {
        String channel = DiscussionOrchestrator.resolveChannel(state);
        if (log.synthesisOutcome != null) {
            orchestrator.finishClose(state, channel, log.answer, log.synthesisOutcome);
            return FINISH_CLOSE;
        }
        if (DIRECT_PATH.equals(log.selectionPath)) {
            orchestrator.finishDirectAnswer(state, log.brief, List.copyOf(log.technologies));
            return ANSWER_DIRECTLY;
        }
        orchestrator.beginSynthesis(state, Instant.now());
        return RESTART_SYNTHESIS;
    }

    /** True when no agent has said anything in this evaluation: nobody is pending and no signal is in. */
    private static boolean noSignalsYet(CoordinatorThreadState log) {
        return Stream.<Collection<?>>of(log.pending.keySet(), log.rosterSignalled, log.agree.keySet(),
                log.concern.keySet(), log.block.keySet(), log.standAside, log.failure.keySet())
                .allMatch(Collection::isEmpty);
    }

    /** The advisory text the evaluation was released with: the logged brief, or the wording a failed advisory step uses. */
    private static String wisdom(CoordinatorThreadState log) {
        return log.brief == null && BROADCAST_PATH.equals(log.selectionPath) ? ADVISORY_FAILED : log.brief;
    }
}
