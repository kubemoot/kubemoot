package ai.kubemoot.agent.nats;

import java.time.Instant;
import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/**
 * One thread's ratification: the draft on the table, the round, who was asked, what each agent
 * answered, and the rounds before. Plain state with no clock and no bus, so the rules (a reply
 * counts only for the current round and only from the roster, a revision starts a new round, what
 * is released and with which objections) are testable alone. The orchestrator drives it.
 *
 * <p>Methods are synchronized: replies arrive on the bus thread, the phase check and the revision
 * run on the coordinator's executor.
 */
final class RatifyState {

    /** One agent's verdict on one round's draft. */
    record Verdict(String agent, RatifyReply.Kind kind, String text, int round) {}

    /** How the ratification ended. */
    static final String RATIFIED = "ratified";
    static final String RELEASED_WITH_OBJECTIONS = "released_with_objections";
    static final String NOT_RATIFIED = "not_ratified";

    private static final String UNRATIFIED_HEADING = "The crew did not settle on this answer. Objections that stand:";
    private static final String ABSENT_HEADING = "Not checked by these agents (they did not answer):";

    private String draft = "";
    private int round;
    private Set<String> roster = Set.of();
    private final Map<String, Verdict> replies = new LinkedHashMap<>();
    private final List<Verdict> earlierObjections = new ArrayList<>();
    private boolean revising;
    private boolean released;
    private String draftId = "";
    private Instant startedAt = Instant.EPOCH;
    private List<Verdict> answered = List.of();

    /** Starts round 1 with {@code firstDraft} put to {@code asked}; {@code synthesisStart} is when the draft was begun. */
    synchronized void begin(String firstDraft, Collection<String> asked, Instant synthesisStart) {
        draft = firstDraft == null ? "" : firstDraft;
        round = 1;
        startedAt = synthesisStart;
        released = false;
        answered = List.of();
        draftId = newDraftId();
        roster = new LinkedHashSet<>(asked);
        replies.clear();
        earlierObjections.clear();
        revising = false;
    }

    synchronized boolean active() {
        return round > 0;
    }

    /**
     * Names the draft now on the table. A reply carries it back, so a late reply to an earlier draft
     * (before a restart, a reopened thread or a revision) is never taken for a verdict on this one.
     */
    synchronized String draftId() {
        return draftId;
    }

    private static String newDraftId() {
        return java.util.UUID.randomUUID().toString();
    }

    /** Forgets the ratification, as a reopened thread starts over. */
    synchronized void reset() {
        round = 0;
        draft = "";
        draftId = "";
        roster = Set.of();
        replies.clear();
        earlierObjections.clear();
        answered = List.of();
        revising = false;
        released = false;
    }

    /** When the draft was begun, for the synthesis duration. */
    synchronized Instant startedAt() {
        return startedAt;
    }

    /** Claims the release: true for the first caller only, so a stop and a settle cannot both release. */
    synchronized boolean claimRelease() {
        if (released) {
            return false;
        }
        released = true;
        return true;
    }

    synchronized int round() {
        return round;
    }

    synchronized String draft() {
        return draft;
    }

    synchronized Set<String> roster() {
        return Set.copyOf(roster);
    }

    /**
     * Records {@code agent}'s answer to the draft {@code answeredDraftId} of {@code answeredRound}. False,
     * and nothing recorded, when it is not the draft on the table, the agent is not on the roster, or it
     * answered already.
     */
    synchronized boolean record(String agent, RatifyReply.Kind kind, String text, int answeredRound,
                                String answeredDraftId) {
        if (answeredRound != round || !draftId.equals(answeredDraftId) || !roster.contains(agent)
                || replies.containsKey(agent)) {
            return false;
        }
        replies.put(agent, new Verdict(agent, kind, text == null ? "" : text.strip(), round));
        return true;
    }

    /** True when every agent on the roster has answered this round, by reply or by failing. */
    synchronized boolean everyoneAnswered() {
        return round > 0 && replies.keySet().containsAll(roster);
    }

    /** The objections (concerns and blocks) of this round, in the order they arrived. */
    synchronized List<Verdict> objections() {
        return replies.values().stream().filter(v -> v.kind().objects()).toList();
    }

    /** The agents of this round that failed to answer. */
    synchronized List<String> failures() {
        return replies.values().stream().filter(v -> v.kind() == RatifyReply.Kind.FAILURE)
                .map(Verdict::agent).toList();
    }

    /** The reason a failed agent gave, or an empty string. */
    synchronized String failureText(String agent) {
        Verdict v = replies.get(agent);
        return v == null ? "" : v.text();
    }

    /** Claims the revision: false when one is already under way, so a settle tick cannot start two. */
    synchronized boolean beginRevision() {
        if (revising || released) {
            return false;
        }
        revising = true;
        return true;
    }

    /** The revision could not be written; the draft stays and the objections stand. */
    synchronized void abandonRevision() {
        revising = false;
    }

    /**
     * Puts {@code revised} to the same roster as the next round, and false when the draft was released
     * meanwhile. The objections of the round it answers are kept, so a release that follows can show
     * what was raised before.
     */
    synchronized boolean revise(String revised) {
        if (released) {
            return false;
        }
        answered = objections();
        earlierObjections.addAll(answered);
        draft = revised == null ? draft : revised;
        round++;
        draftId = newDraftId();
        replies.clear();
        revising = false;
        return true;
    }

    /**
     * The agents that did not ratify: those that failed and those that have not answered. An early
     * release (a stop, the hard ceiling) leaves some unanswered, and silence is never consent.
     */
    synchronized List<String> absent() {
        List<String> absent = new ArrayList<>(failures());
        roster.stream().filter(a -> !replies.containsKey(a)).sorted().forEach(absent::add);
        return absent;
    }

    /** How this ended: ratified, released with standing objections, or not ratified by an absent roster member. */
    synchronized String outcome() {
        if (!objections().isEmpty()) {
            return RELEASED_WITH_OBJECTIONS;
        }
        return absent().isEmpty() ? RATIFIED : NOT_RATIFIED;
    }

    /**
     * The answer as released: the draft, and when objections stand or agents did not answer, the
     * deterministic block that names them. An objection is never left out of a released answer.
     */
    synchronized String releaseText() {
        StringBuilder out = new StringBuilder(draft);
        List<Verdict> standing = objections();
        if (!standing.isEmpty()) {
            out.append("\n\n").append(UNRATIFIED_HEADING);
            standing.forEach(v -> out.append("\n- ").append(v.agent()).append(" (").append(v.kind().signal())
                    .append("): ").append(v.text().isEmpty() ? "no reason given" : v.text()));
        }
        List<String> absent = absent();
        if (!absent.isEmpty()) {
            out.append("\n\n").append(ABSENT_HEADING);
            absent.forEach(a -> out.append("\n- ").append(a));
        }
        return out.toString();
    }

    /** What the bus carries as {@code metadata.ratification}: the state, the rounds, and who said what. */
    synchronized Map<String, Object> metadata() {
        Map<String, Object> meta = new TreeMap<>();
        meta.put("state", outcome());
        meta.put("rounds", round);
        meta.put("roster", List.copyOf(roster));
        meta.put("objections", objections().stream().map(RatifyState::describe).toList());
        meta.put("earlierObjections", earlierObjections.stream().map(RatifyState::describe).toList());
        meta.put("failures", absent());
        return meta;
    }

    /** The objections the latest revision answers, as the revision's {@code metadata.objections}. */
    synchronized List<Map<String, Object>> answeredObjections() {
        return answered.stream().map(RatifyState::describe).toList();
    }

    private static Map<String, Object> describe(Verdict v) {
        Map<String, Object> m = new TreeMap<>();
        m.put("agent", v.agent());
        m.put("verdict", v.kind().signal());
        m.put("round", v.round());
        m.put("content", v.text());
        return m;
    }
}
