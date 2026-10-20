package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.annotation.JsonProperty;

/**
 * A header the gateway sends on every call to one server. The value is a secret: it stays
 * in memory, is never part of a {@link ServerRegistration}, and never appears in
 * {@code toString}, so logging a request or this record cannot expose it.
 */
public record ServerCredential(String header, @JsonProperty(access = JsonProperty.Access.WRITE_ONLY) String value) {

    public ServerCredential {
        if (header == null || header.isEmpty() || !header.chars().allMatch(ServerCredential::isTokenChar)) {
            throw new IllegalArgumentException("Credential header must be a valid header name");
        }
        if (value == null || value.isBlank() || value.chars().anyMatch(c -> c == '\r' || c == '\n')) {
            throw new IllegalArgumentException("Credential value must be a single non-empty line");
        }
    }

    /** An RFC 7230 token character, the same set the operator and the CRD accept. */
    private static boolean isTokenChar(int c) {
        return Character.isLetterOrDigit(c) && c < 128 || "!#$%&'*+-.^_`|~".indexOf(c) >= 0;
    }

    @Override
    public String toString() {
        return "ServerCredential[header=" + header + ", value=<redacted>]";
    }
}
