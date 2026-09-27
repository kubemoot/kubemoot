package ai.kubemoot.agent.provider;

import dev.langchain4j.model.chat.ChatModel;
import org.junit.jupiter.api.Test;

import java.time.Duration;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link ChatModelPool} — the per-endpoint lazy ChatModel cache
 * that backs JIT provider selection. Building a model is offline (no ollama
 * call until {@code .chat()}), so the cache behavior is testable in plain JUnit.
 *
 * Focus: the {@code think} parameter (per-agent thinking control) must (a) be
 * honored without error for null/true/false and (b) participate in the cache
 * key so two agents differing only in thinking get distinct model instances.
 */
class ChatModelPoolTest {

    private static final String EP = "http://ollama.test:11434";
    private static final String MODEL = "qwen3:8b";
    private static final Duration T = Duration.ofMinutes(5);

    private ChatModel build(ChatModelPool pool, Boolean think) {
        return pool.forEndpoint(EP, MODEL, 0.3, 4096, T, think);
    }

    @Test
    void buildsForAllThinkValues_nullTrueFalse() {
        var pool = new ChatModelPool();
        assertNotNull(build(pool, null), "think=null (model default) must build");
        assertNotNull(build(pool, Boolean.TRUE), "think=true must build");
        assertNotNull(build(pool, Boolean.FALSE), "think=false must build");
    }

    @Test
    void thinkValueIsPartOfCacheKey() {
        var pool = new ChatModelPool();
        pool.clearForTest();
        build(pool, Boolean.FALSE);
        build(pool, Boolean.TRUE);
        build(pool, null);
        assertEquals(3, pool.sizeForTest(),
                "null, true, and false must each cache a distinct model instance");
    }

    @Test
    void sameParamsReuseSingleInstance() {
        var pool = new ChatModelPool();
        pool.clearForTest();
        ChatModel a = build(pool, Boolean.FALSE);
        ChatModel b = build(pool, Boolean.FALSE);
        assertSame(a, b, "identical params (incl. think) must reuse the cached model");
        assertEquals(1, pool.sizeForTest());
    }

    @Test
    void sameEndpointDifferentModel_buildsDistinctInstances() {
        // A per-call candidate pick can send a different model to an endpoint the
        // pool already serves; the cached model for the first must not be reused.
        var pool = new ChatModelPool();
        pool.clearForTest();
        ChatModel big = pool.forEndpoint(EP, "qwen3:32b", 0.3, 4096, T, null);
        ChatModel mid = pool.forEndpoint(EP, "qwen3:14b", 0.3, 4096, T, null);
        assertNotSame(big, mid);
        assertEquals(2, pool.sizeForTest());
    }
}
