package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Whether the figures an answer states occur in the data behind it. {@link AnswerGrounding} does
 * this for instance names; this does it for numbers, for a finding that must come from a tool a
 * model ran.
 *
 * <p>A figure here is a number of two or more digits, or any number with a decimal point, standing
 * alone (not part of a name, a version, a path or a list marker at the start of a line). A figure
 * is grounded when a number in one of the sources equals it, or rounds to it at the decimals the
 * answer wrote (33.3 for 33.333, 100 for 99.6), thousands separators and signs ignored. Sources
 * are scanned for every number, wherever it stands (compact JSON, ranges, ratios). The check is
 * code, from the text alone; a figure that is a unit conversion or a tally of source numbers is
 * not matched, so the answer has to compute it with a tool.
 */
public final class NumberGrounding {

    /** Digits with or without thousands separators, then an optional decimal part. */
    private static final String NUMBER_BODY = "(?:\\d{1,3}(?:,\\d{3})+|\\d+)(?:\\.\\d+)?";

    /** A number standing alone; thousands separators allowed, a unit may follow (50GiB). */
    private static final Pattern NUMBER = Pattern.compile("(?<![\\w.:/-])" + NUMBER_BODY);

    /** Every number of a source, wherever it stands: after a colon in compact JSON, in a range or a ratio. */
    private static final Pattern ANY_NUMBER = Pattern.compile(NUMBER_BODY);

    /** A list marker ("12. ", "3) ") at the start of a line, which numbers an item and states no figure. */
    private static final Pattern LIST_MARKER = Pattern.compile("(?m)^[ \\t]*\\d+[.)][ \\t]");

    /** Most characters of sources read; the rest is not searched. */
    private static final int MAX_SOURCE_CHARS = 8_000_000;

    private NumberGrounding() {}

    /** The figures {@code text} states, once each as written, in order. */
    public static List<String> figuresIn(String text) {
        if (text == null) {
            return List.of();
        }
        Set<String> figures = new LinkedHashSet<>();
        Matcher m = NUMBER.matcher(LIST_MARKER.matcher(text).replaceAll(" "));
        while (m.find()) {
            String figure = m.group();
            if (isFigure(figure)) {
                figures.add(figure);
            }
        }
        return new ArrayList<>(figures);
    }

    /** The figures of {@code draft} that no number of any of {@code sources} equals or rounds to. */
    public static List<String> ungrounded(String draft, List<String> sources) {
        List<String> figures = figuresIn(draft);
        if (figures.isEmpty()) {
            return figures;
        }
        double[] known = numbersIn(sources);
        List<String> missing = new ArrayList<>();
        for (String figure : figures) {
            if (!isNear(known, figure)) {
                missing.add(figure);
            }
        }
        return missing;
    }

    /** The re-prompt: the figures the draft states that no result it ran holds. */
    public static String reprompt(List<String> figures) {
        return "Your draft states figures that appear in no result you ran: " + AnswerGrounding.list(figures)
                + ".\nRun a tool that computes them (execute_code, or workspace_query over the stored data) and "
                + "report what it returns. If the data does not hold what the question needs, reply with "
                + "NO_DATA and say what is missing. Do not give an example or illustrative value.";
    }

    /** What the finding becomes when the figures stay ungrounded: nothing is stated, and it says why. */
    public static String withheld(List<String> figures) {
        return "NO_DATA: could not compute this from the data. The figures in the draft ("
                + AnswerGrounding.list(figures) + ") appear in no result that was run, so none is reported.";
    }

    private static boolean isFigure(String figure) {
        return figure.indexOf('.') >= 0 || digitCount(figure) >= 2;
    }

    private static int digitCount(String figure) {
        int n = 0;
        for (int i = 0; i < figure.length(); i++) {
            if (Character.isDigit(figure.charAt(i))) {
                n++;
            }
        }
        return n;
    }

    private static double parse(String figure) {
        return Double.parseDouble(figure.replace(",", ""));
    }

    /** Every number of the sources, sorted. */
    private static double[] numbersIn(List<String> sources) {
        List<Double> all = new ArrayList<>();
        int budget = MAX_SOURCE_CHARS;
        for (String source : sources) {
            if (source == null || budget <= 0) {
                continue;
            }
            String text = source.length() > budget ? source.substring(0, budget) : source;
            budget -= text.length();
            Matcher m = ANY_NUMBER.matcher(text);
            while (m.find()) {
                all.add(parse(m.group()));
            }
        }
        double[] out = all.stream().mapToDouble(Double::doubleValue).toArray();
        Arrays.sort(out);
        return out;
    }

    /** True when a known number is within half a unit of the figure's last written decimal. */
    private static boolean isNear(double[] known, String figure) {
        int dot = figure.indexOf('.');
        int decimals = dot < 0 ? 0 : figure.length() - dot - 1;
        double half = 0.5 * Math.pow(10, -decimals) + 1e-9;
        double value = parse(figure);
        int at = Arrays.binarySearch(known, value - half);
        int from = at >= 0 ? at : -at - 1;
        return from < known.length && known[from] <= value + half;
    }
}
