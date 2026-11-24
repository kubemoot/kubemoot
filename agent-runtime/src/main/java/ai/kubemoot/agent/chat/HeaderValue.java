package ai.kubemoot.agent.chat;

import java.io.ByteArrayOutputStream;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.util.Optional;

/**
 * Carries any text in a NATS header value, which allows printable ASCII only. A value of printable
 * ASCII that does not start with {@link #MARKER} is written as it is. Any other value is written as
 * {@link #MARKER} followed by the text's UTF-8 bytes, each byte outside printable ASCII (and the
 * percent sign) as {@code %XX}. A value without the marker is read as it is, so objects stored before
 * the encoding existed read as before; the one edge is a legacy plain value that starts with
 * {@link #MARKER} and has only valid escapes after it, which reads as encoded in both languages.
 * An unpaired surrogate has no UTF-8 form and is written as {@code ?} ({@code String.getBytes}
 * replaces it), so it does not survive a round trip. A value grows at most 9 characters per
 * character (three bytes, each {@code %XX}), so a CJK character takes 9. Go's {@code store.DecodeHeaderValue} reads the same form;
 * both run {@code test/fixtures/header-values.json}.
 */
public final class HeaderValue {

    /** Starts every encoded value. */
    public static final String MARKER = "utf8-pct:";

    private static final char[] HEX = "0123456789ABCDEF".toCharArray();

    private HeaderValue() {}

    /** The value as it is written to a header. */
    public static String encode(String value) {
        if (!needsEncoding(value)) {
            return value;
        }
        var out = new StringBuilder(MARKER);
        for (byte b : value.getBytes(StandardCharsets.UTF_8)) {
            int c = b & 0xFF;
            if (isLiteral(c)) {
                out.append((char) c);
            } else {
                out.append('%').append(HEX[c >> 4]).append(HEX[c & 0xF]);
            }
        }
        return out.toString();
    }

    /** The text a header value stands for; a value that is not validly encoded is returned as it is. */
    public static String decode(String value) {
        if (value == null || !value.startsWith(MARKER)) {
            return value;
        }
        Optional<byte[]> bytes = unescape(value, MARKER.length());
        if (bytes.isEmpty()) {
            return value;
        }
        try {
            return StandardCharsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(bytes.get())).toString();
        } catch (CharacterCodingException e) {
            return value;
        }
    }

    private static boolean needsEncoding(String value) {
        if (value.startsWith(MARKER)) {
            return true;
        }
        for (int i = 0; i < value.length(); i++) {
            if (!isPrintableAscii(value.charAt(i))) {
                return true;
            }
        }
        return false;
    }

    private static boolean isPrintableAscii(int c) {
        return c >= 0x20 && c < 0x7F;
    }

    private static boolean isLiteral(int c) {
        return isPrintableAscii(c) && c != '%';
    }

    /** The bytes the text from {@code from} stands for; empty when it is not printable ASCII with well-formed escapes. */
    private static Optional<byte[]> unescape(String text, int from) {
        var out = new ByteArrayOutputStream();
        int i = from;
        while (i < text.length()) {
            char c = text.charAt(i);
            if (!isPrintableAscii(c)) {
                return Optional.empty();
            }
            if (c != '%') {
                out.write(c);
                i++;
                continue;
            }
            int hi = i + 2 < text.length() ? hex(text.charAt(i + 1)) : -1;
            int lo = i + 2 < text.length() ? hex(text.charAt(i + 2)) : -1;
            if (hi < 0 || lo < 0) {
                return Optional.empty();
            }
            out.write(hi << 4 | lo);
            i += 3;
        }
        return Optional.of(out.toByteArray());
    }

    /** The value of an ASCII hex digit, or -1. */
    private static int hex(char c) {
        return c < 128 ? Character.digit(c, 16) : -1;
    }
}
