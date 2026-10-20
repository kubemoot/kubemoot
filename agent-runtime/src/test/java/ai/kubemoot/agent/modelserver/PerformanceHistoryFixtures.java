package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.ObjectMapper;

/** Builds a {@link PerformanceHistory} that writes on the caller's thread, for tests in other packages. */
public final class PerformanceHistoryFixtures {

    private PerformanceHistoryFixtures() {}

    public static PerformanceHistory direct(NatsConnectionProvider nats, ObjectMapper mapper, AgentProperties props) {
        return new PerformanceHistory(nats, mapper, props, Runnable::run, SchedulerMetrics.none());
    }
}
