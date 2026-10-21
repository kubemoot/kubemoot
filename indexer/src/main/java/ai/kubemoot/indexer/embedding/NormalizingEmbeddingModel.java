package ai.kubemoot.indexer.embedding;

import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.BatchingStrategy;
import org.springframework.ai.embedding.Embedding;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.embedding.EmbeddingOptions;
import org.springframework.ai.embedding.EmbeddingRequest;
import org.springframework.ai.embedding.EmbeddingResponse;

import java.util.List;

/**
 * An {@link EmbeddingModel} that returns the vectors of another one scaled to unit
 * length, whichever server produced them.
 */
public class NormalizingEmbeddingModel implements EmbeddingModel {

    private final EmbeddingModel delegate;

    public NormalizingEmbeddingModel(EmbeddingModel delegate) {
        this.delegate = delegate;
    }

    @Override
    public EmbeddingResponse call(EmbeddingRequest request) {
        EmbeddingResponse response = delegate.call(request);
        List<Embedding> normalized = response.getResults().stream()
            .map(e -> new Embedding(Vectors.l2Normalize(e.getOutput()), e.getIndex(), e.getMetadata()))
            .toList();
        return new EmbeddingResponse(normalized, response.getMetadata());
    }

    @Override
    public float[] embed(Document document) {
        return Vectors.l2Normalize(delegate.embed(document));
    }

    @Override
    public float[] embed(String text) {
        return Vectors.l2Normalize(delegate.embed(text));
    }

    @Override
    public List<float[]> embed(List<String> texts) {
        return delegate.embed(texts).stream().map(Vectors::l2Normalize).toList();
    }

    @Override
    public List<float[]> embed(List<Document> documents, EmbeddingOptions options, BatchingStrategy batchingStrategy) {
        return delegate.embed(documents, options, batchingStrategy).stream().map(Vectors::l2Normalize).toList();
    }

    @Override
    public int dimensions() {
        return delegate.dimensions();
    }
}
