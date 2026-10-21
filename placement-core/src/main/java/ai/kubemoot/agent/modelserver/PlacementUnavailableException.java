package ai.kubemoot.agent.modelserver;

/**
 * The placement service could not answer: it is unreachable, timed out, is not the leader, or answered
 * with an error. It is not a refusal. A caller that gets it places the call without the service,
 * on the crew's declared server or the last one the service chose, and says so; it never waits for the
 * service and never fails the call because of it.
 */
public class PlacementUnavailableException extends RuntimeException {

    public PlacementUnavailableException(String message) {
        super(message);
    }

    public PlacementUnavailableException(String message, Throwable cause) {
        super(message, cause);
    }
}
