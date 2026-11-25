package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.logging.LogContent;
import ai.kubemoot.agent.chat.ModelWarmupService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.quarkus.runtime.StartupEvent;
import jakarta.annotation.PreDestroy;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Instant;
import java.util.Map;
import java.util.UUID;

/**
 * Publishes {@code waking} and {@code ready} signals for KEDA scale-to-zero support.
 *
 * When an agent pod starts from zero replicas, the coordinator needs to know that
 * agents are booting so it doesn't close the discussion before they can participate.
 *
 * Lifecycle:
 * 1. On startup, immediately publish {@code waking} — tells the coordinator "I'm starting,
 *    don't settle yet" (adds 60s deadline to pendingEvaluations).
 * 2. Poll until model warmup completes, then publish {@code ready} — tells the coordinator
 *    "I'm fully initialized, resume normal timing."
 *
 * Both signals are published to the namespace- and crew-scoped lifecycle subject
 * ({@link CrewScope#lifecycleSubject}), which the coordinator subscribes to.
 */
@ApplicationScoped
public class WakeUpSignalService {

    private static final Logger log = LoggerFactory.getLogger(WakeUpSignalService.class);

    // Wait between model-warmup readiness polls (ms).
    private static final long WARMUP_POLL_MS = 1000;

    private final NatsConnectionProvider natsProvider;
    private final ModelWarmupService warmupService;
    private final AgentProperties properties;
    private final ObjectMapper mapper;
    private SubscribeRetry connectRetry = new SubscribeRetry("wakeup-signal");

    public WakeUpSignalService(NatsConnectionProvider natsProvider,
                                ModelWarmupService warmupService,
                                AgentProperties properties,
                                ObjectMapper mapper) {
        this.natsProvider = natsProvider;
        this.warmupService = warmupService;
        this.properties = properties;
        this.mapper = mapper;
    }

    void onStart(@Observes StartupEvent event) {
        if (!natsProvider.isConfigured()) {
            return;
        }
        if (properties.discuss().coordinator()) {
            return; // Coordinator doesn't scale to zero
        }

        // A start while NATS is unreachable publishes the signals once it is back, with backoff.
        if (!connectRetry.attemptOrRetry(this::signalWhenConnected)) {
            log.warn("NATS not available - the wake-up signals are sent once it is");
        }
    }

    /** Test seam: retry on a short backoff so a test waits on the state, not on a clock. */
    void setConnectRetryForTest(SubscribeRetry retry) {
        this.connectRetry = retry;
    }

    @PreDestroy
    void stopRetrying() {
        connectRetry.stop();
    }

    /** One attempt: false while there is no NATS connection; otherwise sends the signals and returns true. */
    // Visible for testing
    boolean signalWhenConnected() {
        if (natsProvider.getConnection() == null) {
            return false;
        }
        // Phase 1: Publish waking immediately
        publishWakeUpSignal("waking");

        // Phase 2: Wait for model warmup, then publish ready
        Thread.ofVirtual().name("wakeup-signal").start(this::signalReadyWhenWarm);
        return true;
    }

    private void signalReadyWhenWarm() {
        while (!warmupService.isModelReady()) {
            try { Thread.sleep(WARMUP_POLL_MS); } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
        }

        publishWakeUpSignal("ready");
    }

    private void publishWakeUpSignal(String signal) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            String agentName = properties.agentName();

            var message = Map.of(
                    "messageId", UUID.randomUUID().toString(),
                    "threadId", "",
                    "agentName", agentName,
                    "messageType", signal,
                    "content", "",
                    "channel", "broadcast",
                    "timestamp", Instant.now().toString(),
                    "metadata", Map.of("signal", signal)
            );

            // Publish to lifecycle subject OUTSIDE the KUBEMOOT_DISCUSS JetStream stream.
            // Using kubemoot.discuss.> would cause waking signals to appear as consumer lag
            // in every agent's JetStream consumer, triggering a bootstrap storm where agents
            // wake each other up in a feedback loop via KEDA.
            String subject = natsProvider.scope().lifecycleSubject(signal, agentName);

            conn.publish(subject, mapper.writeValueAsBytes(message));
            log.info("Published {} signal to {} for agent {}", signal, subject, agentName);

        } catch (Exception e) {
            log.warn("Failed to publish {} signal: {}", signal, LogContent.reason(e));
        }
    }
}
