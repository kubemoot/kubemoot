package ai.kubemoot.agent;

import ai.kubemoot.agent.config.AgentProperties;

/** Shared defaults for anonymous AgentProperties stubs in unit tests. */
public final class TestStubs {

    private TestStubs() {}

    /** Default crew working-memory config (mirrors the CRD defaults). */
    public static AgentProperties.Memory memory() {
        return new AgentProperties.Memory() {
            @Override public boolean enabled() { return true; }
            @Override public int maxFacts() { return 5000; }
            @Override public int ttlDays() { return 365; }
            @Override public int injectLimit() { return 8; }
            @Override public boolean verifyOnAdd() { return true; }
        };
    }
}
