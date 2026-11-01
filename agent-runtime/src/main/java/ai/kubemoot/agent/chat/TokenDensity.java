package ai.kubemoot.agent.chat;

/**
 * How much denser than prose a text tokenizes. Prose runs near 4 characters per token, but
 * numeric JSON (telemetry, resource lists, RRD series) runs near 1.4: tokenizers of the common
 * model families emit digits one per token and split most punctuation off. A flat characters-per-token
 * ratio counts such text at about half its real size, so a result that "fits" the room overflows
 * the window. The estimate therefore adds extra characters for each digit and each symbol, so
 * the count follows the content and not only the length.
 */
final class TokenDensity {

    /**
     * Extra characters counted for each digit. A digit is about one token, against the 3.0
     * characters per token the estimate allows for JSON.
     */
    static final int DIGIT_EXTRA = 2;

    /** Extra characters counted for each punctuation or quote character (about 2 per token). */
    static final int SYMBOL_EXTRA = 1;

    private TokenDensity() {}

    /**
     * Characters to add to the length of {@code text} when estimating its tokens: each digit
     * counts {@link #DIGIT_EXTRA} more and each other non-letter, non-space character
     * {@link #SYMBOL_EXTRA} more. Letters and spaces count once. Zero for null or empty text.
     */
    static long extraChars(String text) {
        if (text == null) {
            return 0;
        }
        long extra = 0;
        for (int i = 0; i < text.length(); i++) {
            extra += extraFor(text.charAt(i));
        }
        return extra;
    }

    private static int extraFor(char c) {
        if (Character.isDigit(c)) {
            return DIGIT_EXTRA;
        }
        return Character.isLetter(c) || Character.isWhitespace(c) ? 0 : SYMBOL_EXTRA;
    }
}
