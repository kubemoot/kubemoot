package ai.kubemoot.agent;

import ai.kubemoot.agent.chat.ModelWarmupService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.nats.DiscussionSubscriber;
import ai.kubemoot.agent.nats.OnboardingSubscriber;
import ai.kubemoot.agent.nats.RequestQueueConsumer;
import ai.kubemoot.agent.nats.RtfmSubscriber;
import ai.kubemoot.agent.nats.WakeUpSignalService;

import java.util.stream.Stream;

/** The beans whose {@code @Observes StartupEvent} method starts an agent role. */
final class StartupBeans {

    private StartupBeans() {
    }

    static Stream<Class<?>> all() {
        return Stream.of(
                DiscussionSubscriber.class,
                DiscussionOrchestrator.class,
                RtfmSubscriber.class,
                OnboardingSubscriber.class,
                RequestQueueConsumer.class,
                WakeUpSignalService.class,
                AgentHeartbeatService.class,
                ModelWarmupService.class);
    }
}
