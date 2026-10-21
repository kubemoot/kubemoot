package ai.kubemoot.agent.modelserver;

/**
 * Which agent a placement is made for. The agent's pod has exactly one; the placement service
 * serves every agent and keeps one set of per-agent placement state for each identity it sees.
 *
 * @param namespace the agent's namespace; empty when unknown
 * @param agentName the agent's name
 * @param crew      the agent's crew; empty when it belongs to none
 */
public record AgentIdentity(String namespace, String agentName, String crew) {

    public AgentIdentity {
        namespace = namespace == null ? "" : namespace;
        agentName = agentName == null ? "" : agentName;
        crew = crew == null ? "" : crew;
    }

    /** The id demand and history entries carry: {@code <namespace>/<agent>}. */
    public String agentId() {
        return namespace + "/" + agentName;
    }

    /** The crew id demand entries carry: {@code <namespace>/<crew>}. */
    public String crewId() {
        return namespace + "/" + crew;
    }
}
