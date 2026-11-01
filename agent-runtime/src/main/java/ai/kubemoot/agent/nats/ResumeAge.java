package ai.kubemoot.agent.nats;

import java.math.BigDecimal;
import java.time.Duration;
import java.util.Map;
import java.util.Optional;
import java.util.function.Consumer;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * The longest a thread may have gone without a logged event for a restarted coordinator to
 * resume it. The setting is a Go duration (the chart and the operator use that form): a
 * sequence of numbers with units, such as {@code 30m}, {@code 1h30m} or {@code 90s}. Zero or
 * empty means no limit.
 */
final class ResumeAge {

    /** The limit when nothing is set. */
    static final Duration DEFAULT = Duration.ofMinutes(30);

    private static final Pattern WHOLE = Pattern.compile("^([0-9]+(\\.[0-9]+)?(ns|us|ms|s|m|h))+$");
    private static final Pattern PART = Pattern.compile("([0-9]+(?:\\.[0-9]+)?)(ns|us|ms|s|m|h)");
    private static final Map<String, Long> NANOS = Map.of(
            "ns", 1L, "us", 1_000L, "ms", 1_000_000L, "s", 1_000_000_000L,
            "m", 60_000_000_000L, "h", 3_600_000_000_000L);

    private ResumeAge() {
    }

    /**
     * The limit {@code text} names; {@link Duration#ZERO} for no limit. Empty when {@code text}
     * is not a duration.
     */
    static Optional<Duration> parse(String text) {
        String trimmed = text == null ? "" : text.strip();
        if (trimmed.isEmpty() || "0".equals(trimmed)) {
            return Optional.of(Duration.ZERO);
        }
        if (!WHOLE.matcher(trimmed).matches()) {
            return Optional.empty();
        }
        try {
            long nanos = 0;
            Matcher part = PART.matcher(trimmed);
            while (part.find()) {
                long partNanos = new BigDecimal(part.group(1)).multiply(BigDecimal.valueOf(NANOS.get(part.group(2))))
                        .toBigInteger().longValueExact();
                nanos = Math.addExact(nanos, partNanos);
            }
            return Optional.of(Duration.ofNanos(nanos));
        } catch (ArithmeticException e) {
            return Optional.empty();
        }
    }

    /**
     * The limit for the setting's value: the default when it is unset, the default when it is not a
     * duration ({@code onInvalid} is told, so the caller can warn), else what it names.
     */
    static Duration from(Optional<String> text, Consumer<String> onInvalid) {
        if (text.isEmpty()) {
            return DEFAULT;
        }
        return parse(text.get()).orElseGet(() -> {
            onInvalid.accept(text.get());
            return DEFAULT;
        });
    }
}
