package ai.kubemoot.agent.chat;

import dev.langchain4j.agent.tool.ToolExecutionRequest;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;
import io.micrometer.core.instrument.Metrics;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/** Density counting, tool-result compaction and the per-call prompt report. */
class PromptBudgetPartsTest {

    private static final String NUMERIC = "{\"cpu\":0.1234567,\"mem\":1048576}\n".repeat(400);

    private static ToolExecutionRequest call(String id) {
        return ToolExecutionRequest.builder().id(id).name("artifact_head").arguments("{\"key\":\"k\"}").build();
    }

    private static List<ChatMessage> loop(String... results) {
        List<ChatMessage> messages = new ArrayList<>();
        messages.add(SystemMessage.from("system text"));
        messages.add(UserMessage.from("question"));
        for (int i = 0; i < results.length; i++) {
            messages.add(AiMessage.from(List.of(call("c" + i))));
            messages.add(ToolExecutionResultMessage.from(call("c" + i), results[i]));
        }
        return messages;
    }

    @Test
    void density_countsDigitsAndSymbolsDenser_proseAndNullNot() {
        assertEquals(0, TokenDensity.extraChars(null));
        assertEquals(0, TokenDensity.extraChars("plain words and spaces"));
        assertEquals(3 * TokenDensity.DIGIT_EXTRA, TokenDensity.extraChars("a 123 b"));
        assertEquals(2 * TokenDensity.SYMBOL_EXTRA, TokenDensity.extraChars("{a}"));
    }

    @Test
    void anyToolResultAndUserMessageGetTheDensityExtra_systemAndAssistantDoNot() {
        var messages = loop("12345");
        long extra = TokenDensity.extraChars("12345");
        assertEquals(extra, PromptSize.densityExtra(messages));
        var withThread = new ArrayList<>(messages);
        withThread.add(UserMessage.from("{\"a\":1}"));
        assertEquals(extra + TokenDensity.extraChars("{\"a\":1}"), PromptSize.densityExtra(withThread));
    }

    @Test
    void aNumericResult_isEstimatedAboutAsDenseAsTheEngineCountsIt() {
        var ratio = new CharsPerToken();
        long flat = ratio.tokens(NUMERIC.length());
        long dense = ratio.tokens(NUMERIC.length() + TokenDensity.extraChars(NUMERIC));
        // Qwen3 tokenizes numeric JSON near 1.4 characters per token.
        assertTrue(dense > 1.8 * flat, "flat " + flat + " dense " + dense);
        assertTrue(dense >= NUMERIC.length() / 1.5, "dense " + dense);
    }

    @Test
    void budget_admitsASmallNumericResult_andReplacesOneThatFitsOnlyByLength() {
        var ratio = new CharsPerToken();
        String big = NUMERIC.repeat(3); // about 100 KB-class by tokens, 3x the window room by density
        var budget = new ToolResultBudget(32_768, 8_000, ratio);
        assertFalse(budget.admit("artifact_head", "{\"n\":1}", false).replaced());
        var replaced = budget.admit("artifact_head", big, false);
        assertTrue(replaced.replaced());
        assertTrue(replaced.text().contains("aggregate"), replaced.text());
        assertFalse(new ToolResultBudget(32_768, 8_000, ratio).admit("kubectl", big, false).text().contains("aggregate"));
    }

    @Test
    void compaction_replacesTheLargestResultsFirst_untilTheEstimateFits() {
        var ratio = new CharsPerToken();
        var messages = loop("m".repeat(9_000), "m".repeat(30_000), "m".repeat(12_000));
        long before = ToolResultCompaction.estimate(messages, List.of(), ratio);

        int replaced = ToolResultCompaction.compact(messages, List.of(), before - 5_000, ratio);

        assertEquals(1, replaced);
        var texts = messages.stream().filter(ToolExecutionResultMessage.class::isInstance)
                .map(m -> ((ToolExecutionResultMessage) m).text()).toList();
        assertTrue(ToolResultCompaction.isNote(texts.get(1)), "the 30,000 character result went first");
        assertEquals(9_000, texts.get(0).length());
        assertEquals(12_000, texts.get(2).length());
        assertEquals("c1", ((ToolExecutionResultMessage) messages.get(5)).id(), "the call id is kept");
    }

    @Test
    void compaction_doesNothingWhenItFits_andNeverReplacesSmallResultsOrNotes() {
        var ratio = new CharsPerToken();
        var small = loop("tiny", "also tiny");
        assertEquals(0, ToolResultCompaction.compact(small, List.of(), 1, ratio));
        var big = loop("m".repeat(20_000));
        assertEquals(0, ToolResultCompaction.compact(big, List.of(), 1_000_000, ratio));
        assertEquals(1, ToolResultCompaction.compact(big, List.of(), 100, ratio));
        assertEquals(0, ToolResultCompaction.compact(big, List.of(), 1, ratio), "a note is not replaced again");
        assertFalse(ToolResultCompaction.isNote(null));
    }

    @Test
    void report_splitsThePromptByComponent_andNamesTheCallInTheLine() {
        var ratio = new CharsPerToken();
        var spec = ToolSpecification.builder().name("artifact_head").description("first lines of an artifact").build();
        var messages = loop(NUMERIC);

        var report = PromptReport.of(messages, List.of(spec), ratio);

        assertTrue(report.system() > 0 && report.tools() > 0 && report.thread() > 0 && report.assistant() > 0);
        assertTrue(report.toolResults() > report.system() + report.tools() + report.thread() + report.assistant());
        assertEquals(report.system() + report.tools() + report.thread() + report.assistant() + report.toolResults(),
                report.total());
        String line = report.line("compute", 2, 32_768, 21_000);
        assertTrue(line.startsWith("PROMPT-TOKENS agent=compute call=2 window=32768 reported=21000 estimated="), line);
        assertTrue(line.contains(" toolResults=" + report.toolResults()), line);
        assertTrue(report.line("a", 1, 0, 0).contains("window=unknown"));
    }

    @Test
    void report_recordsEachComponentOnTheRegistry() {
        var registry = new SimpleMeterRegistry();
        Metrics.addRegistry(registry);
        try {
            new PromptReport(10, 20, 30, 40, 50).record(160);
            for (String component : PromptReport.COMPONENTS) {
                assertNotNull(registry.find(PromptReport.METRIC).tag("component", component).summary(), component);
            }
            assertEquals(50, registry.find(PromptReport.METRIC).tag("component", "tool_results").summary().totalAmount());
            assertEquals(160, registry.find(PromptReport.METRIC).tag("component", "reported").summary().totalAmount());
        } finally {
            Metrics.removeRegistry(registry);
        }
    }

    @Test
    void density_emptyTextAndOtherScriptsAreHandled() {
        assertEquals(0, TokenDensity.extraChars(""));
        assertEquals(0, TokenDensity.extraChars("\u00e9t\u00e9 caf\u00e9"), "letters of other scripts are letters");
        assertEquals(TokenDensity.DIGIT_EXTRA, TokenDensity.extraChars("\u0663"), "an Arabic-Indic digit is a digit");
        assertEquals(TokenDensity.DIGIT_EXTRA, ToolResultStandIn.DIGIT_EXTRA);
        assertEquals(TokenDensity.SYMBOL_EXTRA, ToolResultStandIn.SYMBOL_EXTRA);
    }

    @Test
    void compaction_withASmallResult_andAnUnreachableTarget_replacesWhatItCanAndStops() {
        var ratio = new CharsPerToken();
        var messages = loop("m".repeat(5_000), "m".repeat(7_000));
        messages.add(AiMessage.from(List.of(call("cx"))));
        messages.add(new ToolExecutionResultMessage("cx", "artifact_head", "small"));

        int replaced = ToolResultCompaction.compact(messages, List.of(), 0, ratio);

        assertEquals(2, replaced, "both large results, never the small one and never a note twice");
    }

    @Test
    void fullEstimate_isWhatTheNextPromptUsesWithoutAnEngineReport() {
        var ratio = new CharsPerToken();
        var messages = loop(NUMERIC);
        assertEquals(PromptSize.fullEstimateTokens(messages, List.of(), ratio),
                PromptSize.nextPromptTokens(messages, List.of(), 0, 0, 0, ratio));
        assertEquals(PromptSize.fullEstimateTokens(messages, List.of(), ratio),
                ToolResultCompaction.estimate(messages, List.of(), ratio));
    }
}
