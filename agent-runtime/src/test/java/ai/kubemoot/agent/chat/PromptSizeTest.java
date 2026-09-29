package ai.kubemoot.agent.chat;

import dev.langchain4j.agent.tool.ToolExecutionRequest;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/** The prompt size estimate counts everything the engine receives, including tool traffic. */
class PromptSizeTest {

    private static final String RESULT = "x".repeat(4_000);

    private static List<ChatMessage> loopMessages() {
        var call = ToolExecutionRequest.builder().id("1").name("pods_list").arguments("{\"ns\":\"all\"}").build();
        List<ChatMessage> messages = new ArrayList<>();
        messages.add(SystemMessage.from("sys"));
        messages.add(UserMessage.from("question"));
        messages.add(AiMessage.from(List.of(call)));
        messages.add(ToolExecutionResultMessage.from(call, RESULT));
        return messages;
    }

    @Test
    void chars_countsTextToolCallsToolResultsAndSpecs() {
        var spec = ToolSpecification.builder().name("pods_list").description("List pods").build();
        int expected = "sys".length() + "question".length()
                + "pods_list".length() + "{\"ns\":\"all\"}".length()
                + "pods_list".length() + RESULT.length()
                + "pods_list".length() + "List pods".length();
        assertEquals(expected, PromptSize.chars(loopMessages(), List.of(spec)));
    }

    @Test
    void chars_toleratesNullSpecsAndEmptyMessages() {
        assertEquals(0, PromptSize.chars(List.of(), null));
        assertEquals(0, PromptSize.chars(AiMessage.from("")));
    }

    @Test
    void nextPromptTokens_withoutAReportedTurn_estimatesTheWholePromptWithTheRatio() {
        var messages = loopMessages();
        var ratio = new CharsPerToken();
        assertEquals(ratio.tokens(PromptSize.chars(messages, List.of())),
                PromptSize.nextPromptTokens(messages, List.of(), 0, 0, 0, ratio));
    }

    @Test
    void nextPromptTokens_addsTheNewToolResultsToTheReportedTurn() {
        var messages = loopMessages();
        var ratio = new CharsPerToken();
        // The engine reported a 30,000-token prompt and a 50-token reply when the
        // message list ended at the AI tool call (3 messages); the tool result came after.
        long next = PromptSize.nextPromptTokens(messages, List.of(), 30_000, 50, 3, ratio);
        assertEquals(30_000 + 50 + ratio.tokens(PromptSize.chars(messages.get(3))), next);
    }

    @Test
    void nextPromptTokens_trustsTheReportedSize_overACharacterEstimate() {
        // The reported prompt counts every token, a reused prefix included, so a
        // report smaller than the character estimate is still the prompt's size.
        var messages = loopMessages();
        var ratio = new CharsPerToken();
        long next = PromptSize.nextPromptTokens(messages, List.of(), 5, 1, 3, ratio);
        assertEquals(5 + 1 + ratio.tokens(PromptSize.chars(messages.get(3))), next);
    }

    @Test
    void nextPromptTokens_ignoresAStaleMessageIndex() {
        var messages = loopMessages();
        var ratio = new CharsPerToken();
        assertEquals(ratio.tokens(PromptSize.chars(messages, List.of())),
                PromptSize.nextPromptTokens(messages, List.of(), 5, 1, 99, ratio));
    }

    @Test
    void chars_sumsEveryTextPartOfAMultiPartUserMessage() {
        var multi = UserMessage.from(List.of(
                dev.langchain4j.data.message.TextContent.from("abc"),
                dev.langchain4j.data.message.TextContent.from("defg")));
        assertEquals(7, PromptSize.chars(multi));
    }

    @Test
    void chars_countsAToolSchema() {
        var withParams = ToolSpecification.builder().name("t").description("d")
                .parameters(dev.langchain4j.model.chat.request.json.JsonObjectSchema.builder()
                        .addStringProperty("namespace", "the namespace to list").required("namespace").build())
                .build();
        int bare = PromptSize.chars(List.of(), List.of(ToolSpecification.builder().name("t").description("d").build()));
        int withSchema = PromptSize.chars(List.of(), List.of(withParams));
        assertTrue(withSchema > bare + "namespace".length() + "the namespace to list".length(),
                "the schema's property names and descriptions are sent, so they count: " + withSchema);
    }
}
