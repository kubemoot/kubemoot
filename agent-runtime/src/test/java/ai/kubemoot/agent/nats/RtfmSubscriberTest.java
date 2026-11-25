package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.MessageHandler;
import io.nats.client.Subscription;
import org.junit.jupiter.api.Test;
import org.mockito.Answers;

import java.time.Duration;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The RTFM subscriber must subscribe once NATS is back, not stay disabled after a start with NATS down. */
class RtfmSubscriberTest {

    private static final String SUBJECT = "kubemoot.operator.onboarding.deployed";

    private static AgentProperties rtfmProperties() {
        var props = mock(AgentProperties.class, Answers.RETURNS_DEEP_STUBS);
        when(props.rtfm().mode()).thenReturn(true);
        return props;
    }

    @Test
    void trySubscribe_isFalseWhileNatsIsDown() {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.getConnection()).thenReturn(null);
        var subscriber = new RtfmSubscriber(natsProvider, mock(ChatService.class), rtfmProperties());

        assertFalse(subscriber.trySubscribe());
    }

    @Test
    void trySubscribe_dropsTheDispatcherWhenTheSubscribeFails() {
        var natsProvider = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class)))
                .thenThrow(new IllegalStateException("connection closed"));
        var subscriber = new RtfmSubscriber(natsProvider, mock(ChatService.class), rtfmProperties());

        assertFalse(subscriber.trySubscribe());
        verify(conn).closeDispatcher(dispatcher);
    }

    @Test
    void onStart_subscribesOnceNatsComesUp_afterStartingWithItDown() throws Exception {
        var natsProvider = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        var subscribed = new CountDownLatch(1);
        when(natsProvider.isConfigured()).thenReturn(true);
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class))).thenAnswer(inv -> {
            subscribed.countDown();
            return mock(Subscription.class);
        });
        when(natsProvider.getConnection()).thenReturn(null, null, null, conn);

        var subscriber = new RtfmSubscriber(natsProvider, mock(ChatService.class), rtfmProperties());
        subscriber.setSubscribeRetryForTest(new SubscribeRetry("test", Duration.ofMillis(1), Duration.ofMillis(2)));
        try {
            subscriber.onStart(new io.quarkus.runtime.StartupEvent());
            assertTrue(subscribed.await(10, TimeUnit.SECONDS), "the RTFM subscriber stayed idle after NATS came up");
            verify(dispatcher).subscribe(eq(SUBJECT), any(MessageHandler.class));
        } finally {
            subscriber.stopRetrying();
        }
    }

    @Test
    void onStart_doesNotThrow_whenTheFirstAttemptThrows() {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.isConfigured()).thenReturn(true);
        when(natsProvider.getConnection()).thenThrow(new IllegalStateException("connection closing"));
        var subscriber = new RtfmSubscriber(natsProvider, mock(ChatService.class), rtfmProperties());
        subscriber.setSubscribeRetryForTest(new SubscribeRetry("test", Duration.ofMillis(1), Duration.ofMillis(2)));
        try {
            assertDoesNotThrow(() -> subscriber.onStart(new io.quarkus.runtime.StartupEvent()));
        } finally {
            subscriber.stopRetrying();
        }
    }
}
