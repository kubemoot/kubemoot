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
        try {
            Message answer = conn.request(subjectFor(provider), mapper.writeValueAsBytes(body), timeout);
            return parse(answer);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return Reply.failure("interrupted while waiting for the operator to " + action + " " + model);
        } catch (Exception e) {
            log.debug("Lifecycle request {} {} on {} failed: {}", action, model, provider, e.toString());
            return Reply.failure("the operator could not be asked to " + action + " " + model + ": " + e.getMessage());
        }
    }

    /** Reads the operator's reply by tree navigation (record deserialization fails in native images). */
    Reply parse(Message answer) {
        if (answer == null) {
            return Reply.failure("the operator did not answer before the safety limit");
        }
        if (answer.isStatusMessage()) {
            return Reply.failure("no operator is listening for model lifecycle requests");
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
