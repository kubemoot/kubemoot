package ai.kubemoot.indexer.embedding;

import org.springframework.beans.factory.config.BeanPostProcessor;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.stereotype.Component;

/**
 * Wraps the embedding model Spring AI configures, so the indexer stores unit-length
 * vectors whichever server it embeds through.
 */
@Component
public class NormalizingEmbeddingModelPostProcessor implements BeanPostProcessor {

    @Override
    public Object postProcessAfterInitialization(Object bean, String beanName) {
        if (bean instanceof EmbeddingModel model && !(bean instanceof NormalizingEmbeddingModel)) {
            return new NormalizingEmbeddingModel(model);
        }
        return bean;
    }
}
