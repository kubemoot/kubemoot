package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

/**
 * RTFM Agent subscriber: listens for onboarding deployment events and
 * auto-creates RAGSource CRs by finding documentation for the onboarded tool.
 *
 * Active only when kubemoot.rtfm.mode=true (set via annotation on the Agent CR).
 */
@ApplicationScoped
public class RtfmSubscriber {

    private static final Logger log = LoggerFactory.getLogger(RtfmSubscriber.class);
    private static final ObjectMapper mapper = new ObjectMapper();

    private static final String NATS_ONBOARDING_PREFIX = "kubemoot.operator.onboarding.";
    private static final String FIELD_DOMAIN = "domain";
    private static final String FIELD_TIMESTAMP = "timestamp";
    private static final String FIELD_SERVER_NAME = "serverName";
    private static final String FIELD_DOC_URLS = "docUrls";

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;
    private final AgentProperties properties;
    private final boolean rtfmMode;

    public RtfmSubscriber(
            NatsConnectionProvider natsProvider,
            ChatService chatService,
            AgentProperties properties
    ) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
        this.properties = properties;
        this.rtfmMode = properties.rtfm().mode();
    }

    void onStart(@Observes StartupEvent event) {
        if (!rtfmMode) {
            log.info("RTFM mode disabled — RTFM subscriber inactive");
            return;
        }

        if (!natsProvider.isConfigured()) {
            log.info("NATS not configured — RTFM subscriber disabled");
            return;
        }

        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            log.warn("Could not connect to NATS — RTFM subscriber disabled");
            return;
        }

        Dispatcher dispatcher = conn.createDispatcher();

        // Listen for onboarding deployment events
        String subject = NATS_ONBOARDING_PREFIX + "deployed";
        dispatcher.subscribe(subject, msg -> {
            try {
                handleDeploymentEvent(new String(msg.getData()));
            } catch (Exception e) {
                log.warn("Error handling RTFM event: {}", e.getMessage());
            }
        });

        log.info("RTFM subscriber active — listening for onboarding deployments on {}", subject);
    }

    private void handleDeploymentEvent(String data) {
        try {
            var event = mapper.readTree(data);
            String serverName = event.has(FIELD_SERVER_NAME) ? event.get(FIELD_SERVER_NAME).asText() : "";
            String domain = event.has(FIELD_DOMAIN) ? event.get(FIELD_DOMAIN).asText() : "";
            String githubUrl = event.has("githubUrl") ? event.get("githubUrl").asText() : "";

            // Read user-provided doc URLs from the event
            List<String> docUrls = extractDocUrls(event);

            if (serverName.isEmpty() && domain.isEmpty()) {
                log.warn("RTFM event missing serverName and domain");
                return;
            }

            log.info("RTFM: Processing newly onboarded server '{}' (domain: {}, docUrls: {})",
                    serverName, domain, docUrls.size());

            // Publish progress: indexing-docs stage
            publishProgress(domain, "indexing-docs",
                    "Indexing documentation for " + domain + (docUrls.isEmpty() ? " (discovering docs via LLM)" : " from " + docUrls.size() + " provided link(s)"));

            String prompt = buildPrompt(serverName, domain, githubUrl, docUrls);

            var request = new ChatService.ChatRequest("rtfm-" + serverName, prompt);
            var result = chatService.directChat(request);

            handleChatResult(result, serverName, domain);
        } catch (Exception e) {
            log.warn("RTFM processing failed: {}", e.getMessage());
        }
    }

    private List<String> extractDocUrls(com.fasterxml.jackson.databind.JsonNode event) {
        List<String> docUrls = new ArrayList<>();
        if (event.has(FIELD_DOC_URLS) && event.get(FIELD_DOC_URLS).isArray()) {
            for (var urlNode : event.get(FIELD_DOC_URLS)) {
                String url = urlNode.asText();
                if (url != null && !url.isEmpty()) {
                    docUrls.add(url);
                }
            }
        }
        return docUrls;
    }

    private String buildPrompt(String serverName, String domain, String githubUrl, List<String> docUrls) {
        if (!docUrls.isEmpty()) {
            // User provided doc URLs — use them as primary sources
            return buildDocUrlPrompt(serverName, domain, docUrls);
        }
        // No doc URLs — fall back to LLM discovery
        return buildDiscoveryPrompt(serverName, domain, githubUrl);
    }

    private void handleChatResult(ChatService.ChatResult result, String serverName, String domain) {
        if (result != null && result.response() != null) {
            String resultText = result.response();
            log.info("RTFM completed for '{}': {}", serverName,
                    resultText.substring(0, Math.min(resultText.length(), 200)));

            // Publish completion events
            publishEvent(serverName, domain, resultText);
            publishProgress(domain, "ready",
                    "Documentation indexed for " + domain + ". Tooler agent is ready.");
        }
    }

    private String buildDocUrlPrompt(String serverName, String domain, List<String> docUrls) {
        StringBuilder urlList = new StringBuilder();
        for (String url : docUrls) {
            urlList.append("- ").append(url).append("\n");
        }

        return String.format(
                "A new MCP server '%s' has been onboarded for the '%s' domain. " +
                "The user provided these documentation URLs:\n%s\n" +
                "Create RAGSource CR(s) for these documentation sources. " +
                "Requirements: " +
                "- Collection name must use underscores (e.g., %s_reference), never hyphens " +
                "- For GitHub URLs, extract the repo and docs path to create a git-type RAGSource " +
                "- For web URLs, use the fetch-mcp tool to verify the URL is accessible, then create a web-type RAGSource " +
                "- Use namespace: kubemoot " +
                "- Use plain directory paths, never glob patterns " +
                "- One RAGSource per domain (combine multiple URLs into paths if from same repo)",
                serverName, domain, urlList, domain.replace("-", "_"));
    }

    private String buildDiscoveryPrompt(String serverName, String domain, String githubUrl) {
        return String.format(
                "A new MCP server '%s' has been onboarded for the '%s' domain. " +
                "%s" +
                "Find documentation for this tool and create a RAGSource CR. " +
                "Requirements: " +
                "- Collection name must use underscores (e.g., %s_reference), never hyphens " +
                "- Verify the docs directory exists before creating the CR " +
                "- Use namespace: kubemoot " +
                "- Use plain directory paths, never glob patterns " +
                "- If no documentation found, report that clearly",
                serverName, domain,
                githubUrl.isEmpty() ? "" : String.format("GitHub repo: %s. ", githubUrl),
                domain.replace("-", "_"));
    }

    private void publishEvent(String serverName, String domain, String result) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var event = Map.of(
                    FIELD_SERVER_NAME, serverName,
                    FIELD_DOMAIN, domain,
                    "agent", properties.agentName(),
                    "result", result != null ? result.substring(0, Math.min(result.length(), 1000)) : "",
                    FIELD_TIMESTAMP, Instant.now().toString()
            );

            conn.publish(NATS_ONBOARDING_PREFIX + "docs-indexed",
                    mapper.writeValueAsBytes(event));
        } catch (Exception e) {
            log.debug("Failed to publish RTFM event: {}", e.getMessage());
        }
    }

    private void publishProgress(String domain, String stage, String message) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var event = Map.of(
                    FIELD_DOMAIN, domain,
                    "stage", stage,
                    "message", message,
                    FIELD_TIMESTAMP, Instant.now().toString()
            );

            conn.publish(NATS_ONBOARDING_PREFIX + "progress",
                    mapper.writeValueAsBytes(event));
        } catch (Exception e) {
            log.debug("Failed to publish progress event: {}", e.getMessage());
        }
    }
}
