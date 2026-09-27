package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.slf4j.Logger;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

/** Key listing failures are reported at WARN, rate-limited; the native build keeps NUID per process. */
class KvKeysTest {

    @BeforeEach
    void reset() {
        KvKeys.resetWarningsForTest();
    }

    @Test
    void failedRead_warnsOnceAMinute_thenDebugs() {
        var log = mock(Logger.class);
        var error = new IOException("deliver policy can not be updated [10012]");

        KvKeys.warnReadFailure(log, "kubemoot_provider_state", "provider state", error);
        KvKeys.warnReadFailure(log, "kubemoot_provider_state", "provider state", error);
        KvKeys.warnReadFailure(log, "kubemoot_provider_tickets", "tickets", error);

        verify(log, times(2)).warn(anyString(), any(), any(), contains("10012"));
        verify(log, times(1)).debug(anyString(), any(), any(), any());
    }

    @Test
    void listPropagatesTheNatsError() throws Exception {
        var conn = mock(Connection.class);
        var jsm = mock(JetStreamManagement.class);
        when(conn.jetStreamManagement()).thenReturn(jsm);
        when(jsm.getStreamInfo(anyString(), any())).thenThrow(new IOException("timeout"));
        assertThrows(IOException.class, () -> KvKeys.list(conn, "b"));
    }

    @Test
    void nativeBuildInitializesNuidAtRunTime() throws Exception {
        String props = Files.readString(Path.of("src/main/resources/application.properties"));
        assertTrue(props.contains("--initialize-at-run-time=io.nats.client.NUID"),
                "every pod must start from its own NUID state, or consumer names repeat across pods");
    }

    @Test
    void listStripsTheBucketPrefix_andIgnoresOtherSubjects() throws Exception {
        var conn = mock(Connection.class);
        var jsm = mock(JetStreamManagement.class);
        var info = mock(io.nats.client.api.StreamInfo.class);
        var state = mock(io.nats.client.api.StreamState.class);
        when(conn.jetStreamManagement()).thenReturn(jsm);
        when(jsm.getStreamInfo(eq("KV_kubemoot_provider_state"), any())).thenReturn(info);
        when(info.getStreamState()).thenReturn(state);
        when(state.getSubjects()).thenReturn(java.util.List.of(
                new io.nats.client.api.Subject("$KV.kubemoot_provider_state.ollama-a", 1),
                new io.nats.client.api.Subject("$KV.kubemoot_provider_state.ollama.b", 1),
                new io.nats.client.api.Subject("$KV.other.x", 1)));

        assertEquals(java.util.List.of("ollama-a", "ollama.b"), KvKeys.list(conn, "kubemoot_provider_state"));

        when(state.getSubjects()).thenReturn(null);
        assertTrue(KvKeys.list(conn, "kubemoot_provider_state").isEmpty(), "an empty stream lists no keys");
    }
}
