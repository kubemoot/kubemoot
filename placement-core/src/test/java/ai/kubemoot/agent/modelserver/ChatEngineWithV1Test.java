package ai.kubemoot.agent.modelserver;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class ChatEngineWithV1Test {

    @Test
    void v1IsAppendedOnceAndTrailingSlashesAreDropped() {
        assertEquals("http://e:8000/v1", ChatEngine.withV1("http://e:8000"));
        assertEquals("http://e:8000/v1", ChatEngine.withV1("http://e:8000///"));
        assertEquals("http://e:8000/v1", ChatEngine.withV1("http://e:8000/v1"));
        assertEquals("http://e:8000/v1", ChatEngine.withV1("http://e:8000/v1/"));
        assertEquals("/v1", ChatEngine.withV1(null));
        assertEquals("/v1", ChatEngine.withV1("///"));
    }
}
