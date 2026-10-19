package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.nats.client.Connection;
import io.nats.client.Message;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Locale;
import java.util.concurrent.CancellationException;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

/**
 * {@link ModelLifecycle} over NATS request and reply on
 * {@code kubemoot.provider.model.<provider>} (bus schemas ModelLifecycleRequest and
 * ModelLifecycleReply). The operator replies when the provider reports the model in the
 * state asked for, so this waits on that state. The timeout is a safety net that outlasts
 * the operator's own: the normal path ends on the reply.
 */
public final class NatsModelLifecycle implements ModelLifecycle {

    private static final Logger log = LoggerFactory.getLogger(NatsModelLifecycle.class);

    /** Subject prefix; the provider's {@link ModelKeys#token} follows. Same as operator bus.ModelLifecycleSubjectPrefix. */
    static final String SUBJECT_PREFIX = "kubemoot.provider.model.";

    /** Longer than the operator's safety limit (30 minutes), so the operator's answer arrives first. */
    static final Duration SAFETY_TIMEOUT = Duration.ofMinutes(31);

    /** How long past the request's own timeout the wait lasts before it gives up on NATS. */
    private static final long GRACE_MS = 5_000L;

    static final String NO_ANSWER = "the operator did not answer before the safety limit";
    static final String NOT_LISTENING = "no operator is listening for model lifecycle requests";

    private final NatsConnectionProvider nats;
    private final ObjectMapper mapper;
    private final String agent;
    private final Duration timeout;

    public NatsModelLifecycle(NatsConnectionProvider nats, ObjectMapper mapper, String agent) {
        this(nats, mapper, agent, SAFETY_TIMEOUT);
    }

    NatsModelLifecycle(NatsConnectionProvider nats, ObjectMapper mapper, String agent, Duration timeout) {
        this.nats = nats;
        this.mapper = mapper;
        this.agent = agent;
        this.timeout = timeout;
    }

    @Override
    public Reply ready(String provider, String model) {
        return request("ready", provider, model);
    }

    @Override
    public Reply release(String provider, String model) {
        return request("release", provider, model);
    }

    static String subjectFor(String provider) {
        return SUBJECT_PREFIX + ModelKeys.token(provider);
    }

    private Reply request(String action, String provider, String model) {
        Connection conn = nats == null ? null : nats.getConnection();
        if (conn == null) {
            return Reply.failure("NATS is not available, so the operator cannot be asked to " + action + " " + model);
        }
        ObjectNode body = mapper.createObjectNode().put("action", action).put("model", model);
        if (agent != null && !agent.isBlank()) {
            body.put("agent", agent);
        }
        CompletableFuture<Message> pending;
        try {
            // The future, not the blocking request: the blocking form returns null both
            // on a timeout and when NATS reports that nobody subscribes to the subject.
            pending = conn.requestWithTimeout(subjectFor(provider), mapper.writeValueAsBytes(body), timeout);
        } catch (Exception e) {
            log.debug("Lifecycle request {} {} on {} failed: {}", action, model, provider, e.toString());
            return Reply.failure(couldNotAsk(action, model, e.getMessage()));
        }
        return await(pending, action, model);
    }

    /**
     * Waits for the operator's reply. NATS ends the request itself at {@code timeout};
     * the wait lasts a little longer only so a request NATS never ends cannot hang.
     */
    Reply await(CompletableFuture<Message> pending, String action, String model) {
        try {
            return parse(pending.get(timeout.toMillis() + GRACE_MS, TimeUnit.MILLISECONDS));
        } catch (CancellationException e) {
            // NATS cancels a request when the server reports that nobody subscribes.
            return Reply.failure(NOT_LISTENING);
        } catch (TimeoutException e) {
            pending.cancel(true);
            return Reply.failure(NO_ANSWER);
        } catch (ExecutionException e) {
            return Reply.failure(reasonFor(e.getCause(), action, model));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return Reply.failure("interrupted while waiting for the operator to " + action + " " + model);
        }
    }

    /** Why a request ended without a reply, from the exception NATS completed it with. */
    static String reasonFor(Throwable cause, String action, String model) {
        if (cause instanceof TimeoutException) {
            return NO_ANSWER;
        }
        String text = cause == null || cause.getMessage() == null ? "no reason given" : cause.getMessage();
        if (text.toLowerCase(Locale.ROOT).contains("no responders")) {
            return NOT_LISTENING;
        }
        return couldNotAsk(action, model, text);
    }

    private static String couldNotAsk(String action, String model, String reason) {
        return "the operator could not be asked to " + action + " " + model + ": " + reason;
    }

    /** Reads the operator's reply by tree navigation (record deserialization fails in native images). */
    Reply parse(Message answer) {
        if (answer == null) {
            return Reply.failure(NO_ANSWER);
        }
        if (answer.isStatusMessage()) {
            return Reply.failure(NOT_LISTENING);
        }
        try {
            JsonNode n = mapper.readTree(new String(answer.getData(), StandardCharsets.UTF_8));
            return new Reply(n.path("ok").asBoolean(false), n.path("state").asText(""),
                    n.path("switchMs").asLong(0L), n.path("error").asText(""));
        } catch (Exception e) {
            return Reply.failure("the operator's reply could not be read: " + e.getMessage());
        }
    }
}
