package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.List;
import org.junit.jupiter.api.Test;

class NativeSelfTestTest {

    @Test
    void everyCheckPassesOnTheJvm() {
        assertEquals(List.of(), new NativeSelfTest(true).run());
    }

    @Test
    void disabledSelfTestDoesNothingAtStartup() {
        // A disabled self-test must not run (or exit the process): onStart returns before any check.
        new NativeSelfTest(false).onStart(null);
    }

    @Test
    void aThrowingCheckIsReportedByNameAndTheOthersStillRun() {
        var ran = new java.util.ArrayList<String>();
        var checks = new java.util.LinkedHashMap<String, Runnable>();
        checks.put("first", () -> {
            throw new IllegalStateException("boom");
        });
        checks.put("second", () -> ran.add("second"));
        checks.put("third", () -> {
            throw new NoClassDefFoundError("org/yaml/snakeyaml/Yaml");
        });
        var failures = NativeSelfTest.runAll(checks);
        assertEquals(2, failures.size());
        assertTrue(failures.get(0).startsWith("first: ") && failures.get(0).contains("boom"));
        assertTrue(failures.get(1).startsWith("third: ") && failures.get(1).contains("snakeyaml"));
        assertEquals(List.of("second"), ran);
    }

    @Test
    void markersAreWhatTheWorkflowGreps() {
        assertEquals("NATIVE_SELFTEST_OK", NativeSelfTest.OK_MARKER);
        assertEquals("NATIVE_SELFTEST_FAILED", NativeSelfTest.FAILED_MARKER);
        assertFalse(NativeSelfTest.OK_MARKER.contains(NativeSelfTest.FAILED_MARKER));
        assertTrue(NativeSelfTest.FAILED_MARKER.startsWith("NATIVE_SELFTEST_"));
    }
}
