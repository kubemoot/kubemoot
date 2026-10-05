package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.JetStreamSubscription;
import io.nats.client.Message;
import io.nats.client.PullSubscribeOptions;
import io.nats.client.api.ConsumerConfiguration;
import io.quarkus.runtime.ShutdownEvent;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * FIFO discussion request consumer via NATS JetStream pull subscription.
 *
 * Activated only on coordinator agents (KUBEMOOT_DISCUSS_COORDINATOR=true).
 * Pulls one request at a time from the KUBEMOOT_REQUEST stream, processes it
 * via the DiscussionOrchestrator, acks on completion, then pulls the next.
 *
 * Different crews have separate consumers (request-{namespace}-{crew}, see
 * {@link CrewScope#requestConsumer()}), so crews on different GPUs, and the same crew
 * name in different namespaces, process in parallel while requests within one crew
 * are serialized.
 */
@ApplicationScoped
public class RequestQueueConsumer {

    private static final Logger log = LoggerFactory.getLogger(RequestQueueConsumer.class);
    private static final ObjectMapper mapper = new ObjectMapper();
    private static final String STREAM_NAME = "KUBEMOOT_REQUEST";
    private static final Duration POLL_TIMEOUT = Duration.ofSeconds(30);
    // Bounded ackWait: the orchestrator's watchdog force-closes a stuck discussion
    // within its hard ceiling (~7 min), so orchestrate() always returns well under
    // this. A bound (not the old 30 min) means that even if a slot is somehow held -
    // a lost ack, a hard JVM stall - the message redelivers in minutes and the crew
    // self-heals, instead of wedging for half an hour. See [[Discussion Request Pump Can Wedge a Crew]].
    private static final Duration ACK_WAIT = Duration.ofMinutes(10);
    // Confirmed-ack timeout: ackSync waits this long for the server to confirm, so a
    // dropped ack can't silently leave the (maxAckPending=1) slot occupied.
    private static final Duration ACK_CONFIRM_TIMEOUT = Duration.ofSeconds(5);
    // A request that keeps failing is dead-lettered after this many deliveries so one
    // poison request can never block the crew's FIFO queue.
    private static final int MAX_DELIVER = 3;
    // Backoff before recreating the subscription after a consume-loop error.
    private static final long ERROR_RETRY_BACKOFF_MS = 10_000;

    private final NatsConnectionProvider natsProvider;
    private final DiscussionOrchestrator orchestrator;
    private final boolean coordinator;
    private final String crew;

    private final AtomicBoolean shutdown = new AtomicBoolean(false);
    private Thread consumerThread;

    public RequestQueueConsumer(
            NatsConnectionProvider natsProvider,
            DiscussionOrchestrator orchestrator,
            AgentProperties properties
    ) {
        this.natsProvider = natsProvider;
        this.orchestrator = orchestrator;
        this.coordinator = properties.discuss().coordinator();
        this.crew = properties.crew().orElse(null);
    }

    void onStart(@Observes StartupEvent event) {
        if (!coordinator) {
            log.debug("Not a coordinator — request queue consumer disabled");
            return;
        }
        if (crew == null || crew.isEmpty()) {
            log.info("No crew configured — request queue consumer disabled");
            return;
        }
        if (!natsProvider.isConfigured()) {
            log.info("NATS not configured — request queue consumer disabled");
            return;
        }

        CrewScope scope = natsProvider.scope();
        consumerThread = new Thread(this::consumeLoop, "request-queue-" + crew);
        consumerThread.setDaemon(true);
        consumerThread.start();
        log.info("Request queue consumer starting: crew={}, consumer={}", crew, scope.requestConsumer());
    }

    void onShutdown(@Observes ShutdownEvent event) {
        shutdown.set(true);
        if (consumerThread != null) {
            consumerThread.interrupt();
        }
    }

    private void consumeLoop() {
        JetStreamSubscription subscription = null;
        boolean interrupted = false;

        while (!shutdown.get() && !interrupted) {
            try {
                subscription = pollOnce(subscription);
            } catch (InterruptedException e) {
                interrupted = true;
            } catch (Exception e) {
                log.warn("Request queue error: {} - retrying in 10s", e.getMessage());
                subscription = null;
                interrupted = !backOff();
            }
        }
        if (interrupted) {
            Thread.currentThread().interrupt();
        }
        log.info("Request queue consumer stopped: crew={}", crew);
    }

    /** Fetch and process at most one message; returns the subscription to reuse, or null when none could be created. */
    private JetStreamSubscription pollOnce(JetStreamSubscription current) throws Exception {
        JetStreamSubscription subscription = current;
        if (subscription == null) {
            subscription = createSubscription();
            if (subscription == null) {
                Thread.sleep(POLL_TIMEOUT.toMillis());
                return null;
            }
        }

        // fetch() issues a fresh pull and collects up to the batch within the
        // timeout, returning a finite list. It replaces the hand-rolled
        // pull(1)+nextMessage pattern, which could stall after the first
        // message and silently stop delivering (observed wedge).
        // See [[Discussion Request Pump Can Wedge a Crew]].
        for (Message msg : subscription.fetch(1, POLL_TIMEOUT)) {
            processMessage(msg);
        }
        return subscription;
    }

    /** Sleep out the error backoff; false when interrupted. */
    private boolean backOff() {
        try {
            Thread.sleep(ERROR_RETRY_BACKOFF_MS);
            return true;
        } catch (InterruptedException ie) {
            return false;
        }
    }

    private JetStreamSubscription createSubscription() {
        Connection conn = natsProvider.getConnection();
        if (conn == null) return null;

        CrewScope scope = natsProvider.scope();
        String consumerName = scope.requestConsumer();
        String filterSubject = scope.requestSubject();

        try {
            // Reset any pre-existing durable consumer left over from a previous pod
            // instance. With ackWait=30m and maxAckPending=1, a previous pod that
            // died mid-message would otherwise block all redeliveries for up to
            // 30 minutes — every coordinator pod restart caused the queue to
            // wedge. Workqueue retention guarantees the messages persist in the
            // stream until acked, not until the consumer is deleted, so resetting
            // the consumer position is safe.
            resetConsumerIfPresent(conn.jetStreamManagement(), STREAM_NAME, consumerName);

            ConsumerConfiguration cc = ConsumerConfiguration.builder()
                    .durable(consumerName)
                    .filterSubject(filterSubject)
                    .ackWait(ACK_WAIT)
                    .maxAckPending(1)
                    .maxDeliver(MAX_DELIVER)
                    .maxPullWaiting(512)
                    .build();

            PullSubscribeOptions opts = PullSubscribeOptions.builder()
                    .stream(STREAM_NAME)
                    .configuration(cc)
                    .build();

            JetStreamSubscription sub = conn.jetStream().subscribe(filterSubject, opts);
            log.info("Subscribed to request queue: stream={}, consumer={}, filter={}",
                    STREAM_NAME, consumerName, filterSubject);
            return sub;
        } catch (JetStreamApiException e) {
            if (e.getMessage() != null && e.getMessage().contains("cannot be modified")) {
                log.info("Consumer config changed — deleting and recreating: {}", consumerName);
                try {
                    conn.jetStreamManagement().deleteConsumer(STREAM_NAME, consumerName);
                    return createSubscription(); // retry with fresh consumer
                } catch (Exception ex) {
                    log.warn("Failed to recreate consumer: {}", ex.getMessage());
                }
            } else {
                log.warn("Failed to create request queue subscription: {}", e.getMessage());
            }
            return null;
        } catch (IOException e) {
            log.warn("Failed to create request queue subscription: {}", e.getMessage());
            return null;
        }
    }

    // Visible for testing.
    // Deletes the named durable consumer if it exists. Used on startup to
    // clear stale ack-pending state left by a previous pod instance.
    // - 10014 = "consumer not found" — fine, first-time startup
    // - any other JetStreamApiException is logged but not propagated; the
    //   subsequent subscribe call will surface a clearer error if the actual
    //   reset failed
    static void resetConsumerIfPresent(JetStreamManagement jsm, String stream, String consumerName) {
        try {
            jsm.deleteConsumer(stream, consumerName);
            log.info("Reset pre-existing consumer for clean startup: {}", consumerName);
        } catch (JetStreamApiException e) {
            if (e.getApiErrorCode() == 10014) {
                log.debug("No pre-existing consumer to reset: {}", consumerName);
            } else {
                log.warn("Failed to reset consumer {} (apiErrorCode={}): {}",
                        consumerName, e.getApiErrorCode(), e.getMessage());
            }
        } catch (IOException e) {
            log.warn("Failed to reset consumer {}: {}", consumerName, e.getMessage());
        }
    }

    // Visible for testing.
    void processMessage(Message msg) {
        RequestMessage request;
        try {
            request = parseRequest(msg.getData());
        } catch (Exception e) {
            // An unparseable message will NEVER parse on redelivery. Terminate it
            // (dead-letter) so it cannot block the FIFO queue or redeliver forever.
            log.error("Discarding unparseable request (term, delivery {}): {}",
                    deliveredCount(msg), e.getMessage());
            safeTerm(msg);
            return;
        }

        try {
            log.info("Processing queued request: conversationId={}, crew={}",
                    request.conversationId(), request.crew());

            // orchestrate() is bounded by the orchestrator's watchdog: it always
            // returns within the hard ceiling (force-closing a stuck discussion),
            // so the consumer thread can never block here indefinitely.
            var result = orchestrator.orchestrate(request.message(), request.conversationId(), request.crew());
            // An interrupted orchestrate() returns null; one that finished returns a result
            // and is acked even when shutdown began meanwhile, so it is not answered twice.
            if (result == null && stopping()) {
                release(msg, request.conversationId());
                return;
            }
            ackConfirmed(msg);

            log.info("Completed queued request: conversationId={}", request.conversationId());
        } catch (Exception e) {
            log.error("Failed to process queued request: conversationId={}, error={}",
                    request.conversationId(), e.getMessage());
            // Bounded redelivery: a request that keeps failing is dead-lettered so one
            // poison request can't wedge the crew. A normal transient first failure
            // (delivery 1..MAX_DELIVER-1) is left to redeliver via ackWait. When the
            // delivery count is UNKNOWN (0, from a metadata error) we also dead-letter:
            // orchestrate has already failed and we cannot count retries, so terminating
            // is safer than risking a redelivery loop (the server-side maxDeliver is the
            // ultimate cap, but don't rely on it alone).
            long delivered = deliveredCount(msg);
            if (delivered == 0 || delivered >= MAX_DELIVER) {
                log.error("Request dead-lettered (term, delivery={}): conversationId={}",
                        delivered, request.conversationId());
                safeTerm(msg);
            }
        }
    }

    // Confirmed ack: wait for the server to acknowledge so a dropped ack can't leave
    // the maxAckPending=1 slot occupied (which would stall the queue until ackWait).
    // Falls back to a best-effort async ack if the sync confirmation itself fails.
    // Visible for testing.
    static void ackConfirmed(Message msg) {
        try {
            msg.ackSync(ACK_CONFIRM_TIMEOUT);
        } catch (Exception e) {
            log.warn("ackSync failed ({}) - falling back to async ack", e.getMessage());
            try {
                msg.ack();
            } catch (Exception ex) {
                log.warn("async ack also failed: {}", ex.getMessage());
            }
        }
    }

    // True when this pod is shutting down: orchestrate() returned because the shutdown
    // interrupted it, not because the discussion finished.
    private boolean stopping() {
        return shutdown.get() || Thread.currentThread().isInterrupted();
    }

    // Hands an unfinished request back to the queue so the coordinator that replaces
    // this pod takes it at once. Acking it here would drop the question: its thread
    // never closes and the asker waits for an answer that no coordinator is writing.
    private static void release(Message msg, String conversationId) {
        log.info("Shutting down mid-discussion; returning request to the queue: conversationId={}", conversationId);
        try {
            msg.nak();
        } catch (Exception e) {
            log.warn("Failed to return the request to the queue ({}); it redelivers after ackWait", e.getMessage());
        }
    }

    private static void safeTerm(Message msg) {
        try {
            msg.term();
        } catch (Exception e) {
            log.warn("Failed to term poison message: {}", e.getMessage());
        }
    }

    // Visible for testing.
    static long deliveredCount(Message msg) {
        try {
            return msg.metaData().deliveredCount();
        } catch (Exception e) {
            return 0L;
        }
    }

    // Visible for testing
    static RequestMessage parseRequest(byte[] data) throws IOException {
        JsonNode node = mapper.readTree(data);
        return new RequestMessage(
                node.path("message").asText(),
                node.path("conversationId").asText(),
                node.path("crew").asText(),
                node.path("timestamp").asText(),
                node.path("use_rag").asBoolean(true),
                node.path("use_tools").asBoolean(true)
        );
    }

    record RequestMessage(
            String message,
            String conversationId,
            String crew,
            String timestamp,
            boolean useRag,
            boolean useTools
    ) {}
}
