package kubemoot.ai.mcpgateway.model;

import java.time.Instant;

/**
 * One person's token for one upstream call. It lives in a local variable for that call and is
 * never stored, cached, logged, serialized or printed: {@code toString} redacts it.
 */
public record CallerToken(String value, Instant expiresAt) {

    public CallerToken {
        if (value == null || value.isBlank() || value.chars().anyMatch(c -> c < 0x21 || c > 0x7e)) {
            throw new IllegalArgumentException("Token must be a single printable ASCII word");
        }
    }

    public boolean expired(Instant now) {
        return expiresAt != null && !expiresAt.isAfter(now);
    }

    @Override
    public String toString() {
        return "CallerToken[value=<redacted>, expiresAt=" + expiresAt + "]";
    }
}
