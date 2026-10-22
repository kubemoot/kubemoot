package ai.kubemoot.indexer.embedding;

import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.BatchingStrategy;
import org.springframework.ai.embedding.Embedding;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.embedding.EmbeddingOptions;
import org.springframework.ai.embedding.EmbeddingRequest;
import org.springframework.ai.embedding.EmbeddingResponse;

import java.util.ArrayList;
import java.util.List;

/**
 * An {@link EmbeddingModel} that returns the vectors of another one scaled to unit
 * length, whichever server produced them, and sends the server at most
 * {@code batchSize} inputs per request so one request stays short on a slow server.
 */
public class NormalizingEmbeddingModel implements EmbeddingModel {

    /** Inputs per request when none is configured. */
    public static final int DEFAULT_BATCH_SIZE = 8;

    private final EmbeddingModel delegate;
    private final int batchSize;

    public NormalizingEmbeddingModel(EmbeddingModel delegate) {
        this(delegate, DEFAULT_BATCH_SIZE);
    }

    /**
     * @param batchSize the most inputs sent in one request; a value below 1 means the default
     */
    public NormalizingEmbeddingModel(EmbeddingModel delegate, int batchSize) {
        this.delegate = delegate;
        this.batchSize = batchSize < 1 ? DEFAULT_BATCH_SIZE : batchSize;
    }

    int batchSize() {
        return batchSize;
    }

    @Override
    public EmbeddingResponse call(EmbeddingRequest request) {
        List<String> inputs = request.getInstructions();
        if (inputs.size() <= batchSize) {
            return normalized(delegate.call(request), 0);
        }
        List<Embedding> all = new ArrayList<>(inputs.size());
        EmbeddingResponse last = null;
        for (List<String> part : partition(inputs)) {
            last = delegate.call(new EmbeddingRequest(part, request.getOptions()));
            all.addAll(normalized(last, all.size()).getResults());
        }
        return new EmbeddingResponse(all, last.getMetadata());
    }

    private static EmbeddingResponse normalized(EmbeddingResponse response, int offset) {
        List<Embedding> source = response.getResults();
        List<Embedding> results = new ArrayList<>(source.size());
        for (int position = 0; position < source.size(); position++) {
            Embedding e = source.get(position);
            int index = e.getIndex() != null ? e.getIndex() : position;
            results.add(new Embedding(Vectors.l2Normalize(e.getOutput()), offset + index, e.getMetadata()));
        }
        return new EmbeddingResponse(results, response.getMetadata());
    }

    private <T> List<List<T>> partition(List<T> items) {
        List<List<T>> parts = new ArrayList<>();
        for (int from = 0; from < items.size(); from += batchSize) {
            parts.add(items.subList(from, Math.min(items.size(), from + batchSize)));
        }
        return parts;
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
        List<float[]> vectors = new ArrayList<>(texts.size());
        for (List<String> part : partition(texts)) {
            delegate.embed(part).forEach(v -> vectors.add(Vectors.l2Normalize(v)));
        }
        return vectors;
    }

    @Override
    public List<float[]> embed(List<Document> documents, EmbeddingOptions options, BatchingStrategy batchingStrategy) {
        List<float[]> vectors = new ArrayList<>(documents.size());
        for (List<Document> part : partition(documents)) {
            delegate.embed(part, options, batchingStrategy).forEach(v -> vectors.add(Vectors.l2Normalize(v)));
        }
        return vectors;
    }

    @Override
    public int dimensions() {
        return delegate.dimensions();
    }
}
