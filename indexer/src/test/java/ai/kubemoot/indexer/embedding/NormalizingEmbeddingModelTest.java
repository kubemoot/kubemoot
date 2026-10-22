package ai.kubemoot.indexer.embedding;

import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.Embedding;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.embedding.EmbeddingRequest;
import org.springframework.ai.embedding.EmbeddingResponse;

import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.env.PropertiesPropertySourceLoader;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.PropertySource;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.core.io.ClassPathResource;
import org.springframework.ai.model.openai.autoconfigure.OpenAiEmbeddingProperties;

import java.io.IOException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Map;
import java.util.stream.IntStream;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
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
        NormalizingEmbeddingModelPostProcessor processor = new NormalizingEmbeddingModelPostProcessor(4);

        Object wrapped = processor.postProcessAfterInitialization(delegate, "embeddingModel");
        Object again = processor.postProcessAfterInitialization(wrapped, "embeddingModel");
        String other = "not an embedding model";

        assertEquals(NormalizingEmbeddingModel.class, wrapped.getClass());
        assertSame(wrapped, again);
        assertSame(other, processor.postProcessAfterInitialization(other, "x"));
    }

    private static List<String> inputs(int n) {
        return IntStream.range(0, n).mapToObj(i -> "t" + i).toList();
    }

    private void echoSizes(List<Integer> sizes) {
        when(delegate.call(any(EmbeddingRequest.class))).thenAnswer(inv -> {
            List<String> in = ((EmbeddingRequest) inv.getArgument(0)).getInstructions();
            sizes.add(in.size());
            List<Embedding> out = new ArrayList<>();
            for (int i = 0; i < in.size(); i++) {
                out.add(new Embedding(new float[]{Float.parseFloat(in.get(i).substring(1)) + 1f, 0f}, i));
            }
            return new EmbeddingResponse(out);
        });
    }

    @Test
    void callSplitsLargeRequestsAndKeepsOrderAndIndexes() {
        List<Integer> sizes = new ArrayList<>();
        echoSizes(sizes);
        EmbeddingModel batched = new NormalizingEmbeddingModel(delegate, 8);

        EmbeddingResponse response = batched.call(new EmbeddingRequest(inputs(20), null));

        assertEquals(List.of(8, 8, 4), sizes);
        assertEquals(20, response.getResults().size());
        for (int i = 0; i < 20; i++) {
            assertEquals(i, response.getResults().get(i).getIndex());
            assertArrayEquals(new float[]{1f, 0f}, response.getResults().get(i).getOutput(), 1e-6f);
        }
    }

    @Test
    void callSendsASmallRequestWhole() {
        List<Integer> sizes = new ArrayList<>();
        echoSizes(sizes);

        new NormalizingEmbeddingModel(delegate, 8).call(new EmbeddingRequest(inputs(8), null));

        assertEquals(List.of(8), sizes);
    }

    @Test
    void embedListAndDocumentsAreSplit() {
        when(delegate.embed(any(List.class))).thenAnswer(inv ->
            ((List<?>) inv.getArgument(0)).stream().map(x -> new float[]{3f, 4f}).toList());
        EmbeddingModel batched = new NormalizingEmbeddingModel(delegate, 3);

        List<float[]> out = batched.embed(inputs(7));

        assertEquals(7, out.size());
        assertArrayEquals(new float[]{0.6f, 0.8f}, out.get(6), 1e-6f);
        verify(delegate, times(3)).embed(any(List.class));
    }

    @Test
    void aBatchSizeBelowOneFallsBackToTheDefault() {
        assertEquals(NormalizingEmbeddingModel.DEFAULT_BATCH_SIZE, new NormalizingEmbeddingModel(delegate, 0).batchSize());
        assertEquals(NormalizingEmbeddingModel.DEFAULT_BATCH_SIZE, new NormalizingEmbeddingModel(delegate, -5).batchSize());
        assertEquals(5, new NormalizingEmbeddingModel(delegate, 5).batchSize());
    }

    @Test
    void anEmptyRequestSendsNothingMoreThanOneCall() {
        when(delegate.embed(any(List.class))).thenReturn(List.of());
        assertTrue(new NormalizingEmbeddingModel(delegate, 8).embed(List.<String>of()).isEmpty());
    }

    private Binder binderWith(Map<String, Object> env) throws IOException {
        StandardEnvironment environment = new StandardEnvironment();
        List<PropertySource<?>> loaded = new PropertiesPropertySourceLoader()
            .load("application", new ClassPathResource("application.properties"));
        environment.getPropertySources().addLast(loaded.get(0));
        environment.getPropertySources().addFirst(new MapPropertySource("env", env));
        return Binder.get(environment);
    }

    @Test
    void theClientRequestTimeoutDefaultsToFiveMinutesAndComesFromConfiguration() throws IOException {
        OpenAiEmbeddingProperties byDefault = binderWith(Map.of())
            .bind("spring.ai.openai.embedding", OpenAiEmbeddingProperties.class).get();
        OpenAiEmbeddingProperties configured = binderWith(Map.of("KUBEMOOT_EMBEDDING_TIMEOUT", "90s"))
            .bind("spring.ai.openai.embedding", OpenAiEmbeddingProperties.class).get();

        assertEquals(Duration.ofMinutes(5), byDefault.getTimeout());
        assertEquals(Duration.ofSeconds(90), configured.getTimeout());
    }

    @Test
    void theBatchSizePropertyDefaultsToEightAndComesFromConfiguration() throws IOException {
        assertEquals(8, binderWith(Map.of()).bind("kubemoot.embedding-batch-size", Integer.class).get());
        assertEquals(16, binderWith(Map.of("KUBEMOOT_EMBEDDING_BATCH_SIZE", "16"))
            .bind("kubemoot.embedding-batch-size", Integer.class).get());
    }
}
