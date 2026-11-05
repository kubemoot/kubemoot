package ai.kubemoot.agent.chat;

import java.util.HashSet;
import java.util.Set;

/**
 * Tells a tool that keeps failing the same way from a model that is correcting itself. One
 * tracker holds the failures of one tool in one evaluation. An error is a repeat when the call
 * (same arguments after normalizing whitespace) was already seen failing or when the error text
 * is one the tool already returned; a repeat means the model is stuck. An error with new
 * arguments and new text is a correction, and corrections have a budget of their own.
 */
final class ToolFailureTracker {

    /** What one more failure means for the loop. */
    enum Verdict { CONTINUE, STUCK, CORRECTIONS_EXHAUSTED }

    private final int maxRepeats;
    private final int maxFailures;
    private final Set<String> failedCalls = new HashSet<>();
    private final Set<String> errorTexts = new HashSet<>();
    private int failures;

    ToolFailureTracker(int maxRepeats, int maxFailures) {
        this.maxRepeats = maxRepeats;
        this.maxFailures = maxFailures;
    }

    /**
     * Record one failed call. {@code knownRepeat} marks a call the runtime refused as an identical
     * repeat, which counts as a repeat whatever the tracker has seen.
     */
    Verdict record(String arguments, String errorText, boolean knownRepeat) {
        failures++;
        boolean newCall = failedCalls.add(ToolCallRepeatGuard.canonical(arguments));
        boolean newError = errorTexts.add(normalize(errorText));
        boolean repeat = knownRepeat || !newCall || !newError;
        if (repeat && failures >= maxRepeats) {
            return Verdict.STUCK;
        }
        return failures >= maxFailures ? Verdict.CORRECTIONS_EXHAUSTED : Verdict.CONTINUE;
    }

    /** Failures of this tool so far, repeats and corrections together. */
    int failures() {
        return failures;
    }

    private static String normalize(String text) {
        return text == null ? "" : text.strip().replaceAll("\\s+", " ");
    }
}
