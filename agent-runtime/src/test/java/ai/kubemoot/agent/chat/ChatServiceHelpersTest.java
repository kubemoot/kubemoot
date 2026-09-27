package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Tests for ChatService inner records and value types.
 * Uses plain JUnit 5 (no @QuarkusTest) to avoid Ollama dependency in CI.
 */
class ChatServiceHelpersTest {

    @Test
    void chatRequest_generatesConversationIdWhenNull() {
        var request = new ChatService.ChatRequest(null, "hello");
        assertNotNull(request.conversationId());
        assertFalse(request.conversationId().isEmpty());
    }

    @Test
    void chatRequest_generatesConversationIdWhenEmpty() {
        var request = new ChatService.ChatRequest("", "hello");
        assertNotNull(request.conversationId());
        assertFalse(request.conversationId().isEmpty());
    }

    @Test
    void chatRequest_preservesExplicitConversationId() {
        var request = new ChatService.ChatRequest("my-id", "hello");
        assertEquals("my-id", request.conversationId());
    }

    @Test
    void chatRequest_preservesMessage() {
        var request = new ChatService.ChatRequest("id", "what is k8s?");
        assertEquals("what is k8s?", request.message());
    }

    @Test
    void chatRequest_twoArgConstructor_crewIsNull() {
        var request = new ChatService.ChatRequest("id", "msg");
        assertNull(request.crew());
    }

    @Test
    void chatRequest_retrievalText_isTheRetrievalQueryWhenGiven() {
        var request = new ChatService.ChatRequest("id", "[User Question] ... [Response (x)] ...", null, "t", "What does Degraded mean?");
        assertEquals("What does Degraded mean?", request.retrievalText());
    }

    @Test
    void chatRequest_retrievalText_fallsBackToTheMessage() {
        assertEquals("msg", new ChatService.ChatRequest("id", "msg").retrievalText());
        assertEquals("msg", new ChatService.ChatRequest("id", "msg", null, "t", "  ").retrievalText());
        assertNull(new ChatService.ChatRequest("id", "msg", null, "t").retrievalQuery());
    }

    @Test
    void chatRequest_threeArgConstructor_preservesCrew() {
        var request = new ChatService.ChatRequest("id", "msg", "homelab-pilot");
        assertEquals("homelab-pilot", request.crew());
    }

    @Test
    void chatResult_convenienceConstructor_zeroTokens() {
        var result = new ChatService.ChatResult("conv-1", "answer", "qwen2.5:32b", "thread-1");
        assertEquals(0, result.inputTokens());
        assertEquals(0, result.outputTokens());
        assertEquals("conv-1", result.conversationId());
        assertEquals("answer", result.response());
        assertEquals("qwen2.5:32b", result.model());
        assertEquals("thread-1", result.threadId());
    }

    @Test
    void chatResult_fullConstructor_preservesTokens() {
        var result = new ChatService.ChatResult("c", "r", "m", "t", 500, 200);
        assertEquals(500, result.inputTokens());
        assertEquals(200, result.outputTokens());
    }

    @Test
    void simpleLlmResult_fieldsAccessible() {
        var result = new ChatService.SimpleLlmResult("hello world", 100, 50);
        assertEquals("hello world", result.text());
        assertEquals(100, result.inputTokens());
        assertEquals(50, result.outputTokens());
    }

    @Test
    void chatRequest_uniqueIdsForDifferentRequests() {
        var r1 = new ChatService.ChatRequest(null, "a");
        var r2 = new ChatService.ChatRequest(null, "b");
        assertNotEquals(r1.conversationId(), r2.conversationId());
    }

    // --- Model tiering: triage-model call parsing (advisory + subcommittee triage
    //     run on the fast triage model; these confirm the response parse) ---

    @Test
    void parseOllamaChat_extractsTextAndTokenCounts() throws Exception {
        var json = new ObjectMapper().readTree(
                "{\"message\":{\"role\":\"assistant\",\"content\":\"{\\\"technologies\\\":[\\\"gpu\\\"]}\"}," +
                        "\"prompt_eval_count\":640,\"eval_count\":42}");
        var result = ChatService.parseOllamaChat(json);
        assertEquals("{\"technologies\":[\"gpu\"]}", result.text());
        assertEquals(640, result.inputTokens());
        assertEquals(42, result.outputTokens());
    }

    @Test
    void parseOllamaChat_missingTokenFieldsDefaultToZero() throws Exception {
        var json = new ObjectMapper().readTree(
                "{\"message\":{\"role\":\"assistant\",\"content\":\"hi\"}}");
        var result = ChatService.parseOllamaChat(json);
        assertEquals("hi", result.text());
        assertEquals(0, result.inputTokens());
        assertEquals(0, result.outputTokens());
    }

    @Test
    void parseOllamaChat_missingMessageYieldsEmptyText() throws Exception {
        var json = new ObjectMapper().readTree("{\"eval_count\":5}");
        var result = ChatService.parseOllamaChat(json);
        assertEquals("", result.text());
        assertEquals(5, result.outputTokens());
    }
}
