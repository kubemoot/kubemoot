package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.core.JsonFactory;
import com.fasterxml.jackson.core.StreamReadConstraints;
import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;
import org.yaml.snakeyaml.LoaderOptions;
import org.yaml.snakeyaml.Yaml;
import org.yaml.snakeyaml.constructor.SafeConstructor;

/**
 * What a tool result is, decided once when it is stored and recorded as the object's
 * {@code Content-Type} header ({@link CallHeaders#CONTENT_TYPE}). Every reader of the workspace
 * believes the header and looks at the bytes only when it is missing.
 *
 * <p>The rule is the one in artifact-access ({@code internal/workspace/format.go}, {@code Detect}
 * and {@code contenttype.go}): JSON when the whole text is one JSON object or list; otherwise the
 * first five non-blank lines decide, in this order: a Markdown table, a kubectl table, YAML, CSV;
 * anything else is plain text. The artifact-access test and {@code ResultTypeTest} run the same
 * cases from {@code test/fixtures/result-types.json}, so the type shown on a {@code [CALL ...]}
 * line and the one a query reads cannot drift apart.
 */
final class ResultType {

    static final String JSON = "application/json";
    static final String YAML = "application/yaml";
    static final String CSV = "text/csv";
    static final String MARKDOWN_TABLE = "text/markdown";
    static final String TEXT = "text/plain";
    /** A kubectl style table is plain text with this profile. */
    static final String KUBECTL_TABLE = "text/plain; profile=kubectl-table";

    private static final int SNIFF_LINES = 5;
    private static final int MIN_CSV_FIELDS = 2;
    private static final char QUOTE = '"';
    private static final char COMMA = ',';
    private static final char NEWLINE = '\n';

    /** Nesting the Go decoders accept (encoding/json and yaml.v3 stop at 10,000); the parsers here stop earlier by default. */
    /** The characters Go's unicode.IsSpace accepts. */
    private static final String GO_SPACE = "\t\n\u000B\f\r \u0085\u00A0\u1680"
            + "\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200A"
            + "\u2028\u2029\u202F\u205F\u3000";

    private static final int MAX_NESTING = 10_000;

    /** Most bytes of text a YAML check is made on: parsing builds the whole document. Go's {@code maxYAMLBytes} is the same. */
    private static final int MAX_YAML_BYTES = 1_000_000;
    private static final int UTF8_MAX_BYTES_PER_CHAR = 3;

    private static final ObjectMapper JSON_MAPPER = jsonMapper();

    /** One column title: words joined by single spaces; titles are separated by two or more spaces. */
    private static final Pattern HEADER_CELL = Pattern.compile("\\S+(?: \\S+)*");
    private static final Pattern HEADER_TITLE = Pattern.compile("^[A-Z][A-Z0-9 _/().-]*$");
    private static final Pattern MARKDOWN_SEPARATOR =
            Pattern.compile("^\\|?\\s*:?-{2,}:?\\s*(\\|\\s*:?-{2,}:?\\s*)*\\|?$");
    private static final Pattern YAML_FIRST_LINE = Pattern.compile("^(---|- |[A-Za-z_][\\w.-]*:(\\s|$))");
    private static final Pattern TRAILING_BLANKS = Pattern.compile("[ \t\r]+$");

    private ResultType() {
    }

    private static ObjectMapper jsonMapper() {
        var factory = new JsonFactory();
        factory.setStreamReadConstraints(StreamReadConstraints.builder()
                .maxNestingDepth(MAX_NESTING).maxStringLength(Integer.MAX_VALUE)
                .maxNumberLength(Integer.MAX_VALUE).build());
        return new ObjectMapper(factory).enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);
    }

    /** A YAML reader that refuses what Go's yaml.v3 refuses: duplicate keys, unknown aliases, values that do not fit their tag. */
    private static Yaml yaml() {
        var options = new LoaderOptions();
        options.setAllowDuplicateKeys(false);
        options.setCodePointLimit(Integer.MAX_VALUE);
        options.setNestingDepthLimit(MAX_NESTING);
        return new Yaml(new SafeConstructor(options));
    }

    /**
     * True for exactly what Go's {@code unicode.IsSpace} counts as space: tab, line feed, vertical
     * tab, form feed, carriage return, space, U+0085, U+00A0, U+1680, U+2000 to U+200A, U+2028,
     * U+2029, U+202F, U+205F and U+3000. (Java's own {@code isWhitespace} also counts the
     * separators U+001C to U+001F and leaves out the no-break spaces.)
     */
    private static boolean isSpace(char c) {
        return GO_SPACE.indexOf(c) >= 0;
    }

    /** The text without the leading and trailing space Go's {@code strings.TrimSpace} removes. */
    static String trim(String text) {
        int from = 0;
        int to = text.length();
        while (from < to && isSpace(text.charAt(from))) {
            from++;
        }
        while (to > from && isSpace(text.charAt(to - 1))) {
            to--;
        }
        return text.substring(from, to);
    }

    /** The content type of a result's text. */
    static String of(String text) {
        String body = text == null ? "" : text;
        return parseJson(body) != null ? JSON : ofNonJson(body);
    }

    /** The content type of a text already known not to be JSON. */
    static String ofNonJson(String text) {
        List<String> lines = leadingLines(text);
        if (lines.isEmpty()) {
            return TEXT;
        }
        if (isMarkdownTable(lines)) {
            return MARKDOWN_TABLE;
        }
        if (!kubectlTitles(lines.get(0)).isEmpty()) {
            return KUBECTL_TABLE;
        }
        if (isYaml(text, firstNonComment(lines))) {
            return YAML;
        }
        return isCsv(lines) ? CSV : TEXT;
    }

    static boolean isJson(String type) {
        return JSON.equals(type);
    }

    /** The text as a JSON object or list, or null when it is anything else. */
    static JsonNode parseJson(String text) {
        String t = trim(text);
        if (t.isEmpty() || (t.charAt(0) != '{' && t.charAt(0) != '[')) {
            return null;
        }
        try {
            return JSON_MAPPER.readTree(t);
        } catch (Exception e) {
            return null;
        }
    }

    /**
     * The column titles of a kubectl table's header line (upper-case titles separated by two or
     * more spaces, at least two of them), as printed; empty when the line is not one.
     */
    static List<String> kubectlTitles(String firstLine) {
        List<String> titles = new ArrayList<>();
        var m = HEADER_CELL.matcher(firstLine);
        while (m.find()) {
            if (!HEADER_TITLE.matcher(m.group()).matches()) {
                return List.of();
            }
            titles.add(m.group());
        }
        return titles.size() < 2 ? List.of() : titles;
    }

    /** Up to five non-blank lines from the start of the text, without trailing blanks. */
    private static List<String> leadingLines(String text) {
        List<String> out = new ArrayList<>(SNIFF_LINES);
        for (String line : text.split("\n", -1)) {
            String t = TRAILING_BLANKS.matcher(line).replaceAll("");
            if (!trim(t).isEmpty()) {
                out.add(t);
            }
            if (out.size() == SNIFF_LINES) {
                break;
            }
        }
        return out;
    }

    /** A pipe row followed by its separator row, anywhere among the leading lines. */
    private static boolean isMarkdownTable(List<String> lines) {
        for (int i = 0; i + 1 < lines.size(); i++) {
            if (trim(lines.get(i)).startsWith("|") && MARKDOWN_SEPARATOR.matcher(trim(lines.get(i + 1))).matches()) {
                return true;
            }
        }
        return false;
    }

    /** The first of the lines that does not start with {@code #}, or "" when every one does. */
    private static String firstNonComment(List<String> lines) {
        for (String line : lines) {
            if (!line.strip().startsWith("#")) {
                return line;
            }
        }
        return "";
    }

    /** YAML (after any comment lines) starts like YAML, fits the size limit, and parses as one mapping or list, or as several documents. */
    private static boolean isYaml(String text, String firstLine) {
        if (!YAML_FIRST_LINE.matcher(firstLine).find() || utf8Bytes(text) > MAX_YAML_BYTES) {
            return false;
        }
        try {
            int count = 0;
            Object only = null;
            for (Object doc : yaml().loadAll(text)) {
                if (doc != null) {
                    only = count == 0 ? doc : only;
                    count++;
                }
            }
            return count > 1 || (count == 1 && (only instanceof Map || only instanceof List));
        } catch (RuntimeException e) {
            return false;
        }
    }

    /** The length of the text in UTF-8, counted only when it could be near the limit. */
    private static long utf8Bytes(String text) {
        if (text.length() > MAX_YAML_BYTES) {
            return text.length();
        }
        if ((long) text.length() * UTF8_MAX_BYTES_PER_CHAR <= MAX_YAML_BYTES) {
            return 0;
        }
        return text.getBytes(StandardCharsets.UTF_8).length;
    }

    /** CSV: every one of the leading lines holds as many fields as the first, which holds two or more. */
    private static boolean isCsv(List<String> lines) {
        if (lines.get(0).indexOf(COMMA) < 0) {
            return false;
        }
        String text = String.join("\n", lines);
        int expected = -1;
        int i = 0;
        while (i < text.length()) {
            int[] record = readRecord(text, i);
            if (record == null || (expected >= 0 && record[1] != expected)) {
                return false;
            }
            expected = record[1];
            i = record[0];
        }
        return expected >= MIN_CSV_FIELDS;
    }

    /** Reads one record from {@code at}: the position after it and its field count; null on a bad quote. */
    private static int[] readRecord(String text, int at) {
        int i = at;
        int fields = 0;
        while (true) {
            i = text.startsWith("\"", i) ? endOfQuoted(text, i) : endOfBare(text, i);
            if (i < 0) {
                return null;
            }
            fields++;
            if (i < text.length() && text.charAt(i) == COMMA) {
                i++;
            } else {
                break;
            }
        }
        return new int[] {i < text.length() ? i + 1 : i, fields};
    }

    /** The position after an unquoted field (at a comma, line end or the end); -1 when it holds a quote. */
    private static int endOfBare(String text, int from) {
        int i = from;
        while (i < text.length() && text.charAt(i) != COMMA && text.charAt(i) != NEWLINE) {
            if (text.charAt(i) == QUOTE) {
                return -1;
            }
            i++;
        }
        return i;
    }

    /** The position after a quoted field, which must end at a comma, line end or the end; -1 otherwise. */
    private static int endOfQuoted(String text, int from) {
        int i = from + 1;
        while (true) {
            int close = text.indexOf(QUOTE, i);
            if (close < 0) {
                return -1;
            }
            if (close + 1 < text.length() && text.charAt(close + 1) == QUOTE) {
                i = close + 2;
                continue;
            }
            int after = close + 1;
            boolean ends = after == text.length() || text.charAt(after) == COMMA || text.charAt(after) == NEWLINE;
            return ends ? after : -1;
        }
    }
}
