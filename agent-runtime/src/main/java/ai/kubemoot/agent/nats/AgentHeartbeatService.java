package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ModelWarmupService;
import ai.kubemoot.agent.config.AgentProperties;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Instant;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

/**
 * Publishes periodic heartbeats to NATS KV bucket for agent liveness tracking, keyed
 * {@code <namespace>.<agent>} (see {@link CrewScope#agentStateKey}).
 * Each record carries {@code intervalSeconds}, the period between beats, so consumers
 * derive staleness from the heartbeat itself.
 * Graceful no-op when NATS is unavailable (matches existing pattern).
 */
@ApplicationScoped
public class AgentHeartbeatService {

    private static final Logger log = LoggerFactory.getLogger(AgentHeartbeatService.class);

    /** Interval used when the configured one is not a positive number. */
    static final int FALLBACK_INTERVAL_SECONDS = 60;

    private final NatsConnectionProvider natsConnectionProvider;
    private final ModelWarmupService warmupService;
    private final AgentProperties properties;
    private volatile Instant lastInference;

    public AgentHeartbeatService(NatsConnectionProvider natsConnectionProvider,
                                  ModelWarmupService warmupService,
                                  AgentProperties properties) {
        this.natsConnectionProvider = natsConnectionProvider;
        this.warmupService = warmupService;
        this.properties = properties;
    }

    void onStart(@Observes StartupEvent event) {
        if (!properties.heartbeat().enabled()) {
            log.info("Agent heartbeat disabled");
            return;
        }
        if (!natsConnectionProvider.isConfigured()) {
            log.info("NATS not configured — heartbeat disabled");
            return;
        }

        int intervalSeconds = effectiveIntervalSeconds();
        ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(1,
                r -> Thread.ofVirtual().name("heartbeat").unstarted(r));
        scheduler.scheduleAtFixedRate(this::publishHeartbeat, intervalSeconds, intervalSeconds, TimeUnit.SECONDS);
        log.info("Agent heartbeat started: interval={}s, bucket={}", intervalSeconds, properties.heartbeat().kvBucket());
    }

    /** The configured interval, or the fallback when it is zero or negative. */
    int effectiveIntervalSeconds() {
        int configured = properties.heartbeat().intervalSeconds();
        return configured > 0 ? configured : FALLBACK_INTERVAL_SECONDS;
    }

    /**
     * Record the time of the last successful inference.
     * Called by ChatService after a successful LLM call.
     */
    public void recordInference() {
        lastInference = Instant.now();
    }

    void publishHeartbeat() {
        try {
            var conn = natsConnectionProvider.getConnection();
            if (conn == null) {
                return;
            }

            boolean natsConnected = natsConnectionProvider.isAvailable();
            boolean ollamaReachable = warmupService.isOllamaReachable();
            String agentName = properties.agentName();
            String model = properties.model().model();
            Instant now = Instant.now();
            Instant inference = lastInference;

            String json = buildHeartbeatJson(agentName, now, natsConnected, ollamaReachable, model, inference,
                    effectiveIntervalSeconds());

            var kv = conn.keyValue(properties.heartbeat().kvBucket());
            kv.put(natsConnectionProvider.scope().agentStateKey(agentName), json.getBytes());
        } catch (Exception e) {
            log.warn("Failed to publish heartbeat: {}", e.getMessage());
        }
    }

    String buildHeartbeatJson(String agent, Instant timestamp, boolean nats, boolean ollama,
                               String model, Instant lastInferenceTime, int intervalSeconds) {
        var sb = new StringBuilder();
        sb.append("{\"agent\":\"").append(escapeJson(agent)).append("\"");
        sb.append(",\"timestamp\":\"").append(timestamp.toString()).append("\"");
        sb.append(",\"nats\":").append(nats);
        sb.append(",\"ollama\":").append(ollama);
        sb.append(",\"model\":\"").append(escapeJson(model)).append("\"");
        sb.append(",\"intervalSeconds\":").append(intervalSeconds);
        if (lastInferenceTime != null) {
            sb.append(",\"lastInference\":\"").append(lastInferenceTime.toString()).append("\"");
        }
        sb.append("}");
        return sb.toString();
    }

    private static String escapeJson(String s) {
        if (s == null) return "";
        return s.replace("\\", "\\\\").replace("\"", "\\\"");
    }
}
