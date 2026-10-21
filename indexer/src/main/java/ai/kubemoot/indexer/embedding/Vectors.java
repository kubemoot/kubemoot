package ai.kubemoot.indexer.embedding;

/**
 * Vector arithmetic for embeddings.
 */
public final class Vectors {

    private Vectors() {
    }

    /**
     * Scales a vector to unit length, in place, so vectors from every embedding server
     * compare the same way. Servers differ: Ollama answers unit vectors, vLLM answers the
     * raw model output. A zero vector has no direction and is returned unchanged.
     *
     * @param vector the vector to scale; may be {@code null}
     * @return the same array, scaled
     */
    public static float[] l2Normalize(float[] vector) {
        if (vector == null) {
            return null;
        }
        double sum = 0;
        for (float x : vector) {
            sum += (double) x * x;
        }
        if (sum == 0) {
            return vector;
        }
        float norm = (float) Math.sqrt(sum);
        for (int i = 0; i < vector.length; i++) {
            vector[i] /= norm;
        }
        return vector;
    }
}
