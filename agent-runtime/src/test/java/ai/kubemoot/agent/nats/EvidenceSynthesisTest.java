package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.PromptWindow;
import ai.kubemoot.agent.chat.SimplePrompt;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;
import java.util.function.Function;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import java.util.stream.IntStream;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * The rounds that get a thread's stored results to the model that writes the answer. A fake model
 * stands in for the scheduler: each call is given the next window of a list, and the prompt it is
 * sent is checked against that window.
 */
class EvidenceSynthesisTest {

    private static final String SYSTEM = "SYNTHESIS SYSTEM";
    private static final String QUESTION = "Thread question: which DaemonSets run?\nanalyst agree: counted by the tool\n";
    private static final int FALLBACK = 24_000;

    /** Four characters to a token, flat. */
    private static long estimate(String s) {
        return (s.length() + 3) / 4;
    }

    /** The reply reserve the fake window keeps free: an eighth of it. */
    private static long reserve(long window) {
        return window / 8;
    }

    private static String key(int i) {
        return "ns/crew/t1/k8s/00" + i + "-tool" + i;
    }

    private static String callLine(int i) {
        return "[CALL key=" + key(i) + " tool=tool" + i + " format=text bytes=1 rows=unknown error=false]";
    }

    /** {@code n} call lines in a conversation, as the orchestrator hands it over (header and footer removed). */
    private static String conversation(int n) {
        return QUESTION + IntStream.rangeClosed(1, n).mapToObj(EvidenceSynthesisTest::callLine)
                .collect(Collectors.joining("\n"));
    }

    /** A result of about {@code tokens} tokens: a unique first line, then lines of 100 characters. */
    private static String content(int i, long tokens) {
        var sb = new StringBuilder("UNIQUE-" + i + "\n");
        while (sb.length() < tokens * 4) {
            sb.append(("line " + i + " ").repeat(16).substring(0, 99)).append('\n');
        }
        return sb.toString();
    }

    private static List<StoredResult> results(int n, long tokensEach) {
        return IntStream.rangeClosed(1, n)
                .mapToObj(i -> new StoredResult(key(i), "tool" + i, callLine(i), content(i, tokensEach))).toList();
    }

    /** A fake model: the window of each call comes from a list, the last one repeats. */
    private static final class FakeModel implements EvidenceSynthesis.Caller {
        final List<Long> windows;
        final List<SimplePrompt> sent = new ArrayList<>();
        final List<Long> windowOfCall = new ArrayList<>();
        final Function<SimplePrompt, String> reply;

        FakeModel(List<Long> windows, Function<SimplePrompt, String> reply) {
            this.windows = windows;
            this.reply = reply;
        }

        FakeModel(List<Long> windows) {
            this(windows, FakeModel::notes);
        }

        /** Notes that keep the earlier notes and add the keys of the batch, one short line each. */
        static String notes(SimplePrompt prompt) {
            String user = prompt.user();
            if (!user.contains("STORED RESULTS (round")) {
                return "the answer";
            }
            String before = "";
            int at = user.indexOf("WORKING NOTES SO FAR:\n");
            if (at >= 0) {
                before = user.substring(at + "WORKING NOTES SO FAR:\n".length(), user.indexOf("\n\nNEW STORED RESULTS"))
                        + "\n";
            }
            var keys = new ArrayList<String>();
            var m = Pattern.compile("\\[tool\\d+ key=(\\S+?)( part \\d+ of \\d+)?]").matcher(user);
            while (m.find()) {
                keys.add(m.group(1) + (m.group(2) == null ? "" : m.group(2)));
            }
            return before + "- [" + String.join("] [", keys) + "] noted";
        }

        @Override
        public ChatService.SimpleLlmResult call(String pickSystem, String pickUser,
                                                Function<PromptWindow, SimplePrompt> promptFor) {
            long window = windows.get(Math.min(sent.size(), windows.size() - 1));
            windowOfCall.add(window);
            var prompt = promptFor.apply(new PromptWindow() {
                @Override
                public OptionalLong tokens() {
                    return window < 0 ? OptionalLong.empty() : OptionalLong.of(window);
                }

                @Override
                public OptionalLong roomAfter(String system, String user) {
                    return window < 0 ? OptionalLong.empty()
                            : OptionalLong.of(Math.max(0, window - estimate(system) - estimate(user) - reserve(window)));
                }
            });
            sent.add(prompt);
            return new ChatService.SimpleLlmResult(reply.apply(prompt), estimate(prompt.system() + prompt.user()), 10);
        }
    }

    private static EvidenceSynthesis synthesis(FakeModel model, int maxRounds) {
        return new EvidenceSynthesis(model, EvidenceSynthesisTest::estimate,
                new EvidenceSynthesis.Settings(maxRounds, FALLBACK, () -> true), SYSTEM);
    }

    private static int count(String text, String part) {
        return text.split(Pattern.quote(part), -1).length - 1;
    }

    /** Every prompt of the run, to count where a result's text was sent. */
    private static String allSent(FakeModel model) {
        return model.sent.stream().map(SimplePrompt::user).collect(Collectors.joining("\n=====\n"));
    }

    private static void assertNoPromptPastItsWindow(FakeModel model) {
        for (int i = 0; i < model.sent.size(); i++) {
            long window = model.windowOfCall.get(i);
            long size = estimate(model.sent.get(i).system()) + estimate(model.sent.get(i).user());
            assertTrue(size + reserve(window) <= window, "call " + (i + 1) + " sent " + size + " tokens to a "
                    + window + "-token window");
        }
    }

    // --- the same evidence on windows of 8K, 32K and 128K ---

    private static final int SEVEN = 7;
    private static final long FIVE_K = 5_000;

    private record Run(FakeModel model, EvidenceSynthesis.Outcome outcome) {}

    private static Run runOnWindow(long window, int maxRounds) {
        var model = new FakeModel(List.of(window));
        var outcome = synthesis(model, maxRounds).run(conversation(SEVEN), results(SEVEN, FIVE_K));
        return new Run(model, outcome);
    }

    @Test
    void aWindowOf128KTakesEverythingInOneRound() {
        var run = runOnWindow(131_072, 10);

        assertEquals(1, run.outcome.report().rounds());
        assertEquals(SYSTEM, run.model.sent.get(0).system(), "the one call is the crew's own synthesis prompt");
        String out = run.outcome.conversation();
        for (int i = 1; i <= SEVEN; i++) {
            assertEquals(1, count(out, "UNIQUE-" + i + "\n"), "result " + i + " is in the prompt once");
            assertFalse(out.contains(callLine(i)), "its call line is replaced by the result");
            assertTrue(out.contains("[tool" + i + "]\nUNIQUE-" + i), "in place, as the merged path showed it");
        }
        assertFalse(out.contains("WORKING NOTES"), out);
        assertEquals(SEVEN, run.outcome.report().filled());
        assertTrue(run.outcome.report().notDelivered().isEmpty());
        assertNoPromptPastItsWindow(run.model);
    }

    @Test
    void aWindowOf32KTakesMoreThanOneRound() {
        var run = runOnWindow(32_768, 10);

        assertEquals(2, run.outcome.report().rounds());
        assertEquals(SYSTEM, run.model.sent.get(1).system());
        assertNotEquals(SYSTEM, run.model.sent.get(0).system(), "round 1 reads results into notes");
        assertReadOnce(run);
        assertNoPromptPastItsWindow(run.model);
    }

    @Test
    void aWindowOf8KTakesSeveralRoundsForTheSameEvidence() {
        var run = runOnWindow(8_192, 10);

        assertTrue(run.outcome.report().rounds() > 2, "rounds " + run.outcome.report().rounds());
        assertReadOnce(run);
        assertNoPromptPastItsWindow(run.model);
        assertEquals(SEVEN, run.outcome.report().filled());
    }

    @Test
    void theSmallerTheWindowTheMoreRoundsTheSameEvidenceTakes() {
        int small = runOnWindow(8_192, 10).outcome.report().rounds();
        int medium = runOnWindow(32_768, 10).outcome.report().rounds();
        int large = runOnWindow(131_072, 10).outcome.report().rounds();

        assertTrue(small > medium && medium > large, small + " > " + medium + " > " + large);
    }

    /** Each result's text went to the model in exactly one prompt, and the notes reached the final prompt. */
    private static void assertReadOnce(Run run) {
        String sent = allSent(run.model);
        for (int i = 1; i <= SEVEN; i++) {
            assertEquals(1, count(sent, "UNIQUE-" + i + "\n"), "result " + i + " was sent once across the rounds");
        }
        assertEquals(SEVEN, run.outcome.report().filled());
        assertTrue(run.outcome.conversation().contains("WORKING NOTES FROM THE STORED TOOL RESULTS"),
                run.outcome.conversation());
    }

    @Test
    void everyStoredResultIsReadFromTheStoreOnce() {
        var reads = new HashMap<String, Integer>();
        var list = StoredResult.listFrom(conversation(SEVEN), k -> {
            reads.merge(k, 1, Integer::sum);
            return content(Integer.parseInt(k.substring(k.indexOf("/00") + 3, k.indexOf("-tool"))), FIVE_K);
        });
        var model = new FakeModel(List.of(8_192L));

        synthesis(model, 10).run(conversation(SEVEN), list);

        assertEquals(SEVEN, reads.size());
        reads.forEach((k, n) -> assertEquals(1, n, k));
    }

    // --- notes carried forward ---

    @Test
    void theNotesOfOneRoundAreInTheNextRoundsPrompt() {
        var run = runOnWindow(8_192, 10);

        String round2 = run.model.sent.get(1).user();
        assertTrue(round2.contains("WORKING NOTES SO FAR:\n- [" + key(1) + "]"), round2);
        String finalPrompt = run.outcome.conversation();
        for (int i = 1; i <= SEVEN; i++) {
            assertTrue(finalPrompt.contains(key(i) + "]") || finalPrompt.contains(key(i) + " part")
                    || finalPrompt.contains("[tool" + i + "]"), "result " + i + " is accounted for in the final prompt");
        }
        assertTrue(run.model.sent.get(0).user().contains("QUESTION AND CONTRIBUTIONS:\n" + QUESTION.strip()));
        assertFalse(run.model.sent.get(0).user().contains("WORKING NOTES SO FAR"), "round 1 has no notes yet");
    }

    @Test
    void aBlankReplyKeepsTheEarlierNotesAndConsumesNothing() {
        var calls = new int[1];
        var model = new FakeModel(List.of(8_192L), prompt -> {
            if (!prompt.user().contains("STORED RESULTS (round")) {
                return "answer";
            }
            return ++calls[0] == 1 ? "  " : FakeModel.notes(prompt);
        });

        var outcome = synthesis(model, 20).run(conversation(SEVEN), results(SEVEN, FIVE_K));

        assertEquals(model.sent.get(0).user().substring(model.sent.get(0).user().indexOf("STORED RESULTS")),
                model.sent.get(1).user().substring(model.sent.get(1).user().indexOf("STORED RESULTS")).replace("round 2", "round 1"),
                "the batch of a round that gave no notes is read again");
        assertEquals(SEVEN, outcome.report().filled());
    }

    // --- a window that changes between rounds ---

    @Test
    void aLaterRoundWithASmallerWindowRebatchesAndNeverOverflows() {
        var model = new FakeModel(List.of(32_768L, 8_192L, 8_192L, 8_192L, 8_192L, 8_192L, 8_192L, 8_192L));

        var outcome = synthesis(model, 10).run(conversation(SEVEN), results(SEVEN, FIVE_K));

        int firstBatch = count(model.sent.get(0).user(), "UNIQUE-");
        int secondBatch = count(model.sent.get(1).user(), "UNIQUE-");
        assertTrue(firstBatch >= 4, "round 1 used the 32K window: " + firstBatch);
        assertEquals(1, secondBatch, "round 2 got the 8K window and took fewer");
        assertNoPromptPastItsWindow(model);
        for (int i = 1; i <= SEVEN; i++) {
            assertEquals(1, count(allSent(model), "UNIQUE-" + i + "\n"), "result " + i);
        }
        assertEquals(SEVEN, outcome.report().filled());
        assertEquals(List.of(32_768L, 8_192L, 8_192L), outcome.report().windows().subList(0, 3));
    }

    @Test
    void aLaterRoundWithALargerWindowTakesMore() {
        var model = new FakeModel(List.of(8_192L, 131_072L));

        var outcome = synthesis(model, 10).run(conversation(SEVEN), results(SEVEN, FIVE_K));

        assertEquals(2, outcome.report().rounds(), "round 2 had the room for the six that were left");
        assertEquals(1, count(model.sent.get(0).user(), "UNIQUE-"));
        assertEquals(SEVEN, outcome.report().filled());
    }

    // --- the fill never stops early ---

    @Test
    void theFillContinuesPastAResultThatDoesNotFit() {
        var list = new ArrayList<>(results(3, 200));
        list.set(0, new StoredResult(key(1), "tool1", callLine(1), content(1, 50_000)));
        var model = new FakeModel(List.of(8_192L));

        var outcome = synthesis(model, 1).run(conversation(3), list);

        String out = outcome.conversation();
        assertEquals(1, outcome.report().rounds());
        assertTrue(out.contains("[tool2]\nUNIQUE-2") && out.contains("[tool3]\nUNIQUE-3"),
                "the results after the oversize one are filled: " + out);
        assertFalse(out.contains("UNIQUE-1"), "no part of the result that did not fit");
        assertTrue(out.contains(DiscussionOrchestrator.artifactUnavailableNotice(key(1))), out);
        assertEquals(List.of(key(1)), outcome.report().notDelivered());
    }

    @Test
    void aResultNotDeliveredNeverClaimsTheAnalystsSummarizedIt() {
        var list = new ArrayList<>(results(2, 200));
        list.set(0, new StoredResult(key(1), "tool1", callLine(1), content(1, 50_000)));
        var model = new FakeModel(List.of(8_192L));

        var out = synthesis(model, 1).run(conversation(2), list).conversation();

        assertFalse(out.contains("summarize"), out);
        assertTrue(out.contains("Treat it as UNKNOWN") && out.contains("do NOT infer, guess, or fabricate"), out);
    }

    @Test
    void anUnreadableResultIsTheUnavailableNoticeAndIsNotQueued() {
        var list = new ArrayList<>(results(2, 200));
        list.set(0, new StoredResult(key(1), "tool1", callLine(1), null));
        var model = new FakeModel(List.of(131_072L));

        var outcome = synthesis(model, 4).run(conversation(2), list);

        assertTrue(outcome.conversation().contains(DiscussionOrchestrator.artifactUnavailableNotice(key(1))));
        assertTrue(outcome.conversation().contains("[tool2]\nUNIQUE-2"));
        assertEquals(1, outcome.report().rounds());
        assertEquals(List.of(key(1)), outcome.report().notDelivered());
        assertEquals(1, outcome.report().filled());
    }

    // --- a result larger than one round's room ---

    @Test
    void aSingleOversizeResultIsReadInPartsAtLineBoundaries() {
        var big = content(1, 20_000);
        var model = new FakeModel(List.of(8_192L));

        var outcome = synthesis(model, 20).run(conversation(1),
                List.of(new StoredResult(key(1), "tool1", callLine(1), big)));

        var delivered = new StringBuilder();
        var labels = new ArrayList<String>();
        for (SimplePrompt p : model.sent) {
            var m = Pattern.compile("\\[tool1(?: key=\\S+)? part (\\d+) of (\\d+)]\\n(.*?)(?=\\n\\n\\[tool1 key=|\\n\\nWHEN you reply|\\n\\nWORKING NOTES|\\z)",
                    Pattern.DOTALL).matcher(p.user().substring(Math.max(0, p.user().indexOf("STORED RESULTS"))));
            while (m.find()) {
                labels.add(m.group(1) + "/" + m.group(2));
                delivered.append(m.group(3).stripTrailing().concat("\n"));
            }
        }
        assertTrue(labels.size() >= 3, "split into parts: " + labels);
        assertEquals("1", labels.get(0).substring(0, 1));
        assertEquals(big.stripTrailing() + "\n", delivered.toString(), "the parts add up to the result, none cut or repeated");
        assertEquals(labels.size(), outcome.report().chunks(), "every part is counted");
        assertNoPromptPastItsWindow(model);
        assertEquals(1, outcome.report().filled());
        assertTrue(outcome.report().notDelivered().isEmpty());
    }

    @Test
    void aSingleLongLineIsCutAtTheCharacterWhenNoLineBreakFits() {
        String oneLine = "x".repeat(60_000);
        var model = new FakeModel(List.of(8_192L));

        var outcome = synthesis(model, 20).run(conversation(1),
                List.of(new StoredResult(key(1), "tool1", callLine(1), oneLine)));

        int total = model.sent.stream().mapToInt(p -> count(p.user(), "x")).sum() - count(RefinePrompts.system(), "x");
        assertTrue(total >= 60_000, "all characters were sent: " + total);
        assertNoPromptPastItsWindow(model);
        assertEquals(1, outcome.report().filled());
    }

    // --- the cap ---

    @Test
    void whenTheRoundCapIsReachedTheRestAreNamedAsUnknown() {
        var run = runOnWindow(8_192, 3);

        var report = run.outcome.report();
        assertEquals(3, report.rounds());
        assertFalse(report.notDelivered().isEmpty());
        String out = run.outcome.conversation();
        for (String key : report.notDelivered()) {
            assertTrue(out.contains(DiscussionOrchestrator.artifactUnavailableNotice(key)), key);
        }
        assertEquals(SEVEN, report.filled() + report.notDelivered().size(), "every result is delivered or named");
        assertEquals(SYSTEM, run.model.sent.get(2).system(), "the last allowed round writes the answer");
        assertNoPromptPastItsWindow(run.model);
    }

    @Test
    void theTimeLimitEndsTheReadingLikeTheCap() {
        var model = new FakeModel(List.of(8_192L));
        int[] checks = {0};
        var synthesis = new EvidenceSynthesis(model, EvidenceSynthesisTest::estimate,
                new EvidenceSynthesis.Settings(10, FALLBACK, () -> ++checks[0] < 2), SYSTEM);

        var outcome = synthesis.run(conversation(SEVEN), results(SEVEN, FIVE_K));

        assertEquals(2, outcome.report().rounds(), "one reading round, then the time ran out");
        assertFalse(outcome.report().notDelivered().isEmpty());
    }

    @Test
    void aWindowTooSmallForAnyResultEndsWithTheResultsNamedUnknown() {
        var model = new FakeModel(List.of(400L));

        var outcome = synthesis(model, 10).run(conversation(2), results(2, 500));

        assertEquals(1, outcome.report().rounds());
        assertEquals(List.of(key(1), key(2)), outcome.report().notDelivered());
    }

    // --- no window known ---

    @Test
    void anUnknownWindowIsOneRoundWithTheFixedCharacterCut() {
        var model = new FakeModel(List.of(-1L));
        var list = new ArrayList<>(results(2, 200));
        list.set(1, new StoredResult(key(2), "tool2", callLine(2), "y".repeat(FALLBACK + 500)));

        var outcome = synthesis(model, 4).run(conversation(2), list);

        String out = outcome.conversation();
        assertEquals(1, outcome.report().rounds());
        assertEquals(SYSTEM, model.sent.get(0).system());
        assertTrue(out.contains("[tool1]\nUNIQUE-1"), out);
        assertTrue(out.contains("[tool2]\n" + "y".repeat(FALLBACK) + ManifestInliner.CUT_NOTE), out);
        assertFalse(out.contains("y".repeat(FALLBACK + 1)));
        assertFalse(out.contains("WORKING NOTES"));
        assertEquals(List.of(-1L), outcome.report().windows());
    }

    // --- the report ---

    @Test
    void theReportCountsWindowsRoomsBytesAndChunks() {
        var run = runOnWindow(8_192, 10);
        var report = run.outcome.report();

        assertEquals(report.rounds(), report.windows().size());
        assertEquals(report.rounds(), report.rooms().size());
        assertTrue(report.rooms().stream().allMatch(r -> r > 0 && r < 8_192));
        long expectedBytes = results(SEVEN, FIVE_K).stream().mapToLong(r -> r.content().length()).sum();
        assertEquals(expectedBytes, report.filledBytes());
        String line = report.logLine("t1");
        for (String part : new String[] {"thread=t1", "windows=[8192", "rooms=[", "results=7", "filled=7",
                "rounds=" + report.rounds(), "chunks=" + report.chunks(), "bytes=" + expectedBytes, "notDelivered=[]"}) {
            assertTrue(line.contains(part), part + " in " + line);
        }
    }

    @Test
    void anUnknownWindowShowsAsUnknownInTheLogLine() {
        var model = new FakeModel(List.of(-1L));
        var outcome = synthesis(model, 4).run(conversation(1), results(1, 100));

        assertTrue(outcome.report().logLine("t").contains("windows=[unknown] rooms=[unknown]"));
    }

    @Test
    void callLinesNotInTheMapStayAsTheyAre() {
        String out = EvidenceSynthesis.replaceCallLines("a " + callLine(1) + " b " + callLine(2), Map.of(key(1), "RESULT $1"));

        assertEquals("a RESULT $1 b " + callLine(2), out);
    }

    // --- time: notes sized to the batch, rounds that must fit the budget left ---

    @Test
    void theNotesAReadingRoundMayWriteAreAFractionOfItsBatchNotAFlatLimit() {
        assertEquals(EvidenceSynthesis.MIN_NOTES_TOKENS, EvidenceSynthesis.notesCap(100));
        assertEquals(2_000, EvidenceSynthesis.notesCap(10_000));
        assertEquals(20_000, EvidenceSynthesis.notesCap(100_000));
    }

    @Test
    void aNotesRoundIsCappedByItsBatchAndTheAnswerIsNot() {
        var run = runOnWindow(8_192, 10);

        assertTrue(run.model.sent.size() > 2);
        for (int i = 0; i < run.model.sent.size() - 1; i++) {
            int cap = run.model.sent.get(i).maxOutputTokens();
            assertTrue(cap >= EvidenceSynthesis.MIN_NOTES_TOKENS, "round " + (i + 1) + " cap " + cap);
            assertTrue(cap < 8_192, "round " + (i + 1) + " cap " + cap + " is below the window");
        }
        assertEquals(0, run.model.sent.get(run.model.sent.size() - 1).maxOutputTokens(), "the answer keeps the model's limit");
    }

    @Test
    void aRoundWhosePredictedCostDoesNotFitTheBudgetLeftIsNotStartedAndTheRestIsNamedUnknown() {
        var model = new FakeModel(List.of(8_192L), prompt -> {
            sleep(30);
            return FakeModel.notes(prompt);
        });
        var settings = new EvidenceSynthesis.Settings(10, FALLBACK, () -> true, () -> 10);

        var outcome = new EvidenceSynthesis(model, EvidenceSynthesisTest::estimate, settings, SYSTEM)
                .run(conversation(SEVEN), results(SEVEN, FIVE_K));

        assertEquals(2, outcome.report().rounds(), "one notes round, then the answer");
        assertFalse(outcome.report().notDelivered().isEmpty());
        assertTrue(outcome.conversation().contains("Treat it as UNKNOWN"));
        assertEquals(2, outcome.report().roundMillis().size());
    }

    @Test
    void withNoCallMadeYetThereIsNothingToPredictFromAndTheRoundStarts() {
        var predictor = new EvidenceSynthesis.CostPredictor();

        assertTrue(predictor.fits(10_000, 2_000, 1));
    }

    @Test
    void thePredictedCostIsTheTimePerTokenOfTheCallsSoFarPlusOneMoreAnswer() {
        var predictor = new EvidenceSynthesis.CostPredictor();
        predictor.observe(800, 200, 1_000);

        assertEquals(500, predictor.predictedMillis(250, 250));
        assertTrue(predictor.fits(250, 250, 1_500));
        assertFalse(predictor.fits(250, 250, 1_499));
    }

    @Test
    void eachRoundIsTimedInTheReport() {
        var run = runOnWindow(8_192, 10);

        assertEquals(run.outcome.report().rounds(), run.outcome.report().roundMillis().size());
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
