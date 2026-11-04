package ai.kubemoot.agent.nats;

/** The discussion roles the crew resumes carry, and which of them gather data; the one place they are named. */
final class CrewRoles {

    static final String ANALYST = "analyst";
    static final String TOOLER = "tooler";

    private CrewRoles() {}

    /** True for the agents whose contribution is gathered data: role "tooler", or none declared. */
    static boolean gathersData(String role) {
        return role == null || role.isEmpty() || TOOLER.equals(role);
    }
}
