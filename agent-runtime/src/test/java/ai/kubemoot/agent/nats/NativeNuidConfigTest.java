package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertTrue;

/** The native image settings that keep jnats consumer names unique across pods. */
class NativeNuidConfigTest {

    @Test
    void nativeBuildInitializesNuidAtRunTime() throws Exception {
        String props = Files.readString(Path.of("src/main/resources/application.properties"));
        assertTrue(props.contains("--initialize-at-run-time=io.nats.client.NUID"),
                "every pod must start from its own NUID state, or consumer names repeat across pods");
    }

    @Test
    void nativeBuildTargetsEveryX8664Cpu() throws Exception {
        String props = Files.readString(Path.of("src/main/resources/application.properties"));
        assertTrue(props.contains("-march=compatibility"),
                "the binary must not depend on the build machine's CPU features");
    }
}
