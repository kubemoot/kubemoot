package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ArgumentRule;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Pattern;
import java.util.regex.PatternSyntaxException;

/**
 * The argument rules of one upstream server, compiled once when the server registers. A rule
 * names a tool parameter, a regular expression and the message to return: a call whose string
 * argument for that parameter contains a match is refused before it is forwarded. This is the
 * only place a rule is evaluated.
 *
 * <p>Only a string value is checked. An absent, null, empty or non-string argument never
 * matches, so the server's own validation still answers for it. Rules for a parameter are tried
 * in order and the first match answers. A pattern that does not compile is logged and skipped,
 * never a failure: the operator rejects such a pattern when the MCPServer is written, so this
 * covers a gateway registered directly.
 */
final class ArgumentRules {

    private static final Logger log = LoggerFactory.getLogger(ArgumentRules.class);
    private static final ArgumentRules NONE = new ArgumentRules(Map.of());

    private record CompiledRule(Pattern pattern, String message) {
    }

    // tool name -> parameter name -> rules in order
    private final Map<String, Map<String, List<CompiledRule>>> byTool;

    private ArgumentRules(Map<String, Map<String, List<CompiledRule>>> byTool) {
        this.byTool = byTool;
    }

    static ArgumentRules none() {
        return NONE;
    }

    /** The compiled rules of the overrides; {@link #none()} when they carry no usable rule. */
    static ArgumentRules compile(List<ToolOverride> overrides, String serverName) {
        if (overrides == null || overrides.isEmpty()) {
            return NONE;
        }
        Map<String, Map<String, List<CompiledRule>>> byTool = new HashMap<>();
        for (ToolOverride override : overrides) {
            if (override == null || override.name() == null) {
                continue;
            }
            Map<String, List<CompiledRule>> byParameter = compileTool(override, serverName);
            if (!byParameter.isEmpty()) {
                byTool.put(override.name(), byParameter);
            }
        }
        return byTool.isEmpty() ? NONE : new ArgumentRules(byTool);
    }

    private static Map<String, List<CompiledRule>> compileTool(ToolOverride override, String serverName) {
        Map<String, List<CompiledRule>> byParameter = new HashMap<>();
        override.parameters().forEach((parameter, spec) -> {
            List<CompiledRule> rules = compileAll(spec == null ? List.of() : spec.rules(),
                serverName, override.name() + "." + parameter);
            if (!rules.isEmpty()) {
                byParameter.put(parameter, rules);
            }
        });
        return byParameter;
    }

    private static List<CompiledRule> compileAll(List<ArgumentRule> rules, String serverName, String where) {
        List<CompiledRule> compiled = new ArrayList<>();
        for (ArgumentRule rule : rules) {
            if (rule == null || rule.reject() == null || rule.reject().isEmpty() || rule.message() == null) {
                log.warn("Argument rule for {} on server {} needs a reject pattern and a message, ignoring",
                    where, serverName);
                continue;
            }
            try {
                compiled.add(new CompiledRule(Pattern.compile(rule.reject()), rule.message()));
            } catch (PatternSyntaxException e) {
                log.warn("Argument rule for {} on server {} has a pattern that does not compile, ignoring: {}",
                    where, serverName, e.getDescription());
            }
        }
        return compiled;
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
        return rules.stream().filter(rule -> rule.pattern().matcher(text).find())
            .map(CompiledRule::message).findFirst();
    }
}
