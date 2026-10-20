package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Where a call for a model goes: a vLLM ModelServer runs one server per model. */
class ServerStateEndpointTest {

    private static final ObjectMapper JSON = new ObjectMapper();

    private static ServerState parse(String json) throws Exception {
        return ServerSelector.fromJson(JSON.readTree(json));
    }

    @Test
    void vllmServerSendsEachModelToItsOwnServer() throws Exception {
        ServerState ps = parse("""
                {"name":"vllm-gpu","engine":"vllm","endpoint":"",
                 "modelEndpoints":{"qwen3:14b":"http://vllm-14b:8000","qwen3:8b":"http://vllm-8b:8000"}}""");

        assertEquals("http://vllm-14b:8000", ps.endpointFor("qwen3:14b"));
        assertEquals("http://vllm-8b:8000", ps.endpointFor("qwen3:8b"));
        assertSame(ChatEngines.OPENAI_COMPATIBLE, ps.chatEngine());
    }

    @Test
    void aModelWithoutItsOwnServerFallsBackToTheServerEndpoint() throws Exception {
        ServerState ps = parse("""
                {"name":"vllm-gpu","engine":"vllm","endpoint":"http://pool:8000",
                 "modelEndpoints":{"qwen3:14b":"http://vllm-14b:8000"}}""");

        assertEquals("http://pool:8000", ps.endpointFor("other"));
        assertEquals("http://pool:8000", ps.endpointFor(null));
    }

    @Test
    void anOllamaServerServesEveryModelAtItsEndpoint() throws Exception {
        ServerState ps = parse("{\"name\":\"o\",\"endpoint\":\"http://ollama:11434\"}");

        assertEquals("http://ollama:11434", ps.endpointFor("qwen3:14b"));
        assertTrue(ps.modelEndpoints().isEmpty(), "an operator that publishes no modelEndpoints parses to an empty map");
    }

    @Test
    void latencyIsKeptPerServerWhenThereIsNoSingleEndpoint() throws Exception {
        assertEquals("vllm-a", parse("{\"name\":\"vllm-a\",\"endpoint\":\"\"}").latencyKey());
        assertEquals("http://o:11434", parse("{\"name\":\"o\",\"endpoint\":\"http://o:11434\"}").latencyKey());
    }

    @Test
    void blankAndNonTextEntriesAreIgnored() throws Exception {
        ServerState ps = parse("""
                {"name":"p","endpoint":"http://pool:8000","modelEndpoints":{"a":"","b":7,"c":"http://c:8000"}}""");

        assertEquals("http://pool:8000", ps.endpointFor("a"));
        assertEquals("http://pool:8000", ps.endpointFor("b"));
        assertEquals("http://c:8000", ps.endpointFor("c"));
    }
}
