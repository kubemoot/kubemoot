package ai.kubemoot.placement;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.time.Duration;
import java.util.Optional;

/**
 * The service's settings. Empty strings are read as absent, so an unset {@code NATS_USER} is no user.
 *
 * <p>{@code leaseSeconds} must equal the TTL of the leader bucket the chart provisions: the bucket
 * removes a lease that is not renewed after that long, and a leader steps down after two thirds of
 * it, so the old leader has always stopped before a new one can take over.</p>
 */
@ApplicationScoped
public class ServiceSettings {

    private final Optional<String> natsUrl;
    private final Optional<String> natsUser;
    private final Optional<String> natsPassword;
    private final String holderId;
    private final Duration lease;
    private final Duration ticketTtl;
    private final String defaultPolicy;

    @Inject
    public ServiceSettings(@ConfigProperty(name = "kubemoot.placement.nats.url") Optional<String> natsUrl,
                           @ConfigProperty(name = "kubemoot.placement.nats.user") Optional<String> natsUser,
                           @ConfigProperty(name = "kubemoot.placement.nats.password") Optional<String> natsPassword,
                           @ConfigProperty(name = "kubemoot.placement.holder-id", defaultValue = "placement-local")
                           String holderId,
                           @ConfigProperty(name = "kubemoot.placement.lease-seconds", defaultValue = "15")
                           int leaseSeconds,
                           @ConfigProperty(name = "kubemoot.placement.ticket-ttl-seconds", defaultValue = "600")
                           int ticketTtlSeconds,
                           @ConfigProperty(name = "kubemoot.placement.default-policy", defaultValue = "algorithm")
                           String defaultPolicy) {
        this(natsUrl, natsUser, natsPassword, holderId, Duration.ofSeconds(Math.max(3, leaseSeconds)),
                Duration.ofSeconds(Math.max(10, ticketTtlSeconds)), defaultPolicy);
    }

    ServiceSettings(Optional<String> natsUrl, Optional<String> natsUser, Optional<String> natsPassword,
                    String holderId, Duration lease, Duration ticketTtl, String defaultPolicy) {
        this.natsUrl = natsUrl.filter(s -> !s.isBlank());
        this.natsUser = natsUser.filter(s -> !s.isBlank());
        this.natsPassword = natsPassword.filter(s -> !s.isBlank());
        this.holderId = holderId;
        this.lease = lease;
        this.ticketTtl = ticketTtl;
        this.defaultPolicy = defaultPolicy;
    }

    /** Settings for tests. */
    static ServiceSettings forTest(String holderId, Duration lease, Duration ticketTtl) {
        return new ServiceSettings(Optional.empty(), Optional.empty(), Optional.empty(), holderId, lease,
                ticketTtl, "algorithm");
    }

    public Optional<String> natsUrl() {
        return natsUrl;
    }

    public Optional<String> natsUser() {
        return natsUser;
    }

    public Optional<String> natsPassword() {
        return natsPassword;
    }

    /** This replica's name, written into the lease and onto every ticket it claims. */
    public String holderId() {
        return holderId;
    }

    /** How long a lease holds without a renewal. */
    public Duration lease() {
        return lease;
    }

    /** How long a ticket whose holder never released it stays before it is dropped. */
    public Duration ticketTtl() {
        return ticketTtl;
    }

    /** The placement policy used for a caller that names none. */
    public String defaultPolicy() {
        return defaultPolicy;
    }
}
