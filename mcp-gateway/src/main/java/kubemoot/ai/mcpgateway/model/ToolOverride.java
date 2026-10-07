package kubemoot.ai.mcpgateway.model;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * Replaces the descriptions an upstream MCP server gives one of its tools. Only text
 * changes: the tool's name, parameters and behavior are untouched.
 *
 * @param name        the upstream tool this applies to
 * @param description replacement tool description, or null to keep the upstream one
 * @param parameters  replacement descriptions keyed by parameter name
 */
public record ToolOverride(String name, String description, Map<String, ParameterOverride> parameters) {

    public record ParameterOverride(String description) {
    }

    public ToolOverride {
        parameters = parameters == null ? Map.of() : Collections.unmodifiableMap(new LinkedHashMap<>(parameters));
    }
}
