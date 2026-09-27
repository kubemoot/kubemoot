package ai.kubemoot.indexer.source;

import org.springframework.web.reactive.function.client.WebClient;

/**
 * The HTTP client every source uses to fetch documents. WebClient buffers a response
 * body in memory up to a limit that defaults to 256 KB, far below a book, a PDF, or
 * a long web page; this client raises it for the indexer's downloads.
 */
public final class Downloads {

    /** The largest document the indexer fetches into memory. */
    public static final int MAX_DOCUMENT_BYTES = 64 * 1024 * 1024;

    private Downloads() {
    }

    /** A WebClient from the given builder (left unchanged) that accepts documents up to MAX_DOCUMENT_BYTES. */
    public static WebClient client(WebClient.Builder builder) {
        return builder.clone()
            .codecs(codecs -> codecs.defaultCodecs().maxInMemorySize(MAX_DOCUMENT_BYTES))
            .build();
    }
}
