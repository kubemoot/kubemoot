package ai.kubemoot.agent.nats;

import java.math.BigDecimal;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.function.Consumer;

/**
 * The longest a thread may have gone without a logged event for a restarted coordinator to
 * resume it. The setting is a Go duration (the chart and the operator use that form): a
 * sequence of numbers with units, such as {@code 30m}, {@code 1h30m} or {@code 90s}. Zero or
 * empty means no limit.
 */
final class ResumeAge {

    /** The limit when nothing is set. */
    static final Duration DEFAULT = Duration.ofMinutes(30);

    private static final Map<String, Long> NANOS = Map.of(
            "ns", 1L, "us", 1_000L, "ms", 1_000_000L, "s", 1_000_000_000L,
            "m", 60_000_000_000L, "h", 3_600_000_000_000L);

    /** Every unit, two-letter ones first. */
    private static final List<String> UNITS = List.of("ns", "us", "ms", "s", "m", "h");

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
        try {
            long nanos = 0;
            int at = 0;
            while (at < trimmed.length()) {
                int numberEnd = numberEnd(trimmed, at);
                String unit = numberEnd < 0 ? null : unitAt(trimmed, numberEnd);
                if (unit == null) {
                    return Optional.empty();
                }
                long partNanos = new BigDecimal(trimmed.substring(at, numberEnd))
                        .multiply(BigDecimal.valueOf(NANOS.get(unit))).toBigInteger().longValueExact();
                nanos = Math.addExact(nanos, partNanos);
                at = numberEnd + unit.length();
            }
            return Optional.of(Duration.ofNanos(nanos));
        } catch (ArithmeticException e) {
            return Optional.empty();
        }
    }

    /** The end of the number starting at {@code from} (digits, then optionally a dot and digits); -1 if none. */
    private static int numberEnd(String text, int from) {
        int at = digitsEnd(text, from);
        if (at == from) {
            return -1;
        }
        if (at < text.length() && text.charAt(at) == '.') {
            int fraction = digitsEnd(text, at + 1);
            return fraction == at + 1 ? -1 : fraction;
        }
        return at;
    }

    private static int digitsEnd(String text, int from) {
        int at = from;
        while (at < text.length() && text.charAt(at) >= '0' && text.charAt(at) <= '9') {
            at++;
        }
        return at;
    }

    /** The unit at {@code at}: two-letter units first, so "ms" is never read as minutes then seconds. */
    private static String unitAt(String text, int at) {
        for (String unit : UNITS) {
            if (text.startsWith(unit, at)) {
                return unit;
            }
        }
        return null;
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
