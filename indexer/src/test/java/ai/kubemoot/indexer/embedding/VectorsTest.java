package ai.kubemoot.indexer.embedding;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

class VectorsTest {

    @Test
    void scalesToUnitLength() {
        float[] v = Vectors.l2Normalize(new float[]{3f, 0f, 4f});

        assertArrayEquals(new float[]{0.6f, 0f, 0.8f}, v, 1e-6f);
    }

    @Test
    void anUnnormalizedServerVectorEndsUpWithLengthOne() {
        float[] v = Vectors.l2Normalize(new float[]{22f, -7f, 13f, 1f});

        double sum = 0;
        for (float x : v) {
            sum += x * x;
        }
        assertEquals(1.0, Math.sqrt(sum), 1e-6);
    }

    @Test
    void aVectorThatIsAlreadyUnitStaysTheSame() {
        float[] v = Vectors.l2Normalize(new float[]{0f, 1f});

        assertArrayEquals(new float[]{0f, 1f}, v, 1e-7f);
    }

    @Test
    void aZeroVectorHasNoDirectionAndIsUnchanged() {
        assertArrayEquals(new float[]{0f, 0f}, Vectors.l2Normalize(new float[]{0f, 0f}));
    }

    @Test
    void nullAndEmptyAreLeftAlone() {
        assertNull(Vectors.l2Normalize(null));
        assertEquals(0, Vectors.l2Normalize(new float[0]).length);
    }
}
