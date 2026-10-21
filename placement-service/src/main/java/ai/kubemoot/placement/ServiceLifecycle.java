package ai.kubemoot.placement;

import io.micrometer.core.instrument.FunctionCounter;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.event.Observes;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;
import org.eclipse.microprofile.health.HealthCheck;
import org.eclipse.microprofile.health.HealthCheckResponse;
import org.eclipse.microprofile.health.Readiness;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;

/**
 * What the service does when its replica gains or loses the lease, and its readiness: a replica is
 * Ready only while it is the leader and has recovered the tickets, so the Service routes to exactly
 * one pod.
 *
 * <ul>
 *   <li><b>Elected.</b> The table starts empty, the mirror takes in what the previous leader left in
 *       the bucket, the server-state watch starts, and then the table opens to claims.</li>
 *   <li><b>Demoted.</b> The table closes first, so no claim is answered, then the mirror writes what is
 *       queued and stops. The bucket keeps every entry for the next leader.</li>
 * </ul>
 */
@Singleton
public class ServiceLifecycle implements Leadership.Callbacks {

    private static final Logger log = LoggerFactory.getLogger(ServiceLifecycle.class);
    private static final Duration CATCH_UP = Duration.ofSeconds(5);
    private static final Duration DRAIN = Duration.ofSeconds(2);

    private final Leadership leadership;
    private final TicketTable table;
    private final TicketMirror mirror;
    private final StateWatch stateWatch;
    private final PlacementEngine engine;
    private final CapacityVersion capacity;
    private final MeterRegistry registry;

    @Inject
    public ServiceLifecycle(Leadership leadership, TicketTable table, TicketMirror mirror, StateWatch stateWatch,
                            PlacementEngine engine, CapacityVersion capacity, MeterRegistry registry) {
        this.leadership = leadership;
        this.table = table;
        this.mirror = mirror;
        this.stateWatch = stateWatch;
        this.engine = engine;
        this.capacity = capacity;
        this.registry = registry;
    }

    void onStart(@Observes StartupEvent event) {
        registerMetrics();
        table.fenceWith(leadership::isLeader);
        mirror.fenceWith(leadership::isLeader);
        leadership.register(this);
        leadership.start();
    }

    @Override
    public void elected() throws Exception {
        table.deactivate();
        mirror.start(CATCH_UP);
        stateWatch.start();
        table.activate();
        capacity.bump();
    }

    @Override
    public void demoted(boolean leaseLost) {
        table.deactivate();
        stateWatch.stop();
        // After a lost lease the next leader may already be recovering from the bucket, so nothing
        // more is written; on a clean shutdown the queue is written before the lease is handed over.
        mirror.stop(leaseLost ? Duration.ZERO : DRAIN);
        engine.reset();
        log.info("Stopped serving placements");
    }

    /** Ready only on the leader, so the Service has one endpoint. */
    @Readiness
    @Singleton
    public static class LeaderReadiness implements HealthCheck {
        private final Leadership leadership;
        private final TicketTable table;

        @Inject
        public LeaderReadiness(Leadership leadership, TicketTable table) {
            this.leadership = leadership;
            this.table = table;
        }

        @Override
        public HealthCheckResponse call() {
            boolean ready = leadership.isLeader() && table.available();
            return HealthCheckResponse.named("placement-leader")
                    .status(ready)
                    .withData("role", ready ? "leader" : "standby")
                    .build();
        }
    }

    private void registerMetrics() {
        Gauge.builder("kubemoot_placement_leader", leadership, l -> l.isLeader() ? 1.0 : 0.0)
                .description("1 while this replica is the placement leader").register(registry);
        Gauge.builder("kubemoot_placement_tickets", table, t -> t.size())
                .description("Tickets and residency entries held in memory").register(registry);
        Gauge.builder("kubemoot_placement_capacity_waits", capacity, CapacityVersion::waiting)
                .description("Open waits for a capacity change").register(registry);
        Gauge.builder("kubemoot_placement_mirror_queue", mirror, TicketMirror::queued)
                .description("Ticket changes queued for the NATS mirror").register(registry);
        FunctionCounter.builder("kubemoot_placement_mirror_writes_total", mirror, m -> (double) m.written())
                .description("Ticket changes written to the NATS mirror").register(registry);
        FunctionCounter.builder("kubemoot_placement_mirror_failures_total", mirror, m -> (double) m.failed())
                .description("Ticket changes the NATS mirror could not write").register(registry);
        FunctionCounter.builder("kubemoot_placement_mirror_dropped_total", mirror, m -> (double) m.dropped())
                .description("Ticket changes dropped because the mirror queue was full").register(registry);
    }
}
