package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.util.LinkedHashMap;
import java.util.List;
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
 * nothing to answer from and would invent. When a successful result was stored, the coordinator
 * reads it first ({@link StoredResults}); when none can be read it answers without the model: what
 * was asked and that it could not be checked, in the asker's terms.
 */
final class DiscussionFacts {

    /** One message of the thread: who sent it, its type and its text. */
    record Message(String agent, String type, String content) {}

    /** The message types whose text can hold facts; the others (advisory, stand_aside, failure, the coordinator's own) cannot. */
    private static final Set<String> STATEMENT_TYPES = Set.of("agree", "contribution", "proposal", "concern", "block");

    /** Words that are protocol and say nothing about the world. */
    private static final Pattern PROTOCOL_WORDS = Pattern.compile("\\b(?:NOTHING_TO_ADD|NO_DATA)\\b");

    private static final int QUESTION_CHARS = 500;

    /** A call line or a not-stored line, in the order they stand. */
    private static final Pattern ITEM = Pattern.compile(
            "\\[CALL key=([^\\]\\s]++) tool=([^\\]\\s]++)([^\\]]*+)\\]"
                    + "|\\[NOT STORED tool=([^\\]\\s]++)([^\\]]*+)\\]");

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

    /** A successful tool call whose result was stored: who made it, with what tool, and the thread-relative key. */
    record StoredCall(String agent, String tool, String key) {}

    /**
     * The successful, stored calls the thread describes, in the order they stand and without repeats.
     * When the thread holds no fact these are results nobody stated and that were too large to post.
     */
    static List<StoredCall> storedSuccesses(List<Message> thread) {
        var found = new LinkedHashMap<String, StoredCall>();
        statements(thread).forEach(m -> {
            Matcher item = ITEM.matcher(m.content());
            while (item.find()) {
                if (item.group(2) != null && !item.group(3).contains("error=true")) {
                    found.putIfAbsent(item.group(1), new StoredCall(m.agent(), item.group(2), item.group(1)));
                }
            }
        });
        return List.copyOf(found.values());
    }

    /**
     * The answer for a thread that holds no facts and has no stored result to read: what was asked
     * and that it could not be checked. It speaks to the asker, so it names no agent, tool, key or
     * count of calls. A failed lookup is never reported as an empty result.
     */
    static String noDataAnswer(String question, boolean anyResultStored) {
        var out = new StringBuilder("I could not get the data needed to answer this question, so I have no answer to give.\n\n");
        out.append("You asked: ").append(question == null || question.isBlank() ? "(not recorded)"
                : cut(oneLine(question), QUESTION_CHARS)).append("\n\n");
        return out.append(anyResultStored
                ? "Some lookups returned results, but none of them could be read here, so nothing from them is reported."
                : "Every lookup this needed failed, so nothing could be checked.")
                .append(" A lookup that failed says nothing about what exists, so no item is reported as missing or as zero.")
                .toString();
    }

    private static String oneLine(String text) {
        return text == null ? "" : text.replaceAll("\\s+", " ").strip();
    }

    private static String cut(String text, int max) {
        return text.length() <= max ? text : text.substring(0, max) + "...";
    }
}
