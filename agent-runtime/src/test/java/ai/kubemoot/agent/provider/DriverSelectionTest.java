package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The lifecycle driver follows the provider's engine: Ollama's calls never reach another engine. */
class DriverSelectionTest {

    /** Records the endpoints it was asked to load or release. */
    private static final class RecordingControl extends OllamaModelControl {
        final List<String> calls = new ArrayList<>();

        RecordingControl() {
            super(new ObjectMapper());
        }

        @Override
        public void startLoad(String endpoint, String model) {
            calls.add("load " + endpoint);
        }

        @Override
        public boolean unload(String endpoint, String model) {
            calls.add("unload " + endpoint);
            return true;
        }
    }

    private static ProviderState state(String engine) {
        return new ProviderState("p", "http://p:1", 1, 0, 0, List.of(), true, "", 0L,
                java.util.Map.of(), java.util.Map.of(), 0L, java.util.Map.of(), java.util.Map.of(), engine);
    }

    private final RecordingControl control = new RecordingControl();
    private final ProviderSelector selector =
            new ProviderSelector(null, new ObjectMapper(), null, null, new OllamaDriver(control));

    @Test
    void ollamaAndUnnamedEnginesGetTheOllamaDriver() {
        assertSame(selector.driver(), selector.driverFor(state("ollama")));
        assertSame(selector.driver(), selector.driverFor(state("")));
        assertSame(selector.driver(), selector.driverFor(state(null)));
        assertSame(selector.driver(), selector.driverFor(state(" Ollama ")));
    }

    @Test
    void vllmGetsADriverThatNeverCallsOllamaEndpoints() {
        EngineDriver d = selector.driverFor(state("vllm"));
        d.warm("http://vllm:8000", "m");
        assertFalse(d.release("http://vllm:8000", "m"));
        assertTrue(control.calls.isEmpty(), "no load or unload may reach a vLLM server: " + control.calls);
    }

    @Test
    void unknownEngineGetsTheNoOpAndLogsOnce() {
        EngineDriver d = selector.driverFor(state("sglang"));
        assertSame(d, selector.driverFor(state("sglang")));
        d.warm("e", "m");
        d.release("e", "m");
        d.warm("e", "m");
        assertTrue(control.calls.isEmpty());
    }

    @Test
    void driverAndChatEngineAgreeOnWhichEnginesAreOllama() {
        for (String engine : new String[] {null, "", " ", "ollama", " OLLAMA ", "vllm", "sglang", "openai"}) {
            boolean ollamaChat = ChatEngines.forType(engine) == ChatEngines.OLLAMA;
            boolean ollamaDriver = selector.driverFor(state(engine)) == selector.driver();
            assertEquals(ollamaChat, ollamaDriver, "engine " + engine);
        }
    }

    @Test
    void bothDriversCanRelease_ollamaByUnloadingAndOtherEnginesThroughTheOperator() {
        assertTrue(selector.driverFor(state("ollama")).canRelease());
        assertTrue(selector.driverFor(state("vllm")).canRelease());
    }

    /** Records the requests that would go to the operator. */
    private static final class RecordingLifecycle implements ModelLifecycle {
        final List<String> asked = new ArrayList<>();
        Reply answer = new Reply(true, "awake", 800L, "");

        @Override
        public Reply ready(String provider, String model) {
            asked.add("ready " + provider + " " + model);
            return answer;
        }

        @Override
        public Reply release(String provider, String model) {
            asked.add("release " + provider + " " + model);
            return answer;
        }
    }

    @Test
    void vllmWakesAndSleepsThroughTheOperatorAndNeverOllamasEndpoints() {
        RecordingLifecycle operator = new RecordingLifecycle();
        selector.useLifecycle(operator);
        EngineDriver d = selector.driverFor(state("vllm"));

        d.ensureReady(state("vllm"), "m");
        assertTrue(d.release(state("vllm"), "m"));

        assertEquals(List.of("ready p m", "release p m"), operator.asked);
        assertTrue(control.calls.isEmpty(), "nothing may reach an Ollama endpoint: " + control.calls);
    }

    @Test
    void aFailedWakeIsAnExceptionNamingTheReason() {
        RecordingLifecycle operator = new RecordingLifecycle();
        operator.answer = new ModelLifecycle.Reply(false, "", 0L, "the server has no sleep mode");
        selector.useLifecycle(operator);
        ModelNotReadyException e = org.junit.jupiter.api.Assertions.assertThrows(ModelNotReadyException.class,
                () -> selector.driverFor(state("vllm")).ensureReady(state("vllm"), "m"));
        assertTrue(e.getMessage().contains("the server has no sleep mode"), e.getMessage());
        assertFalse(selector.driverFor(state("vllm")).release(state("vllm"), "m"));
    }

    @Test
    void aBackgroundWarmAsksTheOperatorWithoutWaiting() {
        RecordingLifecycle operator = new RecordingLifecycle();
        OperatorLifecycleDriver d = new OperatorLifecycleDriver(selector.driver(), "vllm", operator, Runnable::run);
        d.warm(state("vllm"), "m");
        assertEquals(List.of("ready p m"), operator.asked);
    }

    @Test
    void ollamaNeverWaitsForAReadyModel() {
        EngineDriver ollama = selector.driverFor(state("ollama"));
        ollama.ensureReady(state("ollama"), "m"); // load on demand: nothing to wait for
        ollama.warm(state("ollama"), "m");
        assertEquals(List.of("load http://p:1"), control.calls);
    }

    @Test
    void capacityQuestionsStillComeFromTheBaseDriver() {
        EngineDriver d = selector.driverFor(state("vllm"));
        assertEquals(selector.driver().costProfile(), d.costProfile());
        assertEquals(selector.driver().hasFreeCapacity(state("vllm"), 0), d.hasFreeCapacity(state("vllm"), 0));
    }
}
