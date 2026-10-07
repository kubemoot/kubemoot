package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ParameterOverride;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * Per-tool description overrides for an upstream server's tool list. Overrides change
 * description text only; an override for a tool or parameter the upstream does not list
 * is skipped, and {@link #warnUnmatched} reports it.
 */
final class ToolOverrides {

    private static final Logger log = LoggerFactory.getLogger(ToolOverrides.class);
    private static final String PROPERTIES = "properties";
    private static final String DESCRIPTION = "description";

    private ToolOverrides() {
    }

    /** The tools with overrides applied; the input list and its tools are not modified. */
    static List<ToolInfo> apply(List<ToolInfo> tools, List<ToolOverride> overrides) {
        if (overrides == null || overrides.isEmpty() || tools.isEmpty()) {
            return tools;
        }
        Map<String, ToolOverride> byName = byName(overrides);
        return tools.stream()
            .map(tool -> byName.containsKey(tool.name()) ? applyOne(tool, byName.get(tool.name())) : tool)
            .toList();
    }

    /** Logs each override that names a tool or parameter the tools do not have. */
    static void warnUnmatched(List<ToolInfo> tools, List<ToolOverride> overrides, String serverName) {
        if (overrides == null || tools.isEmpty()) {
            return;
        }
        Map<String, ToolInfo> toolsByName = new HashMap<>();
        tools.forEach(t -> toolsByName.put(t.name(), t));
        byName(overrides).forEach((name, override) -> {
            ToolInfo tool = toolsByName.get(name);
            if (tool == null) {
                log.warn("Tool override for {} names a tool server {} does not list, ignoring", name, serverName);
                return;
            }
            Map<String, Object> properties = properties(tool);
            override.parameters().keySet().stream()
                .filter(param -> !(properties.get(param) instanceof Map<?, ?>))
                .forEach(param -> log.warn(
                    "Tool override for {}.{} names a parameter server {} does not list, ignoring",
                    name, param, serverName));
        });
    }

    private static Map<String, ToolOverride> byName(List<ToolOverride> overrides) {
        Map<String, ToolOverride> byName = new HashMap<>();
        overrides.stream().filter(o -> o != null && o.name() != null).forEach(o -> byName.put(o.name(), o));
        return byName;
    }

    private static ToolInfo applyOne(ToolInfo tool, ToolOverride override) {
        String description = override.description() != null ? override.description() : tool.description();
        return new ToolInfo(tool.name(), description, applyParameters(tool, override.parameters()),
            tool.serverId(), tool.serverName());
    }

    private static Map<String, Object> applyParameters(ToolInfo tool, Map<String, ParameterOverride> params) {
        Map<String, Object> properties = properties(tool);
        Map<String, Object> changed = new HashMap<>(properties);
        params.entrySet().stream()
            .filter(e -> e.getValue() != null && e.getValue().description() != null)
            .filter(e -> properties.get(e.getKey()) instanceof Map<?, ?>)
            .forEach(e -> {
                Map<String, Object> parameter = stringKeyed((Map<?, ?>) properties.get(e.getKey()));
                parameter.put(DESCRIPTION, e.getValue().description());
                changed.put(e.getKey(), parameter);
            });
        if (changed.equals(properties)) {
            return tool.inputSchema();
        }
        Map<String, Object> schema = new HashMap<>(tool.inputSchema());
        schema.put(PROPERTIES, changed);
        return schema;
    }

    private static Map<String, Object> properties(ToolInfo tool) {
        Object properties = tool.inputSchema() == null ? null : tool.inputSchema().get(PROPERTIES);
        return properties instanceof Map<?, ?> map ? stringKeyed(map) : new HashMap<>();
    }

    private static Map<String, Object> stringKeyed(Map<?, ?> source) {
        Map<String, Object> copy = new HashMap<>();
        source.forEach((k, v) -> copy.put(Objects.toString(k), v));
        return copy;
    }
}
