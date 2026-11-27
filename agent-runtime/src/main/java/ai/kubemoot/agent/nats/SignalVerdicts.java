package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.signal.SignalCall;
import ai.kubemoot.agent.signal.SignalEffect;

import java.util.EnumMap;
import java.util.HashMap;
import java.util.Map;
import java.util.function.BiFunction;

/**
 * How an agent's signal-tool verdict goes on the bus: the message type its kind is published
 * as, the content, and the metadata that names the call. The kind's effect comes from the
 * runtime's table ({@link SignalEffect}); each effect has one entry here. The agent's reply text
 * is its finding and rides along as the content, after the verdict's own statement where the
 * statement is the point (a concern, a block, a question).
 */
final class SignalVerdicts {

    /** The message type of a question to a named agent; not a terminal signal. */
    static final String MSG_QUESTION = "question";
    /** The message type of a declared kind the runtime has no phase logic for. */
    static final String MSG_DECLARED = "signal";

    static final String FIELD_KIND = "signalKind";
    static final String FIELD_TO = "signalTo";
    static final String FIELD_STATEMENT = "signalStatement";
    static final String FIELD_REASON = "signalReason";
    static final String FIELD_EVIDENCE = "signalEvidence";
    /** On a declared kind outside the runtime's table: whether the archetype says it ends the turn. */
    static final String FIELD_TERMINAL = "signalTerminal";

    /** The failure type a no-data verdict is published with: the same as a tooler's failed gather. */
    static final String GATHER_FAILED = "gather_failed";

    /** What is published for a verdict. */
    record Verdict(String signal, String content, Map<String, Object> metadata) {}

    /** The finding the verdict goes with: the agent's reply and the descriptions of its files. */
    record Finding(String text, String files) {
        String contribution() {
            return DiscussionSubscriber.contribution(text, files);
        }

        /** The verdict's statement, then the finding: what a concern, a block or a question posts. */
        String after(String stated) {
            String body = text == null || text.isBlank() ? stated : stated + "\n\n" + text.strip();
            return DiscussionSubscriber.contribution(body, files);
        }
    }

    private static final Map<SignalEffect, BiFunction<SignalCall, Finding, Verdict>> BY_EFFECT =
            new EnumMap<>(SignalEffect.class);

    static {
        BY_EFFECT.put(SignalEffect.AGREE, (c, f) -> verdict(DiscussionSubscriber.SIGNAL_AGREE, f.contribution(), c));
        BY_EFFECT.put(SignalEffect.CONCERN, (c, f) -> verdict(DiscussionSubscriber.SIGNAL_CONCERN, f.after(c.stated()), c));
        BY_EFFECT.put(SignalEffect.BLOCK, (c, f) -> verdict(DiscussionSubscriber.SIGNAL_BLOCK, f.after(c.stated()), c));
        BY_EFFECT.put(SignalEffect.QUESTION, (c, f) -> verdict(MSG_QUESTION, f.after(c.stated()), c));
        BY_EFFECT.put(SignalEffect.STAND_ASIDE, (c, f) -> verdict(DiscussionSubscriber.SIGNAL_STAND_ASIDE, c.stated(), c));
        BY_EFFECT.put(SignalEffect.TOOL_GAP, (c, f) -> verdict(DiscussionSubscriber.SIGNAL_CONCERN, c.stated(), c));
        BY_EFFECT.put(SignalEffect.NO_DATA, SignalVerdicts::noData);
        BY_EFFECT.put(SignalEffect.DECLARED, SignalVerdicts::declared);
    }

    private SignalVerdicts() {
    }

    /** The message for {@code call} with {@code finding}; {@code terminal} is the archetype's flag for its kind. */
    static Verdict of(SignalCall call, Finding finding, boolean terminal) {
        var verdict = BY_EFFECT.get(call.effect()).apply(call, finding);
        if (call.effect() == SignalEffect.DECLARED) {
            verdict.metadata().put(FIELD_TERMINAL, terminal);
        }
        return verdict;
    }

    /** No data: a failure, the way a tooler's failed gather is one, with the agent's reason. */
    private static Verdict noData(SignalCall call, Finding finding) {
        var verdict = verdict(DiscussionSubscriber.SIGNAL_FAILURE,
                "No data: " + (call.stated().isEmpty() ? "the tools returned nothing usable" : call.stated()), call);
        verdict.metadata().put(MetaKeys.FAILURE_TYPE, GATHER_FAILED);
        verdict.metadata().put(MetaKeys.LAST_ERROR, call.stated());
        return verdict;
    }

    private static Verdict declared(SignalCall call, Finding finding) {
        return verdict(MSG_DECLARED, finding.after(call.stated()), call);
    }

    private static Verdict verdict(String signal, String content, SignalCall call) {
        var meta = new HashMap<String, Object>();
        meta.put(FIELD_KIND, call.kind());
        putIfPresent(meta, FIELD_TO, call.to());
        putIfPresent(meta, FIELD_STATEMENT, call.statement());
        putIfPresent(meta, FIELD_REASON, call.reason());
        putIfPresent(meta, FIELD_EVIDENCE, call.evidence());
        return new Verdict(signal, content, meta);
    }

    private static void putIfPresent(Map<String, Object> meta, String key, String value) {
        if (value != null && !value.isEmpty()) {
            meta.put(key, value);
        }
    }
}
