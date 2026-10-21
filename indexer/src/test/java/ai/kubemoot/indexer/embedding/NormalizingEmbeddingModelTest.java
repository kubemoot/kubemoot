package ai.kubemoot.indexer.embedding;

import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.Embedding;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.embedding.EmbeddingRequest;
import org.springframework.ai.embedding.EmbeddingResponse;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

class NormalizingEmbeddingModelTest {

    private final EmbeddingModel delegate = mock(EmbeddingModel.class);
    private final EmbeddingModel model = new NormalizingEmbeddingModel(delegate);

    @Test
    void callReturnsUnitVectors() {
        EmbeddingResponse raw = new EmbeddingResponse(List.of(new Embedding(new float[]{3f, 4f}, 0)));
        when(delegate.call(any(EmbeddingRequest.class))).thenReturn(raw);

        EmbeddingResponse response = model.call(new EmbeddingRequest(List.of("hello"), null));

        assertArrayEquals(new float[]{0.6f, 0.8f}, response.getResults().get(0).getOutput(), 1e-6f);
        assertEquals(0, response.getResults().get(0).getIndex());
    }

    @Test
    void embedStringAndDocumentReturnUnitVectors() {
        when(delegate.embed("hello")).thenReturn(new float[]{0f, 5f});
        Document doc = new Document("text");
        when(delegate.embed(doc)).thenReturn(new float[]{6f, 8f});

        assertArrayEquals(new float[]{0f, 1f}, model.embed("hello"), 1e-6f);
        assertArrayEquals(new float[]{0.6f, 0.8f}, model.embed(doc), 1e-6f);
    }

    @Test
    void embedListReturnsUnitVectors() {
        when(delegate.embed(List.of("a", "b"))).thenReturn(List.of(new float[]{2f, 0f}, new float[]{0f, 9f}));

        List<float[]> out = model.embed(List.of("a", "b"));

        assertArrayEquals(new float[]{1f, 0f}, out.get(0), 1e-6f);
        assertArrayEquals(new float[]{0f, 1f}, out.get(1), 1e-6f);
    }

    @Test
    void postProcessorWrapsAnEmbeddingModelOnceAndLeavesOtherBeansAlone() {
        NormalizingEmbeddingModelPostProcessor processor = new NormalizingEmbeddingModelPostProcessor();

        Object wrapped = processor.postProcessAfterInitialization(delegate, "embeddingModel");
        Object again = processor.postProcessAfterInitialization(wrapped, "embeddingModel");
        String other = "not an embedding model";

        assertEquals(NormalizingEmbeddingModel.class, wrapped.getClass());
        assertSame(wrapped, again);
        assertSame(other, processor.postProcessAfterInitialization(other, "x"));
    }
}
