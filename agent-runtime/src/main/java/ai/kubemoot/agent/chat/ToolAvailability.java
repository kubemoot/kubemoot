package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;

import java.util.Collection;
import java.util.List;

/**
 * The tools an agent declares (its Agent spec's enabled MCP tools, less its disabled ones)
 * set against the tools the runtime fetched from the gateway or servers.
 *
 * An agent that declares no tools is never short of them: an empty enabled list means
 * "every tool offered", not a requirement. How much of the declared set must be present
 * depends on the role. A tooler's answer is what its tools return, so it needs every
 * declared tool. Any other role reasons over what it is given and needs at least one.
 */
record ToolAvailability(List<String> declared, List<String> missing) {

    static ToolAvailability of(List<String> enabled, List<String> disabled, Collection<String> available) {
        var declared = enabled.stream()
                .filter(name -> McpClientService.isServerToolAllowed(name, enabled, disabled))
                .distinct().toList();
        var missing = declared.stream().filter(name -> !available.contains(name)).toList();
        return new ToolAvailability(declared, missing);
    }

    boolean anyMissing() {
        return !missing.isEmpty();
    }

    /** True when the agent cannot do its job with the tools on offer. */
    boolean unmet(boolean tooler) {
        if (declared.isEmpty()) {
            return false;
        }
        return tooler ? anyMissing() : missing.size() == declared.size();
    }
}
