package ai.kubemoot.agent.tracing;

import io.opentelemetry.api.trace.Tracer;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import jakarta.inject.Inject;

/** Hands the extension's tracer to {@link AgentTrace} when the runtime starts. */
@ApplicationScoped
public class TracingSetup {

    private final Tracer tracer;

    @Inject
    public TracingSetup(Tracer tracer) {
        this.tracer = tracer;
    }

    void onStart(@Observes StartupEvent event) {
        AgentTrace.install(tracer);
    }
}
