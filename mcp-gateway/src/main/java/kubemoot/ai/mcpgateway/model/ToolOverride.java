package kubemoot.ai.mcpgateway.model;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * Replaces the descriptions an upstream MCP server gives one of its tools. Only text
 * changes: the tool's name, parameters and behavior are untouched.
 *
 * @param name        the upstream tool this applies to
 * @param description replacement tool description, or null to keep the upstream one
 * @param parameters  replacement descriptions keyed by parameter name
 */
public record ToolOverride(String name, String description, Map<String, ParameterOverride> parameters) {

    /**
     * What the gateway changes for one parameter.
     *
     * @param description replacement parameter description, or null to keep the upstream one
     * @param rules       patterns that reject a call before it reaches the server
     */
    public record ParameterOverride(String description, List<ArgumentRule> rules) {

        public ParameterOverride {
            rules = rules == null ? List.of() : rules.stream().filter(Objects::nonNull).toList();
        }

        public ParameterOverride(String description) {
            this(description, List.of());
        }
    }

    /**
     * Rejects a call when a string argument matches a regular expression: the caller gets
     * {@code message} as the tool error and the server is not called.
     *
     * @param reject  regular expression searched anywhere in the argument text
     * @param message the tool error the caller receives
     */
    public record ArgumentRule(String reject, String message) {
    }

    public ToolOverride {
        parameters = parameters == null ? Map.of() : Collections.unmodifiableMap(new LinkedHashMap<>(parameters));
    }
}
