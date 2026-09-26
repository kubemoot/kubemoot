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
import java.util.*;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

/**
 * Onboarding Agent subscriber — coordinator-triggered gap detection.
 *
 * Gap detection is now handled by the coordinator's LLM, which naturally
 * suggests onboarding when no tooler can help. This subscriber:
 * 1. Listens for gap_detected signals from the coordinator
 * 2. Handles "onboard <domain>" consent from user replies
 * 3. Deploys MCPServer CRs on consent
 *
 * Active only when kubemoot.onboarding.mode=true (set via annotation on the Agent CR).
 */
@ApplicationScoped
public class OnboardingSubscriber {

    private static final Logger log = LoggerFactory.getLogger(OnboardingSubscriber.class);
    private static final ObjectMapper mapper = new ObjectMapper();
    private static final String DISCUSS_PREFIX = "kubemoot.discuss.";

    // JSON field name constants (used in NATS message construction/parsing)
    private static final String FIELD_MESSAGE_ID = "messageId";
    private static final String FIELD_THREAD_ID = "threadId";
    private static final String FIELD_AGENT_NAME = "agentName";
    private static final String FIELD_MESSAGE_TYPE = "messageType";
    private static final String FIELD_CONTENT = "content";
    private static final String FIELD_CHANNEL = "channel";
    private static final String FIELD_TIMESTAMP = "timestamp";
    private static final String FIELD_METADATA = "metadata";
    private static final String CHANNEL_GENERAL = "general";

    // Dedup-set size cap before it is cleared wholesale to bound memory.
    private static final int MAX_PROCESSED_MESSAGES = 2000;
    // Period (minutes) between proposal/dedup cleanup sweeps.
    private static final int CLEANUP_INTERVAL_MINUTES = 5;
    // Max chars of the onboarding proposal content published inline.
    private static final int PROPOSAL_CONTENT_CHARS = 5000;

    // Shared doc-URL hint appended to the onboarding prompt when the user supplies
    // documentation links; %s is the comma-separated URL list.
    private static final String DOC_URL_HINT_FORMAT =
            " The user provided documentation URLs: %s. " +
            "Add annotation kubemoot.ai/doc-urls with these URLs (comma-separated) to the MCPServer CR.";

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;
    private final AgentProperties properties;
    private final List<String> channels;
    private final GapDetector gapDetector;
    private final boolean onboardingMode;
    private final ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(1);

    // Dedup
    private final Set<String> processedMessages = ConcurrentHashMap.newKeySet();
    // Store consent until advisory_ready arrives (coordinator clears signals during advisory phase)
    private record PendingConsent(String subject, String userQuery) {}
    private final ConcurrentHashMap<String, PendingConsent> pendingConsents = new ConcurrentHashMap<>();

    public OnboardingSubscriber(
            NatsConnectionProvider natsProvider,
            ChatService chatService,
            AgentProperties properties
    ) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
        this.properties = properties;
        this.onboardingMode = properties.onboarding().mode();
        this.gapDetector = new GapDetector();

        String channelsConfig = properties.discuss().channels().orElse("");
        if (!channelsConfig.isEmpty()) {
            this.channels = Arrays.asList(channelsConfig.split(","));
        } else {
            this.channels = List.of();
        }
    }

    void onStart(@Observes StartupEvent event) {
        if (!canActivate()) return;

        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            log.warn("Could not connect to NATS — onboarding subscriber disabled");
            return;
        }

        Dispatcher dispatcher = conn.createDispatcher();
        String crew = properties.crew().orElse(null);

        subscribeBroadcast(dispatcher, crew);
        subscribeChannels(dispatcher, crew);

        scheduler.scheduleAtFixedRate(() -> {
            gapDetector.cleanup();
            if (processedMessages.size() > MAX_PROCESSED_MESSAGES) processedMessages.clear();
        }, CLEANUP_INTERVAL_MINUTES, CLEANUP_INTERVAL_MINUTES, TimeUnit.MINUTES);

        log.info("Onboarding subscriber active on {} channels", channels.size());
    }

    private boolean canActivate() {
        if (!onboardingMode) {
            log.info("Onboarding mode disabled — onboarding subscriber inactive");
            return false;
        }
        if (channels.isEmpty()) {
            log.info("No discussion channels configured — onboarding subscriber inactive");
            return false;
        }
        if (!natsProvider.isConfigured()) {
            log.info("NATS not configured — onboarding subscriber disabled");
            return false;
        }
        return true;
    }

    private String crewSubjectPrefix(String crew) {
        return (crew != null && !crew.isEmpty()) ? DISCUSS_PREFIX + crew + "." : DISCUSS_PREFIX;
    }

    private void subscribeBroadcast(Dispatcher dispatcher, String crew) {
        String broadcastSubject = crewSubjectPrefix(crew) + "broadcast.>";
        dispatcher.subscribe(broadcastSubject, msg -> {
            try {
                handleMessage(msg.getSubject(), new String(msg.getData()));
            } catch (Exception e) {
                log.warn("Error handling broadcast message on {}: {}", msg.getSubject(), e.getMessage());
            }
        });
    }

    private void subscribeChannels(Dispatcher dispatcher, String crew) {
        String prefix = crewSubjectPrefix(crew);
        for (String channel : channels) {
            String subject = prefix + channel.trim() + ".>";
            dispatcher.subscribe(subject, msg -> {
                try {
                    handleMessage(msg.getSubject(), new String(msg.getData()));
                } catch (Exception e) {
                    log.warn("Error handling onboarding message on {}: {}", msg.getSubject(), e.getMessage());
                }
            });
        }
    }

    private void handleMessage(String subject, String data) {
        try {
            var msg = mapper.readTree(data);
            String messageType = msg.has(FIELD_MESSAGE_TYPE) ? msg.get(FIELD_MESSAGE_TYPE).asText() : "";
            String threadId = msg.has(FIELD_THREAD_ID) ? msg.get(FIELD_THREAD_ID).asText() : "";
            String agentName = msg.has(FIELD_AGENT_NAME) ? msg.get(FIELD_AGENT_NAME).asText() : "";
            String messageId = msg.has(FIELD_MESSAGE_ID) ? msg.get(FIELD_MESSAGE_ID).asText() : "";
            String content = msg.has(FIELD_CONTENT) ? msg.get(FIELD_CONTENT).asText() : "";

            // Ignore own messages
            if (properties.agentName().equals(agentName)) return;

            // Dedup
            if (!messageId.isEmpty() && !processedMessages.add(messageId)) return;

            switch (messageType) {
                // Coordinator signals gap after LLM assessment
                case "gap_detected" -> handleGapDetected(subject, threadId, msg);
                // User consent: "onboard <domain>" — defer until advisory_ready
                case "thread_start", "reply" -> handleConsentCandidate(subject, threadId, messageType, content, msg);
                // Advisory ready: now safe to publish agree signals
                case "advisory_ready" -> handleAdvisoryReady(threadId);
                default -> {
                    // Other message types are not onboarding-relevant; ignore.
                }
            }
        } catch (Exception e) {
            log.warn("Failed to handle onboarding message: {}", e.getMessage());
        }
    }

    private void handleGapDetected(String subject, String threadId,
                                   com.fasterxml.jackson.databind.JsonNode msg) {
        String userQuery = extractUserQuery(msg);
        if (userQuery.isEmpty()) return;

        String channel = msg.has(FIELD_CHANNEL) ? msg.get(FIELD_CHANNEL).asText() : CHANNEL_GENERAL;

        // Extract tool-gap concerns from metadata
        Map<String, String> concerns = extractConcerns(msg);

        String reason = msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has("reason")
                ? msg.get(FIELD_METADATA).get("reason").asText() : "";
        log.info("Coordinator signaled gap for thread {} (reason: {}) — triggering onboarding",
                threadId, reason);
        handleGap(subject, threadId, userQuery, channel, concerns);
    }

    private Map<String, String> extractConcerns(com.fasterxml.jackson.databind.JsonNode msg) {
        Map<String, String> concerns = new LinkedHashMap<>();
        if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has("toolGapConcerns")) {
            var concernsNode = msg.get(FIELD_METADATA).get("toolGapConcerns");
            concernsNode.fields().forEachRemaining(
                    entry -> concerns.put(entry.getKey(), entry.getValue().asText()));
        }
        return concerns;
    }

    private void handleConsentCandidate(String subject, String threadId, String messageType,
                                        String content, com.fasterxml.jackson.databind.JsonNode msg) {
        String userQuery = "thread_start".equals(messageType) ? extractUserQuery(msg) : content;
        if (userQuery.isEmpty()) return;
        if (isOnboardConsent(userQuery)) {
            // Store consent and wait for advisory_ready before publishing agree.
            // The coordinator clears all signals when advisory_ready is published,
            // so signals sent during the ADVISORY phase get wiped.
            pendingConsents.put(threadId, new PendingConsent(subject, userQuery));
            log.info("Stored pending consent for thread {}: {}", threadId, userQuery);
        }
    }

    private void handleAdvisoryReady(String threadId) {
        var consent = pendingConsents.remove(threadId);
        if (consent != null) {
            log.info("Advisory ready for consent thread {} — now handling consent", threadId);
            handleOnboardConsent(consent.subject(), threadId, consent.userQuery());
        }
    }

    private boolean isOnboardConsent(String query) {
        String lower = query.toLowerCase();
        return lower.startsWith("onboard ") || lower.startsWith("yes, onboard");
    }

    private void handleGap(String subject, String threadId, String userQuery, String channel,
                           Map<String, String> concerns) {
        String prompt;
        if (concerns != null && !concerns.isEmpty()) {
            String toolNeeds = String.join("; ", concerns.values());
            log.info("Tool gap detected for thread {} — toolers need: {}", threadId, toolNeeds);
            prompt = String.format(
                    "TOOL GAP DETECTED. Existing toolers recognized this query but need additional tools. " +
                    "User asked: \"%s\" (channel: %s). " +
                    "Toolers reported these needs: %s. " +
                    "Search MCP registries for a server that provides these capabilities. " +
                    "If you find one, propose it to the user with consent instructions. " +
                    "Include the credential requirements if any. " +
                    "Tip: tell the user they can include documentation links for a smarter tooler, like: " +
                    "'onboard <domain> https://docs.example.com https://github.com/org/repo'",
                    userQuery, channel, toolNeeds);
        } else {
            log.info("Tooler gap detected for thread {} — no tooler could handle: {}", threadId, userQuery);
            prompt = String.format(
                    "CAPABILITY GAP DETECTED. The user asked: \"%s\" (channel: %s). " +
                    "No tooler agent could directly answer this question. " +
                    "Search MCP registries for a server that could handle this request. " +
                    "If you find one, propose it to the user with consent instructions. " +
                    "Include the credential requirements if any. " +
                    "Tip: tell the user they can include documentation links for a smarter tooler, like: " +
                    "'onboard <domain> https://docs.example.com https://github.com/org/repo'",
                    userQuery, channel);
        }

        contributeToThread(subject, threadId, prompt);
    }

    private void handleOnboardConsent(String subject, String threadId, String userQuery) {
        log.info("Onboarding consent detected in thread {}: {}", threadId, userQuery);

        // Parse: "onboard <domain> [url1 url2 ...]"
        String stripped = userQuery
                .replaceAll("(?i)^(yes,?\\s*)?onboard\\s+", "")
                .trim();

        String[] tokens = stripped.split("\\s+");
        String domain = tokens.length > 0 ? tokens[0].toLowerCase() : "";

        // Extract URLs
        List<String> docUrls = new ArrayList<>();
        String[] originalTokens = userQuery
                .replaceAll("(?i)^(yes,?\\s*)?onboard\\s+", "")
                .trim()
                .split("\\s+");
        for (int i = 1; i < originalTokens.length; i++) {
            if (looksLikeUrl(originalTokens[i])) {
                docUrls.add(originalTokens[i]);
            }
        }

        // Publish progress
        publishProgress(domain, "deploying",
                "Setting up a tooler for " + domain + ". This will take a few minutes.");

        var proposal = gapDetector.findProposal(domain);
        String prompt;
        if (proposal != null) {
            List<String> allDocUrls = new ArrayList<>(docUrls);
            if (proposal.docUrls() != null) {
                allDocUrls.addAll(proposal.docUrls());
            }
            String docUrlHint = allDocUrls.isEmpty() ? "" : String.format(
                    DOC_URL_HINT_FORMAT, String.join(", ", allDocUrls));

            prompt = String.format(
                    "The user has approved onboarding '%s'. " +
                    "Previously proposed server: %s from %s. " +
                    "Create the MCPServer CR now with labels: kubemoot.ai/onboarded=true, kubemoot.ai/domain=%s. " +
                    "Use transport: stdio.%s",
                    domain, proposal.serverName(), proposal.registryUrl(), domain, docUrlHint);
            gapDetector.removeProposal(domain);
        } else {
            String docUrlHint = docUrls.isEmpty() ? "" : String.format(
                    DOC_URL_HINT_FORMAT, String.join(", ", docUrls));

            prompt = String.format(
                    "The user wants to onboard '%s'. Search registries for an MCP server for this domain, " +
                    "evaluate quality, and create the MCPServer CR with labels: kubemoot.ai/onboarded=true, " +
                    "kubemoot.ai/domain=%s. Use transport: stdio.%s",
                    domain, domain, docUrlHint);
        }

        // Publish consent acknowledgement as agree signal so coordinator returns it to user
        String progressContent = "Setting up a tooler for " + domain + ".";
        if (!docUrls.isEmpty()) {
            progressContent += " Indexing documentation from " + docUrls.size() + " provided link(s) — this will take a few minutes.";
        } else {
            progressContent += " Searching for documentation — this may take a few minutes.";
        }
        publishAgreeSignal(subject, threadId, progressContent);

        // Deploy asynchronously — contributeToThread makes LLM calls
        contributeToThread(subject, threadId, prompt);
    }

    /**
     * Run the onboarding LLM call, returning its text or {@code null} when the call
     * fails. Extracted so {@link #contributeToThread} has no nested try.
     */
    private String onboardingLlmCall(String threadId, String prompt) {
        try {
            return chatService.simpleLlmCall(null, prompt);
        } catch (Exception e) {
            log.warn("Onboarding LLM call failed for thread {}: {}", threadId, e.getMessage());
            return null;
        }
    }

    private void contributeToThread(String originalSubject, String threadId, String prompt) {
        try {
            String content = onboardingLlmCall(threadId, prompt);
            if (content == null) return; // call failed; already logged in onboardingLlmCall

            if (content.isEmpty()) {
                log.warn("Empty response from onboarding LLM for thread {}", threadId);
                return;
            }

            var conn = natsProvider.getConnection();
            if (conn == null) return;

            String[] parts = originalSubject.split("\\.");
            String channel = parts.length > 2 ? parts[2] : CHANNEL_GENERAL;

            var proposal = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, "proposal",
                    FIELD_CONTENT, truncate(content, PROPOSAL_CONTENT_CHARS),
                    FIELD_CHANNEL, channel,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, Map.of(
                            "onboarding", true,
                            "gapDetected", true
                    )
            );

            String crewId = properties.crew().orElse(null);
            String publishSubject = (crewId != null && !crewId.isEmpty())
                    ? DISCUSS_PREFIX + crewId + "." + channel + "." + threadId
                    : DISCUSS_PREFIX + channel + "." + threadId;
            conn.publish(publishSubject, mapper.writeValueAsBytes(proposal));
            log.info("Published onboarding proposal to {} for thread {}", publishSubject, threadId);

        } catch (Exception e) {
            log.warn("Failed to contribute onboarding response: {}", e.getMessage());
        }
    }

    private void publishAgreeSignal(String originalSubject, String threadId, String content) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            String[] parts = originalSubject.split("\\.");
            String channel = parts.length > 2 ? parts[2] : CHANNEL_GENERAL;

            var message = Map.of(
                    FIELD_MESSAGE_ID, UUID.randomUUID().toString(),
                    FIELD_THREAD_ID, threadId,
                    FIELD_AGENT_NAME, properties.agentName(),
                    FIELD_MESSAGE_TYPE, "agree",
                    FIELD_CONTENT, content,
                    FIELD_CHANNEL, channel,
                    FIELD_TIMESTAMP, Instant.now().toString(),
                    FIELD_METADATA, Map.of("onboarding", true, "consentAck", true)
            );

            String crewId = properties.crew().orElse(null);
            String publishSubject = (crewId != null && !crewId.isEmpty())
                    ? DISCUSS_PREFIX + crewId + "." + channel + "." + threadId
                    : DISCUSS_PREFIX + channel + "." + threadId;
            conn.publish(publishSubject, mapper.writeValueAsBytes(message));
            log.info("Published onboarding agree signal to {} for thread {}", publishSubject, threadId);
        } catch (Exception e) {
            log.warn("Failed to publish agree signal: {}", e.getMessage());
        }
    }

    private void publishProgress(String domain, String stage, String message) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var event = Map.of(
                    "domain", domain,
                    "stage", stage,
                    "message", message,
                    FIELD_TIMESTAMP, Instant.now().toString()
            );

            conn.publish("kubemoot.operator.onboarding.progress",
                    mapper.writeValueAsBytes(event));
        } catch (Exception e) {
            log.debug("Failed to publish progress event: {}", e.getMessage());
        }
    }

    private String extractUserQuery(com.fasterxml.jackson.databind.JsonNode msg) {
        if (msg.has(FIELD_METADATA) && msg.get(FIELD_METADATA).has("userQuery")) {
            return msg.get(FIELD_METADATA).get("userQuery").asText();
        }
        return msg.has(FIELD_CONTENT) ? msg.get(FIELD_CONTENT).asText() : "";
    }

    private static boolean looksLikeUrl(String token) {
        return token.startsWith("http://") || token.startsWith("https://");
    }

    private static String truncate(String text, int maxLen) {
        if (text == null) return "";
        return text.length() > maxLen ? text.substring(0, maxLen) + "..." : text;
    }
}
