package ai.kubemoot.agent.modelserver;

/** The server did not make a model ready (the operator refused, failed, or did not answer). */
public class ModelNotReadyException extends RuntimeException {

    public ModelNotReadyException(String message) {
        super(message);
    }
}
