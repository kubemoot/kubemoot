package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import dev.langchain4j.data.message.UserMessage;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Map;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.*;

/** The chat engines: where each finds the OpenAI-compatible API, and what each sends. */
class ChatEngineTest {

    @Test
    void forType_picksOllamaByDefaultAndTheGenericEngineForOthers() {
        assertSame(ChatEngines.OLLAMA, ChatEngines.forType(null));
        assertSame(ChatEngines.OLLAMA, ChatEngines.forType(""));
        assertSame(ChatEngines.OLLAMA, ChatEngines.forType("Ollama"));
        assertSame(ChatEngines.OLLAMA, ChatEngines.forType(" ollama "));
        assertSame(ChatEngines.OPENAI_COMPATIBLE, ChatEngines.forType("vllm"));
        assertSame(ChatEngines.OPENAI_COMPATIBLE, ChatEngines.forType("openai"));
    }

    @Test
    void providerState_readsTheEngineType_andFallsBackToOllama() throws IOException {
        var json = new ObjectMapper();
        assertSame(ChatEngines.OPENAI_COMPATIBLE,
                ProviderSelector.fromJson(json.readTree("{\"name\":\"v\",\"engine\":\"vllm\"}")).chatEngine());
        assertSame(ChatEngines.OLLAMA, ProviderSelector.fromJson(json.readTree("{\"name\":\"o\"}")).chatEngine(),
                "a provider published before the engine type was is Ollama");
    }

    @Test
    void theApiLivesUnderV1_addedOnce() {
        assertEquals("http://ollama:11434/v1", ChatEngines.OLLAMA.chatBaseUrl("http://ollama:11434"));
        assertEquals("http://ollama:11434/v1", ChatEngines.OLLAMA.chatBaseUrl("http://ollama:11434/"));
        assertEquals("http://vllm:8000/v1", ChatEngines.OPENAI_COMPATIBLE.chatBaseUrl("http://vllm:8000/v1"));
        assertEquals("/v1", ChatEngine.withV1(null));
    }

    @Test
    void thinking_ollamaTurnsItOffByReasoningEffort_othersThroughTheChatTemplate() {
        assertEquals(Map.of("reasoning_effort", "none"), ChatEngines.OLLAMA.thinkingFields(false));
        assertEquals(Map.of(), ChatEngines.OLLAMA.thinkingFields(true), "thinking models think by default");
        assertEquals(Map.of(), ChatEngines.OLLAMA.thinkingFields(null));
        assertEquals(Map.of("chat_template_kwargs", Map.of("enable_thinking", false)),
                ChatEngines.OPENAI_COMPATIBLE.thinkingFields(false));
        assertEquals(Map.of(), ChatEngines.OPENAI_COMPATIBLE.thinkingFields(true),
                "only turning thinking off sends a field a strict endpoint may reject");
        assertEquals(Map.of(), ChatEngines.OPENAI_COMPATIBLE.thinkingFields(null));
    }

    /** The request a pooled model sends, as a server receives it. */
    @Test
    void aPooledModelSendsTheOpenAiRequestWithTheEnginesThinkingField() throws IOException {
        var path = new AtomicReference<String>();
        var body = new AtomicReference<JsonNode>();
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            path.set(exchange.getRequestURI().getPath());
            body.set(new ObjectMapper().readTree(exchange.getRequestBody()));
            byte[] reply = ("{\"id\":\"1\",\"object\":\"chat.completion\",\"created\":1,\"model\":\"qwen3:8b\","
                    + "\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"OK.\"},"
                    + "\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":13,\"completion_tokens\":3,"
                    + "\"total_tokens\":16}}").getBytes(StandardCharsets.UTF_8);
            exchange.getResponseHeaders().add("Content-Type", "application/json");
            exchange.sendResponseHeaders(200, reply.length);
            exchange.getResponseBody().write(reply);
            exchange.close();
        });
        server.start();
        try {
            String endpoint = "http://127.0.0.1:" + server.getAddress().getPort();
            var model = new ChatModelPool().forEndpoint(ChatEngines.OLLAMA, endpoint, "qwen3:8b", 0.3, 512,
                    Duration.ofSeconds(10), false);
            var response = model.chat(UserMessage.from("Say OK."));

            assertEquals("/v1/chat/completions", path.get());
            assertEquals("qwen3:8b", body.get().path("model").asText());
            assertEquals("none", body.get().path("reasoning_effort").asText());
            assertEquals(512, body.get().path("max_tokens").asInt());
            assertEquals("OK.", response.aiMessage().text());
            assertEquals(13, response.tokenUsage().inputTokenCount());
        } finally {
            server.stop(0);
        }
    }
}
