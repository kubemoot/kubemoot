package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Whether a discussion holds any facts, and the answer given when tools ran and it holds none.
 *
 * <p>A fact is text an agent stated or a tool result an agent posted whole. The descriptions of
 * stored files ({@link CallManifest}), the error text of a failed call, the protocol words and
 * the signals that carry no text (stand-aside, failure, an empty agree) are not facts. When tools
 * ran (some contribution describes files) and nothing else was posted, the synthesizer would have
 * nothing to answer from and would invent; the coordinator answers without the model instead:
 * what was asked, which tools ran, which failed and why, and that no data was obtained.
 */
final class DiscussionFacts {

    /** One message of the thread: who sent it, its type and its text. */
    record Message(String agent, String type, String content) {}

    /** The message types whose text can hold facts; the others (advisory, stand_aside, failure, the coordinator's own) cannot. */
    private static final Set<String> STATEMENT_TYPES = Set.of("agree", "contribution", "proposal", "concern", "block");

    /** Words that are protocol and say nothing about the world. */
    private static final Pattern PROTOCOL_WORDS = Pattern.compile("\\b(?:NOTHING_TO_ADD|NO_DATA)\\b");

    private static final int FAILURE_TEXT_CHARS = 300;
    private static final int QUESTION_CHARS = 500;

    /** A call line, a not-stored line, or the error text block of a failed call, in the order they stand. */
    private static final Pattern ITEM = Pattern.compile(
            "\\[CALL key=([^\\]\\s]++) tool=([^\\]\\s]++)([^\\]]*+)\\]"
                    + "|\\[NOT STORED tool=([^\\]\\s]++)([^\\]]*+)\\]"
                    + "|" + Pattern.quote(CallManifest.FAILED_RESULT_OPEN) + "\\n(.*?)\\n"
                    + Pattern.quote(CallManifest.RESULT_CLOSE),
            Pattern.DOTALL);

    private DiscussionFacts() {}

    /** True when some agent's contribution describes the files of tool calls (they ran, stored or not). */
    static boolean toolsRan(List<Message> thread) {
        return statements(thread).anyMatch(m -> m.content().contains(CallManifest.CALL_PREFIX)
                || m.content().contains(CallManifest.NOT_STORED_PREFIX));
    }

    /** True when any contribution holds text beyond file descriptions, failed-call text and protocol words, or a posted result. */
    static boolean holdsFacts(List<Message> thread) {
        return statements(thread).anyMatch(m -> holdsFacts(m.content()));
    }

    /** True when tools ran and the discussion holds no fact: the case answered without the model. */
    static boolean noData(List<Message> thread) {
        return toolsRan(thread) && !holdsFacts(thread);
    }

    private static java.util.stream.Stream<Message> statements(List<Message> thread) {
        return thread.stream().filter(m -> STATEMENT_TYPES.contains(m.type()) && m.content() != null);
    }

    private static boolean holdsFacts(String content) {
        Matcher result = CallManifest.RESULT_BLOCK.matcher(content);
        while (result.find()) {
            if (!result.group(1).isBlank()) {
                return true;
            }
        }
        return !ownText(content).isBlank();
    }

    /** The contribution's own words: what is left after the descriptions, result blocks and protocol words are taken out. */
    static String ownText(String content) {
        String text = ManifestText.strip(content, true);
        text = CallManifest.OMITTED_LINE.matcher(text).replaceAll("");
        return PROTOCOL_WORDS.matcher(text).replaceAll("").strip();
    }

    /**
     * The answer for a thread that holds no facts: the question, the tools that ran per agent, the
     * failures with their text, and the plain statement that no data was obtained.
     * {@code failureSignals} maps an agent that reported a failure to what it reported.
     */
    static String noDataAnswer(String question, List<Message> thread, Map<String, String> failureSignals) {
        var ran = new LinkedHashMap<String, Map<String, int[]>>();
        var failures = new ArrayList<String>();
        statements(thread).forEach(m -> collect(m, ran, failures));
        failureSignals.forEach((agent, text) -> failures.add(agent + ": " + oneLine(text)));

        var out = new StringBuilder("No data was obtained to answer this question, so there is no answer to give.\n\n");
        out.append("Question: ").append(question == null || question.isBlank() ? "(not recorded)"
                : cut(oneLine(question), QUESTION_CHARS)).append("\n\n");
        out.append("Tools that ran:\n");
        ran.forEach((agent, tools) -> out.append("- ").append(agent).append(": ").append(toolList(tools)).append('\n'));
        if (!failures.isEmpty()) {
            out.append("\nFailures:\n");
            failures.forEach(f -> out.append("- ").append(f).append('\n'));
        }
        boolean anySucceeded = ran.values().stream().flatMap(t -> t.values().stream()).anyMatch(c -> c[0] > c[1]);
        return out.append(anySucceeded
                ? "\nThe calls that succeeded returned results that no agent stated and that were too large to post, "
                        + "so no tool result reached this answer and nothing can be reported as fact."
                : "\nEvery call failed, so no tool result reached this answer and nothing can be reported as fact.")
                .toString();
    }

    /** Adds the tools and failures one contribution describes. A failed call takes the error text posted after it. */
    private static void collect(Message m, Map<String, Map<String, int[]>> ran, List<String> failures) {
        Matcher item = ITEM.matcher(m.content());
        String lastFailed = null;
        while (item.find()) {
            if (item.group(2) != null) {
                String tool = item.group(2);
                boolean failed = item.group(3).contains("error=true");
                count(ran, m.agent(), tool, failed);
                lastFailed = failed ? m.agent() + ", " + tool : null;
                if (failed) {
                    failures.add(lastFailed + ": the tool returned an error");
                }
            } else if (item.group(4) != null) {
                count(ran, m.agent(), item.group(4), item.group(5).contains("error=true"));
                lastFailed = null;
            } else if (lastFailed != null) {
                failures.set(failures.size() - 1, lastFailed + ": " + cut(oneLine(item.group(6)), FAILURE_TEXT_CHARS));
                lastFailed = null;
            }
        }
    }

    private static void count(Map<String, Map<String, int[]>> ran, String agent, String tool, boolean failed) {
        int[] counts = ran.computeIfAbsent(agent, a -> new LinkedHashMap<>()).computeIfAbsent(tool, t -> new int[2]);
        counts[0]++;
        if (failed) {
            counts[1]++;
        }
    }

    private static String toolList(Map<String, int[]> tools) {
        var parts = new ArrayList<String>();
        tools.forEach((tool, c) -> parts.add(tool + " (" + c[0] + (c[0] == 1 ? " call" : " calls")
                + (c[1] > 0 ? ", " + c[1] + " failed" : "") + ")"));
        return String.join(", ", parts);
    }

    private static String oneLine(String text) {
        return text == null ? "" : text.replaceAll("\\s+", " ").strip();
    }

    private static String cut(String text, int max) {
        return text.length() <= max ? text : text.substring(0, max) + "...";
    }
}
