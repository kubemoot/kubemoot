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
import java.util.Optional;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The onboarding subscriber must subscribe once NATS is back, not stay disabled after a start with NATS down. */
class OnboardingSubscriberTest {

    private static AgentProperties properties() {
        var props = mock(AgentProperties.class, Answers.RETURNS_DEEP_STUBS);
        when(props.onboarding().mode()).thenReturn(true);
        when(props.discuss().channels()).thenReturn(Optional.of("general"));
        return props;
    }

    private static NatsConnectionProvider provider(Connection conn) {
        var natsProvider = mock(NatsConnectionProvider.class);
        var scope = mock(CrewScope.class);
        when(scope.channelWildcard(anyString())).thenAnswer(inv -> "subject." + inv.getArgument(0));
        when(natsProvider.scope()).thenReturn(scope);
        when(natsProvider.isConfigured()).thenReturn(true);
        when(natsProvider.getConnection()).thenReturn(conn);
        return natsProvider;
    }

    @Test
    void trySubscribe_isFalseWhileNatsIsDown() {
        var subscriber = new OnboardingSubscriber(provider(null), mock(ChatService.class), properties());

        assertFalse(subscriber.trySubscribe());
    }

    @Test
    void trySubscribe_dropsTheDispatcherWhenASubscribeFails() {
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class)))
                .thenReturn(mock(Subscription.class))
                .thenThrow(new IllegalStateException("connection closed"));
        var subscriber = new OnboardingSubscriber(provider(conn), mock(ChatService.class), properties());

        assertFalse(subscriber.trySubscribe());
        verify(conn).closeDispatcher(dispatcher);
    }

    @Test
    void onStart_subscribesOnceNatsComesUp_afterStartingWithItDown() throws Exception {
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        var subscribed = new CountDownLatch(1);
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class))).thenAnswer(inv -> {
            subscribed.countDown();
            return mock(Subscription.class);
        });
        var natsProvider = provider(null);
        when(natsProvider.getConnection()).thenReturn(null, null, null, conn);
        var subscriber = new OnboardingSubscriber(natsProvider, mock(ChatService.class), properties());
        subscriber.setSubscribeRetryForTest(new SubscribeRetry("test", Duration.ofMillis(1), Duration.ofMillis(2)));
        try {
            subscriber.onStart(new io.quarkus.runtime.StartupEvent());
            assertTrue(subscribed.await(10, TimeUnit.SECONDS), "the onboarding subscriber stayed idle after NATS came up");
        } finally {
            subscriber.stopRetrying();
        }
    }

    @Test
    void aRetryThatSucceeds_keepsItsDispatcher() {
        var conn = mock(Connection.class);
        var first = mock(Dispatcher.class);
        var second = mock(Dispatcher.class);
        when(conn.createDispatcher()).thenReturn(first, second);
        when(first.subscribe(anyString(), any(MessageHandler.class))).thenThrow(new IllegalStateException("connection closed"));
        when(second.subscribe(anyString(), any(MessageHandler.class))).thenReturn(mock(Subscription.class));
        var subscriber = new OnboardingSubscriber(provider(conn), mock(ChatService.class), properties());

        assertFalse(subscriber.trySubscribe());
        assertTrue(subscriber.trySubscribe());

        verify(conn).closeDispatcher(first);
        verify(conn, org.mockito.Mockito.never()).closeDispatcher(second);
    }
}
