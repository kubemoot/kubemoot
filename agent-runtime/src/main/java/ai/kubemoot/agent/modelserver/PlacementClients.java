package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.time.Duration;
import java.util.Optional;

/**
 * The client of the placement service this agent calls, or none. One client is shared by placement and
 * the capacity wait so both see the same outage at once.
 */
@ApplicationScoped
public class PlacementClients {

    /** A placement call is a few milliseconds; this bounds a call to a service that has stopped answering. */
    static final Duration CONNECT_TIMEOUT = Duration.ofMillis(500);
    static final Duration REQUEST_TIMEOUT = Duration.ofSeconds(3);

    private final String url;
    private final PlacementClient client;

    @Inject
    public PlacementClients(@ConfigProperty(name = "placement-service.url") Optional<String> url,
                            ObjectMapper mapper) {
        this.url = url.map(String::trim).filter(s -> !s.isEmpty()).orElse(null);
        this.client = this.url == null ? null
                : new PlacementClient(this.url, mapper, CONNECT_TIMEOUT, REQUEST_TIMEOUT);
    }

    /** The client, when the operator configured the placement service for this agent. */
    public Optional<PlacementClient> client() {
        return Optional.ofNullable(client);
    }

    /** The service address, or null when placement is in-process. */
    public String url() {
        return url;
    }
}
