package ai.kubemoot.agent.chat;

import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;
import io.micrometer.core.instrument.DistributionSummary;
import io.micrometer.core.instrument.Metrics;

import java.util.List;

/**
 * Where one model call's prompt tokens went: the system prompt, the tool definitions, the
 * user messages (the thread an agent is shown), the model's own earlier turns, and the tool
 * results. Components are estimated with the same density rules the window checks use; the
 * engine reports only a total, so that total is logged beside them. One log line and one
 * metric per call make a window overflow diagnosable without reconstructing the prompt.
 */
record PromptReport(long system, long tools, long thread, long assistant, long toolResults) {

    /** Metric name; the {@code component} tag is one of the values of {@link #COMPONENTS}. */
    static final String METRIC = "kubemoot_agent_prompt_tokens";

    private static final String DESCRIPTION = "Prompt tokens of one model call, by component (estimated), "
            + "and the total the engine reported (component reported)";

    static final List<String> COMPONENTS =
            List.of("system", "tools", "thread", "assistant", "tool_results", "reported");

    static PromptReport of(List<ChatMessage> messages, List<ToolSpecification> specs, CharsPerToken ratio) {
        long system = 0;
        long thread = 0;
        long assistant = 0;
        long results = 0;
        for (ChatMessage m : messages) {
            long tokens = tokens(m, ratio);
            if (m instanceof SystemMessage) {
                system += tokens;
            } else if (m instanceof UserMessage) {
                thread += tokens;
            } else if (m instanceof AiMessage) {
                assistant += tokens;
            } else if (m instanceof ToolExecutionResultMessage) {
                results += tokens;
            }
        }
        return new PromptReport(system, ratio.tokens(PromptSize.chars(List.of(), specs)), thread, assistant, results);
    }

    private static long tokens(ChatMessage m, CharsPerToken ratio) {
        long extra = PromptSize.densityExtra(List.of(m));
        return ratio.tokens(PromptSize.chars(m) + extra) + PromptSize.MESSAGE_OVERHEAD_TOKENS;
    }

    long total() {
        return system + tools + thread + assistant + toolResults;
    }

    /** The log line for one call; {@code reported} is the engine's prompt size, or zero when it gave none. */
    String line(String agent, int call, long window, long reported) {
        return "PROMPT-TOKENS agent=" + agent + " call=" + call + " window=" + (window > 0 ? window : "unknown")
                + " reported=" + reported + " estimated=" + total() + " system=" + system + " tools=" + tools
                + " thread=" + thread + " assistant=" + assistant + " toolResults=" + toolResults;
    }

    /** Record each component, and the engine's total, on the global registry (a no-op without one). */
    void record(long reported) {
        long[] values = {system, tools, thread, assistant, toolResults, reported};
        for (int i = 0; i < values.length; i++) {
            if (values[i] > 0) {
                DistributionSummary.builder(METRIC).description(DESCRIPTION).tag("component", COMPONENTS.get(i))
                        .register(Metrics.globalRegistry).record(values[i]);
            }
        }
    }
}
