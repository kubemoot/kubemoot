package ai.kubemoot.placement;

import org.junit.jupiter.api.Test;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertTrue;

/** The native image runs on any x86-64 CPU, whichever machine built it. */
class NativeMarchConfigTest {

    @Test
    void nativeBuildTargetsEveryX8664Cpu() throws Exception {
        String props = Files.readString(Path.of("src/main/resources/application.properties"));
        assertTrue(props.contains("-march=compatibility"),
                "the binary must not depend on the build machine's CPU features");
    }
}
