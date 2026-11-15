package kubemoot.ai.mcpgateway.service;

import com.google.re2j.Pattern;
import com.google.re2j.PatternSyntaxException;
import io.micrometer.core.instrument.Metrics;
import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ArgumentRule;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

/**
 * The argument rules of one upstream server, compiled once when the server registers or its
 * overrides change. A rule names a tool parameter, a regular expression and the message to
 * return: a call whose string argument for that parameter contains a match is refused before it
 * is forwarded. This is the only place a rule is evaluated.
 *
 * <p>Patterns are RE2 (re2j): matching time is linear in the argument, and the dialect is the
 * one the operator validates when the MCPServer is written. Only a top-level string value is
 * checked; an absent, null, empty or non-string argument (including a string inside an object
 * or array) never matches, so the server's own validation still answers for it. Rules for a
 * parameter are tried in order, parameters in the order the override lists them, and the first
 * match answers.
 *
 * <p>A rule that cannot be used (no pattern or message, or a pattern that does not compile)
 * fails closed: it refuses every string value of its parameter with its own message, is logged
 * at error, and is counted in the {@code mcpgateway.argument_rules.invalid} metric and in
 * {@link #invalidRules()}. Admission validation makes this unreachable for an MCPServer applied
 * through the operator; it covers a gateway registered directly.
 */
final class ArgumentRules {

    private static final Logger log = LoggerFactory.getLogger(ArgumentRules.class);
    private static final ArgumentRules NONE = new ArgumentRules(Map.of(), 0);

    /** A rule; a null pattern is a rule that could not be compiled and refuses every value. */
    private record CompiledRule(Pattern pattern, String message) {
        boolean refuses(String text) {
            return pattern == null || pattern.matcher(text).find();
        }
    }

    // tool name -> parameter name -> rules in order
    private final Map<String, Map<String, List<CompiledRule>>> byTool;
    private final int invalid;

    private ArgumentRules(Map<String, Map<String, List<CompiledRule>>> byTool, int invalid) {
        this.byTool = byTool;
        this.invalid = invalid;
    }

    static ArgumentRules none() {
        return NONE;
    }

    /** How many rules could not be used and now refuse every value of their parameter. */
    int invalidRules() {
        return invalid;
    }

    /** The compiled rules of the overrides; {@link #none()} when they carry no rule. */
    static ArgumentRules compile(List<ToolOverride> overrides, String serverName) {
        if (overrides == null || overrides.isEmpty()) {
            return NONE;
        }
        Map<String, Map<String, List<CompiledRule>>> byTool = new HashMap<>();
        int[] invalid = new int[1];
        for (ToolOverride override : overrides) {
            if (override == null || override.name() == null) {
                continue;
            }
            Map<String, List<CompiledRule>> byParameter = compileTool(override, serverName, invalid);
            if (!byParameter.isEmpty()) {
                byTool.put(override.name(), byParameter);
            }
        }
        if (invalid[0] > 0) {
            Metrics.counter("mcpgateway.argument_rules.invalid", "server", serverName).increment(invalid[0]);
        }
        return byTool.isEmpty() ? NONE : new ArgumentRules(byTool, invalid[0]);
    }

    private static Map<String, List<CompiledRule>> compileTool(ToolOverride override, String serverName,
                                                               int[] invalid) {
        Map<String, List<CompiledRule>> byParameter = new LinkedHashMap<>();
        override.parameters().forEach((parameter, spec) -> {
            List<CompiledRule> rules = compileAll(spec == null ? List.of() : spec.rules(),
                serverName, override.name() + "." + parameter, invalid);
            if (!rules.isEmpty()) {
                byParameter.put(parameter, rules);
            }
        });
        return byParameter;
    }

    private static List<CompiledRule> compileAll(List<ArgumentRule> rules, String serverName, String where,
                                                 int[] invalid) {
        List<CompiledRule> compiled = new ArrayList<>();
        for (ArgumentRule rule : rules) {
            Optional<CompiledRule> one = compileOne(rule, serverName, where);
            if (one.isEmpty()) {
                invalid[0]++;
                compiled.add(new CompiledRule(null, rule == null || rule.message() == null
                    ? "This argument is refused: its argument rule is invalid." : rule.message()));
            } else {
                compiled.add(one.get());
            }
        }
        return compiled;
    }

    private static Optional<CompiledRule> compileOne(ArgumentRule rule, String serverName, String where) {
        if (rule == null || rule.reject() == null || rule.reject().isEmpty() || rule.message() == null) {
            log.error("Argument rule for {} on server {} needs a reject pattern and a message; "
                + "the parameter now refuses every string value", where, serverName);
            return Optional.empty();
        }
        try {
            return Optional.of(new CompiledRule(Pattern.compile(rule.reject()), rule.message()));
        } catch (PatternSyntaxException e) {
            log.error("Argument rule for {} on server {} has a pattern that does not compile ({}); "
                + "the parameter now refuses every string value", where, serverName, e.getDescription());
            return Optional.empty();
        }
    }

    /** The message of the first rule the call's arguments break, or empty when the call may go. */
    Optional<String> check(String tool, Map<?, ?> arguments) {
        Map<String, List<CompiledRule>> byParameter = byTool.get(tool);
        if (byParameter == null || arguments == null) {
            return Optional.empty();
        }
        return byParameter.entrySet().stream()
            .map(entry -> firstMatch(entry.getValue(), arguments.get(entry.getKey())))
            .flatMap(Optional::stream)
            .findFirst();
    }

    private static Optional<String> firstMatch(List<CompiledRule> rules, Object value) {
        if (!(value instanceof String text) || text.isEmpty()) {
            return Optional.empty();
        }
        return rules.stream().filter(rule -> rule.refuses(text)).map(CompiledRule::message).findFirst();
    }
}
