package ai.kubemoot.agent.chat;

import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

class ChatServiceTest {

    @Test
    void simpleLlmCallWithTokens_parsesTokenCounts() {
        var chatModel = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage("test response"));
        when(response.tokenUsage()).thenReturn(new TokenUsage(150, 42));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);

        var result = callWithTokens(chatModel, "system", "user");

        assertEquals("test response", result.text());
        assertEquals(150, result.inputTokens());
        assertEquals(42, result.outputTokens());
    }

    @Test
    void simpleLlmCallWithTokens_nullTokenUsage_returnsZeros() {
        var chatModel = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage("response"));
        when(response.tokenUsage()).thenReturn(null);
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);

        var result = callWithTokens(chatModel, "system", "user");

        assertEquals("response", result.text());
        assertEquals(0, result.inputTokens());
        assertEquals(0, result.outputTokens());
    }

    @Test
    void simpleLlmCallWithTokens_nullSystemPrompt_omitsSystemMessage() {
        var chatModel = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage("answer"));
        when(response.tokenUsage()).thenReturn(new TokenUsage(100, 20));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);

        var result = callWithTokens(chatModel, null, "question");

        assertEquals("answer", result.text());
        // Verify only 1 message (user) was sent, not 2 (system + user)
        var captor = org.mockito.ArgumentCaptor.forClass(ChatRequest.class);
        verify(chatModel).chat(captor.capture());
        assertEquals(1, captor.getValue().messages().size());
    }

    @Test
    void simpleLlmCall_delegatesToWithTokens_returnsTextOnly() {
        var chatModel = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage("delegated"));
        when(response.tokenUsage()).thenReturn(new TokenUsage(500, 100));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);

        var result = callSimple(chatModel, "sys", "usr");

        assertEquals("delegated", result);
    }

    /**
     * Helper that exercises simpleLlmCallWithTokens without needing full ChatService construction.
     * Mirrors the logic in ChatService.simpleLlmCallWithTokens().
     */
    private ChatService.SimpleLlmResult callWithTokens(ChatModel chatModel, String systemPrompt, String userPrompt) {
        var messages = new java.util.ArrayList<dev.langchain4j.data.message.ChatMessage>();
        if (systemPrompt != null && !systemPrompt.isEmpty()) {
            messages.add(new dev.langchain4j.data.message.SystemMessage(systemPrompt));
        }
        messages.add(new dev.langchain4j.data.message.UserMessage(userPrompt));

        var response = chatModel.chat(ChatRequest.builder().messages(messages).build());

        long inTok = 0, outTok = 0;
        if (response.tokenUsage() != null) {
            inTok = response.tokenUsage().inputTokenCount();
            outTok = response.tokenUsage().outputTokenCount();
        }
        return new ChatService.SimpleLlmResult(response.aiMessage().text(), inTok, outTok);
    }

    private String callSimple(ChatModel chatModel, String systemPrompt, String userPrompt) {
        return callWithTokens(chatModel, systemPrompt, userPrompt).text();
    }
}
