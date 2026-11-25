package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ModelWarmupService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.Answers;

import java.time.Duration;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** A scaled-from-zero agent that starts while NATS is down sends its wake-up signals once NATS is back. */
class WakeUpSignalServiceTest {

    private static WakeUpSignalService service(NatsConnectionProvider natsProvider) {
        var props = mock(AgentProperties.class, Answers.RETURNS_DEEP_STUBS);
        when(props.discuss().coordinator()).thenReturn(false);
        when(props.agentName()).thenReturn("agent-a");
        var warmup = mock(ModelWarmupService.class);
        when(warmup.isModelReady()).thenReturn(true);
        return new WakeUpSignalService(natsProvider, warmup, props, new ObjectMapper());
    }

    @Test
    void signalWhenConnected_isFalseWhileNatsIsDown() {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.getConnection()).thenReturn(null);

        assertFalse(service(natsProvider).signalWhenConnected());
    }

    @Test
    void onStart_sendsWakingAndReadyOnceNatsComesUp_afterStartingWithItDown() throws Exception {
        var natsProvider = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        var scope = mock(CrewScope.class);
        var sent = new CountDownLatch(2);
        when(natsProvider.isConfigured()).thenReturn(true);
        when(natsProvider.scope()).thenReturn(scope);
        when(scope.lifecycleSubject(anyString(), anyString())).thenAnswer(inv -> "lifecycle." + inv.getArgument(0));
        org.mockito.Mockito.doAnswer(inv -> {
            sent.countDown();
            return null;
        }).when(conn).publish(anyString(), any(byte[].class));
        when(natsProvider.getConnection()).thenReturn(null, null, null, conn);
        var service = service(natsProvider);
        service.setConnectRetryForTest(new SubscribeRetry("test", Duration.ofMillis(1), Duration.ofMillis(2)));
        try {
            service.onStart(new io.quarkus.runtime.StartupEvent());
            assertTrue(sent.await(10, TimeUnit.SECONDS), "no waking and ready signals after NATS came up");
        } finally {
            service.stopRetrying();
        }
    }
}
