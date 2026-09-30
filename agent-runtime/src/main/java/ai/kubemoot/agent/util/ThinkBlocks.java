package ai.kubemoot.agent.util;

import java.util.regex.Pattern;

/** A reasoning model's {@code <think>...</think>} blocks, which are never part of its answer. */
public final class ThinkBlocks {

    private static final Pattern THINK_BLOCK = Pattern.compile("(?is)<think>.*?</think>");

    private ThinkBlocks() {}

    /** {@code text} with each reasoning block replaced by a space; null is empty. */
    public static String remove(String text) {
        return THINK_BLOCK.matcher(text == null ? "" : text).replaceAll(" ");
    }
}
