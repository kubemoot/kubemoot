package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.MeterRegistry;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.inject.Produces;
import jakarta.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Wires the placement code in placement-core to this agent: who the agent is, where tickets are
 * kept (the NATS KV bucket every agent shares), and which placement policy its configuration
 * selects.
 */
@ApplicationScoped
public class PlacementBeans {

    private static final Logger log = LoggerFactory.getLogger(PlacementBeans.class);

    /** The agent this process runs. */
    @Produces
    @Singleton
    AgentIdentity agentIdentity(AgentProperties props) {
        return new AgentIdentity(props.namespace().orElse(""), props.agentName(), props.crew().orElse(""));
    }

    /** Tickets live in the shared NATS KV bucket when this agent places in-process. */
    @Produces
    @ApplicationScoped
    TicketStore ticketStore(NatsConnectionProvider nats, ObjectMapper mapper) {
        return new KvTicketStore(nats, mapper);
    }

    /** The placement policy this agent's configuration selects, over the shared ticket manager. */
    @Produces
    @ApplicationScoped
    PlacementPolicy placementPolicy(ServerSelector selector, TicketManager tickets, DemandBoard demand,
                                    AgentProperties props, MeterRegistry registry) {
        PlacementPolicy policy = PlacementPolicies.create(props.scheduler().placementPolicy(),
                new AlgorithmPlacement(selector, tickets, demand), selector,
                PlacementPolicies.shadowRecorder(registry));
        log.info("Placement policy: {}", policy.mode().configName());
        return policy;
    }
}
