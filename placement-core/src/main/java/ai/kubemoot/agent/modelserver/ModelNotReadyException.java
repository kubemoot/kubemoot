package ai.kubemoot.agent.modelserver;

/** The server did not make a model ready (the operator refused, failed, or did not answer). */
public class ModelNotReadyException extends RuntimeException {

    /** Between the server's name and the model in the text of a model that could not be made ready. */
    public static final String COULD_NOT_MAKE = " could not make ";
    /** Between the model and the operator's reason in the same text. */
    public static final String READY_SUFFIX = " ready: ";

    public ModelNotReadyException(String message) {
        super(message);
    }

    /** The exception for a server that could not make {@code model} ready, for the reason the operator gave. */
    public static ModelNotReadyException of(String server, String model, String reason) {
        return new ModelNotReadyException("server " + server + COULD_NOT_MAKE + model + READY_SUFFIX + reason);
    }
}
