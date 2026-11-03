package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.PromptWindow;
import ai.kubemoot.agent.chat.SimplePrompt;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;
import java.util.function.BooleanSupplier;
import java.util.function.Function;
import java.util.function.LongSupplier;
import java.util.function.ToLongFunction;
import java.util.regex.Matcher;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Gets a thread's stored tool results to the model that writes the answer, in as many rounds as
 * the evidence needs (the "refine" pattern). Every round sizes its prompt to the window of the model
 * the scheduler picked for that round, so a round that lands on a smaller window takes a smaller
 * batch and never overflows.
 *
 * <p>When every result fits the final prompt, there is one round: the results go in place of their call
 * lines and the crew's own synthesis prompt writes the answer. When they do not, each round before the
 * last reads the next batch of results into working notes (the notes and the batch are the prompt;
 * see {@link RefinePrompts}), and the last round writes the answer from the question, the analysts'
 * contributions and the notes. A result larger than a round's room is read in consecutive parts. The
 * last allowed round (the cap, or the time limit) takes what fits and names every other result as
 * unknown. If the picked model has no known window, there is one round and each result is cut at a
 * fixed number of characters, as before.
 */
final class EvidenceSynthesis {

    private static final Logger log = LoggerFactory.getLogger(EvidenceSynthesis.class);

    /** One no-tool model call whose prompt is built after the model is picked. */
    interface Caller {
        ChatService.SimpleLlmResult call(String pickSystem, String pickUser,
                                         Function<PromptWindow, SimplePrompt> promptFor);
    }

    /**
     * The round cap, the characters a result is cut at when no window is known, the time check, and
     * the milliseconds of synthesis budget left (a notes round starts only when its predicted cost fits).
     */
    record Settings(int maxRounds, int fallbackChars, BooleanSupplier timeLeft, LongSupplier remainingMillis) {
        Settings(int maxRounds, int fallbackChars, BooleanSupplier timeLeft) {
            this(maxRounds, fallbackChars, timeLeft, () -> Long.MAX_VALUE);
        }
    }

    /** A notes round writes about one token for this many tokens of the results it reads, and never fewer than the minimum. */
    static final int NOTES_TOKENS_PER_BATCH_TOKENS = 5;
    static final int MIN_NOTES_TOKENS = 512;

    /** The call that wrote the answer, the prompt it was given, and what was done with the results. */
    record Outcome(ChatService.SimpleLlmResult result, String conversation, EvidenceReport report,
                   long notesInputTokens) {}

    private final Caller caller;
    private final ToLongFunction<String> tokens;
    private final Settings settings;
    private final String system;

    EvidenceSynthesis(Caller caller, ToLongFunction<String> tokens, Settings settings, String system) {
        this.caller = caller;
        this.tokens = tokens;
        this.settings = settings;
        this.system = system;
    }

    /**
     * Runs the rounds. {@code conversation} is the synthesis input with the manifests' call lines in
     * it (the header and closing lines already removed); {@code results} are the stored results those
     * lines name, in order.
     */
    Outcome run(String conversation, List<StoredResult> results) {
        var report = new EvidenceReport(results.size());
        var queue = new EvidenceQueue(results, tokens);
        var predictor = new CostPredictor();
        String notes = null;
        long notesTokensIn = 0;
        for (int number = 1; ; number++) {
            var round = new Round(number, conversation, notes, queue, results, report, predictor);
            long started = System.nanoTime();
            var result = caller.call(system, round.pickPrompt(), round);
            long elapsedMs = (System.nanoTime() - started) / 1_000_000;
            report.roundTimed(elapsedMs);
            predictor.observe(result.inputTokens(), result.outputTokens(), elapsedMs);
            if (round.last) {
                return new Outcome(result, round.finalConversation, report, notesTokensIn);
            }
            notesTokensIn += result.inputTokens();
            if (result.text() != null && !result.text().isBlank()) {
                queue.commit(round.batch);
                round.reportBatch();
                notes = result.text().strip();
            } else {
                log.warn("synthesis evidence: round {} returned no notes; its batch of {} results is read again",
                        number, round.batch.size());
            }
        }
    }

    /** One round: decides from the window its call got whether it reads a batch or writes the answer. */
    private final class Round implements Function<PromptWindow, SimplePrompt> {
        private final int number;
        private final String conversation;
        private final String notes;
        private final EvidenceQueue queue;
        private final List<StoredResult> results;
        private final EvidenceReport report;
        private final CostPredictor predictor;
        private EvidenceQueue.Batch batch;
        private long windowRoom = -1;
        boolean last;
        String finalConversation;

        Round(int number, String conversation, String notes, EvidenceQueue queue, List<StoredResult> results,
              EvidenceReport report, CostPredictor predictor) {
            this.predictor = predictor;
            this.number = number;
            this.conversation = conversation;
            this.notes = notes;
            this.queue = queue;
            this.results = results;
            this.report = report;
        }

        /** The text the scheduler places the call by: the input as it is before any result goes in. */
        String pickPrompt() {
            return withNotes(conversation, notes);
        }

        @Override
        public SimplePrompt apply(PromptWindow window) {
            String base = pickPrompt();
            OptionalLong room = window.roomAfter(system, base);
            windowRoom = room.orElse(-1);
            report.roundStarted(window.tokens().orElse(EvidenceReport.UNKNOWN), room.orElse(EvidenceReport.UNKNOWN));
            if (room.isEmpty()) {
                return answer(queue.cut(settings.fallbackChars()));
            }
            var fill = queue.fill(room.getAsLong());
            if (!isLastRound() && fill.size() < queue.size()) {
                SimplePrompt reading = readingRound(window);
                if (reading != null) {
                    return reading;
                }
            }
            return answer(fill);
        }

        private boolean isLastRound() {
            return number >= settings.maxRounds() || !settings.timeLeft().getAsBoolean();
        }

        /** A notes round over the next batch, or null when not one result fits the round. */
        private SimplePrompt readingRound(PromptWindow window) {
            String fixed = RefinePrompts.round(number, conversation, notes, "");
            OptionalLong room = window.roomAfter(RefinePrompts.system(), fixed);
            batch = queue.batch(room.orElse(0));
            if (batch.isEmpty()) {
                return null;
            }
            String text = String.join("\n\n", batch.slices().stream().map(s -> s.render(true)).toList());
            String prompt = RefinePrompts.round(number, conversation, notes, text);
            int cap = notesCap(tokens.applyAsLong(text));
            long remaining = settings.remainingMillis().getAsLong();
            long promptTokens = tokens.applyAsLong(prompt);
            if (!predictor.fits(promptTokens, cap, remaining)) {
                log.info("synthesis evidence: round {} would take about {} ms and {} ms are left; the results not read are named unknown",
                        number, predictor.predictedMillis(promptTokens, cap), remaining);
                return null;
            }
            return new SimplePrompt(RefinePrompts.system(), prompt, cap);
        }

        void reportBatch() {
            for (var s : batch.slices()) {
                report.delivered(s.text().getBytes(StandardCharsets.UTF_8).length, s.isPart());
            }
        }

        /** The last round: the results in {@code fill} go in place, the rest are named as unknown. */
        private SimplePrompt answer(EvidenceQueue.Batch fill) {
            last = true;
            batch = fill;
            queue.commit(fill);
            reportBatch();
            Map<String, String> byKey = new HashMap<>();
            for (var s : fill.slices()) {
                byKey.put(s.key(), s.render(false));
            }
            var undelivered = new ArrayList<String>();
            for (StoredResult r : results) {
                byKey.computeIfAbsent(r.key(), key -> absent(r, undelivered));
            }
            report.finish((int) results.stream().filter(r -> r.readable() && queue.isRead(r.key())).count(),
                    undelivered);
            finalConversation = withNotes(replaceCallLines(conversation, byKey), notes);
            long finalRoom = windowRoom;
            if (finalRoom == 0) {
                log.warn("synthesis evidence: round {} leaves no room in its window after the question and notes", number);
            }
            return new SimplePrompt(system, finalConversation);
        }

        /** What stands in place of a result the last prompt does not hold whole. */
        private String absent(StoredResult r, List<String> undelivered) {
            if (r.readable() && queue.isRead(r.key())) {
                return "[" + r.tool() + "] read in an earlier round; its findings are in the working notes below.";
            }
            undelivered.add(r.key());
            String unknown = DiscussionOrchestrator.artifactUnavailableNotice(r.key());
            return r.readable() && queue.partlyRead(r.key())
                    ? "[" + r.tool() + "] its first part was read; its findings so far are in the working notes below.\n"
                            + unknown
                    : unknown;
        }
    }

    /** The most tokens a notes round may write for a batch of {@code batchTokens}: a fraction of the batch, not a flat limit. */
    static int notesCap(long batchTokens) {
        return (int) Math.min(Integer.MAX_VALUE, Math.max(MIN_NOTES_TOKENS, batchTokens / NOTES_TOKENS_PER_BATCH_TOKENS));
    }

    /**
     * Predicts what the next call costs from the calls already made in this synthesis: the time per
     * token (read and written) they took. With no call made yet there is nothing to predict from and
     * the round is allowed. The answer call that must follow a notes round is predicted as the mean
     * of the calls so far.
     */
    static final class CostPredictor {
        private long millis;
        private long tokenCount;
        private int calls;

        void observe(long inputTokens, long outputTokens, long elapsedMillis) {
            millis += elapsedMillis;
            tokenCount += inputTokens + outputTokens;
            calls++;
        }

        long predictedMillis(long inputTokens, long outputTokens) {
            return tokenCount == 0 ? 0 : (inputTokens + outputTokens) * millis / tokenCount;
        }

        /** True when a notes round of these sizes and the answer call after it fit in {@code remainingMillis}. */
        boolean fits(long inputTokens, long outputTokens, long remainingMillis) {
            if (calls == 0) {
                return true;
            }
            return predictedMillis(inputTokens, outputTokens) + millis / calls <= remainingMillis;
        }
    }

    private static String withNotes(String conversation, String notes) {
        return notes == null ? conversation : conversation + "\n\n" + RefinePrompts.finalNotes(notes);
    }

    /** {@code text} with each call line replaced by its entry in {@code byKey}; a line not in the map stays. */
    static String replaceCallLines(String text, Map<String, String> byKey) {
        Matcher m = CallManifest.CALL_LINE.matcher(text);
        var sb = new StringBuilder();
        while (m.find()) {
            m.appendReplacement(sb, Matcher.quoteReplacement(byKey.getOrDefault(m.group(1), m.group())));
        }
        m.appendTail(sb);
        return sb.toString();
    }
}
