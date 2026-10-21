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
 * kept (the NATS KV bucket every agent shares), which placement policy its configuration selects,
 * and whether placement runs in-process or asks the placement service.
 *
 * <p>The service is used only when the operator sets {@code KUBEMOOT_PLACEMENT_SERVICE_URL} on the
 * agent (the KubemootConfig flag, off by default). Without it, nothing here changes how the agent
 * places its calls.</p>
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

    /** Placement for this agent's calls: the placement service when it is configured, else in-process. */
    @Produces
    @ApplicationScoped
    CallPlacement callPlacement(CallPlanner local, PlacementClients clients, ServerSelector selector,
                                AgentIdentity identity, AgentProperties props, ObjectMapper mapper,
                                MeterRegistry registry) {
        if (clients.client().isEmpty()) {
            return local;
        }
        log.info("Placement runs in the placement service at {}", clients.url());
        return new RemoteCallPlanner(clients.client().get(), selector, identity,
                props.scheduler().placementPolicy(), mapper, registry);
    }

    /** The wake-up for a call waiting on capacity: the service's change counter, or the NATS watch in-process. */
    @Produces
    @ApplicationScoped
    CapacitySignal capacitySignal(NatsCapacityWatch local, PlacementClients clients, ObjectMapper mapper) {
        return clients.client().<CapacitySignal>map(c -> new RemoteCapacitySignal(c, mapper)).orElse(local);
    }
}
