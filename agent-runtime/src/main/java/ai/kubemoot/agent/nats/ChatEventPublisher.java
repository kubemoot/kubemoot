package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import jakarta.enterprise.context.ApplicationScoped;
import java.time.Instant;
import java.util.Map;

/**
 * Publishes chat events to NATS for observability via the Kubemoot dashboard messages page.
 * No-op when NATS_URL is not set.
 */
@ApplicationScoped
public class ChatEventPublisher {

    private static final Logger log = LoggerFactory.getLogger(ChatEventPublisher.class);
    private static final ObjectMapper mapper = new ObjectMapper();

    // Max chars of the user message persisted on the chat event for the dashboard.
    private static final int EVENT_USER_MESSAGE_CHARS = 500;
    // Max chars of the agent response persisted on the chat event for the dashboard.
    private static final int EVENT_RESPONSE_CHARS = 2000;

    private final NatsConnectionProvider natsProvider;

    public ChatEventPublisher(NatsConnectionProvider natsProvider) {
        this.natsProvider = natsProvider;
    }

    public void publishChatEvent(String conversationId, String userMessage, String response, String model,
                                 long inputTokens, long outputTokens) {
        if (!natsProvider.isAvailable()) return;

        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            String subject = "kubemoot.chat." + natsProvider.getAgentName().replace("-", "_");
            var event = Map.ofEntries(
                    Map.entry("agent", natsProvider.getAgentName()),
                    Map.entry("conversationId", conversationId),
                    Map.entry("userMessage", truncate(userMessage, EVENT_USER_MESSAGE_CHARS)),
                    Map.entry("response", truncate(response, EVENT_RESPONSE_CHARS)),
                    Map.entry("model", model),
                    Map.entry("inputTokens", inputTokens),
                    Map.entry("outputTokens", outputTokens),
                    Map.entry("timestamp", Instant.now().toString())
            );

            conn.publish(subject, mapper.writeValueAsBytes(event));
        } catch (Exception e) {
            log.warn("Failed to publish chat event: {}", e.getMessage());
        }
    }

    private static String truncate(String text, int maxLen) {
        if (text == null) return "";
        return text.length() > maxLen ? text.substring(0, maxLen) + "..." : text;
    }
}
