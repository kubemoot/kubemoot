package ai.kubemoot.agent.provider;

/** The provider did not make a model ready (the operator refused, failed, or did not answer). */
public class ModelNotReadyException extends RuntimeException {

    public ModelNotReadyException(String message) {
        super(message);
    }
}
