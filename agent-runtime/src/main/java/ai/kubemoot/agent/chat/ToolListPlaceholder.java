package ai.kubemoot.agent.chat;

import java.util.Collection;
import java.util.function.Supplier;
import java.util.stream.Collectors;

/**
 * The {@code ${agent.tools}} placeholder a PromptModule uses where it lists the agent's tools.
 *
 * The runtime replaces it, when the system prompt is built for a call, with the names of the
 * tools this agent registered (sorted, comma separated), or {@code none}. Descriptions are not
 * repeated: each registered tool already reaches the model with its description in the tool
 * definitions of the same request. A prompt without the placeholder is returned unchanged, and
 * its tools are not looked up.
 */
public final class ToolListPlaceholder {

    static final String TOKEN = "${agent.tools}";
    static final String NO_TOOLS = "none";

    private ToolListPlaceholder() {
    }

    /** The prompt with the placeholder replaced by the names the supplier gives at this moment. */
    public static String render(String prompt, Supplier<? extends Collection<String>> toolNames) {
        if (prompt == null || !prompt.contains(TOKEN)) {
            return prompt;
        }
        var names = toolNames.get().stream().distinct().sorted().collect(Collectors.joining(", "));
        return prompt.replace(TOKEN, names.isEmpty() ? NO_TOOLS : names);
    }
}
