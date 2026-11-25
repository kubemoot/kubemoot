package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.MessageHandler;
import io.nats.client.Subscription;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.Optional;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The coordinator must subscribe once NATS is back, and must not leave a dispatcher behind when a subscribe fails. */
class DiscussionOrchestratorSubscribeTest {

    private NatsConnectionProvider natsProvider;
    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("test-coordinator");
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("kubernetes"));
        when(discuss.settleSeconds()).thenReturn(5);
        when(discuss.minEvalSeconds()).thenReturn(20);
        when(discuss.minReviewSeconds()).thenReturn(10);
        when(discuss.advisoryTimeoutSeconds()).thenReturn(10);
        when(discuss.evaluationTimeoutSeconds()).thenReturn(15);
        when(discuss.reviewTimeoutSeconds()).thenReturn(15);
        when(discuss.synthesisTimeoutSeconds()).thenReturn(90);
        when(discuss.evalGraceSeconds()).thenReturn(10);
        when(discuss.conversationMaxTurns()).thenReturn(10);
        when(discuss.conversationTtlMinutes()).thenReturn(1440);
        when(discuss.conversationKvBucket()).thenReturn("kubemoot_conversations");
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);
        natsProvider = mock(NatsConnectionProvider.class);
        var scope = mock(CrewScope.class);
        when(scope.discussWildcard()).thenReturn("discuss.>");
        when(scope.lifecycleWildcard()).thenReturn("lifecycle.>");
        when(natsProvider.scope()).thenReturn(scope);
        when(natsProvider.isConfigured()).thenReturn(true);
        orchestrator = new DiscussionOrchestrator(natsProvider, properties, mock(ChatService.class),
                new DiscussionMetrics(new SimpleMeterRegistry()), mock(LatencyTracker.class),
                mock(ResumeSearchClient.class));
    }

    @AfterEach
    void tearDown() {
        orchestrator.stopRetrying();
    }

    @Test
    void trySubscribe_isFalseWhileNatsIsDown() {
        when(natsProvider.getConnection()).thenReturn(null);

        assertFalse(orchestrator.trySubscribe());
    }

    @Test
    void trySubscribe_dropsTheDispatcherAndStartsNothingWhenASubscribeFails() {
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class)))
                .thenReturn(mock(Subscription.class))
                .thenThrow(new IllegalStateException("connection closed"));

        assertFalse(orchestrator.trySubscribe());
        verify(conn).closeDispatcher(dispatcher);
        verify(dispatcher, never()).unsubscribe(any(Subscription.class));
    }

    @Test
    void onStart_subscribesOnceNatsComesUp_afterStartingWithItDown() throws Exception {
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        var subscribed = new CountDownLatch(2);
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class))).thenAnswer(inv -> {
            subscribed.countDown();
            return mock(Subscription.class);
        });
        when(natsProvider.getConnection()).thenReturn(null, null, null, conn);
        orchestrator.setSubscribeRetryForTest(new SubscribeRetry("test", Duration.ofMillis(1), Duration.ofMillis(2)));

        orchestrator.onStart(new io.quarkus.runtime.StartupEvent());

        assertTrue(subscribed.await(10, TimeUnit.SECONDS), "the coordinator stayed idle after NATS came up");
    }

    @Test
    void aFailureStartingThePeriodicTasks_closesTheDispatcher_andTheRetryStartsThemOnce() {
        var conn = mock(Connection.class);
        var first = mock(Dispatcher.class);
        var second = mock(Dispatcher.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.createDispatcher()).thenReturn(first, second);
        var spied = org.mockito.Mockito.spy(orchestrator);
        org.mockito.Mockito.doThrow(new IllegalStateException("scheduler is shut down"))
                .doCallRealMethod().when(spied).startPeriodicTasks();

        assertFalse(spied.trySubscribe());
        verify(conn).closeDispatcher(first);

        assertTrue(spied.trySubscribe());
        verify(conn, never()).closeDispatcher(second);
        verify(first, org.mockito.Mockito.times(2)).subscribe(anyString(), any(MessageHandler.class));
        verify(second, org.mockito.Mockito.times(2)).subscribe(anyString(), any(MessageHandler.class));
        spied.stopRetrying();
    }
}
