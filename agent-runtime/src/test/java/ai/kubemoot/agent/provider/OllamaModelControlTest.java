package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/** Ollama's load and unload requests, and the driver that wraps them. */
class OllamaModelControlTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void unloadSetsKeepAliveZero_loadLeavesTheProviderDefault() throws Exception {
        var control = new OllamaModelControl(MAPPER);
        var unload = MAPPER.readTree(control.body("qwen3:14b", true));
        assertEquals("qwen3:14b", unload.path("model").asText());
        assertEquals(0, unload.path("keep_alive").asInt(-1));
        var load = MAPPER.readTree(control.body("qwen3:14b", false));
        assertTrue(load.path("keep_alive").isMissingNode());
    }

    @Test
    void unreachableProvider_reportsFailureWithoutThrowing() {
        var control = new OllamaModelControl(MAPPER);
        assertFalse(control.unload("http://127.0.0.1:1", "qwen3:14b"));
    }

    @Test
    void driverWithoutControl_neverReportsARelease() {
        var driver = new OllamaDriver(null);
        assertFalse(driver.release("http://x", "m"));
        driver.warm("http://x", "m");
        assertEquals(OllamaDriver.RELEASE_SECONDS, driver.costProfile().releaseSeconds());
    }

    @Test
    void driverConcurrency_isTheProvidersSlots() {
        var driver = new OllamaDriver(null);
        var twoSlots = new ProviderState("p", "http://p", 2, 0, 0, java.util.List.of(), true, "", 0L, java.util.Map.of());
        assertTrue(driver.hasFreeCapacity(twoSlots, 1));
        assertFalse(driver.hasFreeCapacity(twoSlots, 2));
    }
}
