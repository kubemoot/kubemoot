package ai.kubemoot.agent.nats;

/** Keys of the metadata object that rides on a discussion message, shared by whoever writes or reads it. */
final class MetaKeys {

    static final String LAST_ERROR = "lastError";
    static final String FAILURE_TYPE = "failureType";

    private MetaKeys() {
    }
}
