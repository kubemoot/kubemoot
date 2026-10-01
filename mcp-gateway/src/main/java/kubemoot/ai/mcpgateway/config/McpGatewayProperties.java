package kubemoot.ai.mcpgateway.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;

@ConfigurationProperties(prefix = "mcp.gateway")
public class McpGatewayProperties {

    private int port = 8080;
    private List<ServerConfig> servers = new ArrayList<>();
    private ToolIndex toolIndex = new ToolIndex();
    private MetaTools metaTools = new MetaTools();
    /**
     * Safety net: a connected server whose tool list is older than this is re-listed when
     * the operator next re-registers it. The primary signals are a lost session and the
     * server's notifications/tools/list_changed; this only catches a change neither reported.
     */
    private Duration toolListMaxAge = Duration.ofMinutes(10);

    public Duration getToolListMaxAge() {
        return toolListMaxAge;
    }

    public void setToolListMaxAge(Duration toolListMaxAge) {
        this.toolListMaxAge = toolListMaxAge;
    }

    public int getPort() {
        return port;
    }

    public void setPort(int port) {
        this.port = port;
    }

    public List<ServerConfig> getServers() {
        return servers;
    }

    public void setServers(List<ServerConfig> servers) {
        this.servers = servers;
    }

    public ToolIndex getToolIndex() {
        return toolIndex;
    }

    public void setToolIndex(ToolIndex toolIndex) {
        this.toolIndex = toolIndex;
    }

    public MetaTools getMetaTools() {
        return metaTools;
    }

    public void setMetaTools(MetaTools metaTools) {
        this.metaTools = metaTools;
    }

    public static class ServerConfig {
        private String name;
        private String url;
        private String transport = "sse";

        public String getName() {
            return name;
        }

        public void setName(String name) {
            this.name = name;
        }

        public String getUrl() {
            return url;
        }

        public void setUrl(String url) {
            this.url = url;
        }

        public String getTransport() {
            return transport;
        }

        public void setTransport(String transport) {
            this.transport = transport;
        }
    }

    /**
     * Configuration for semantic tool search via RAGSource query service.
     */
    public static class ToolIndex {
        private boolean enabled = false;
        private String queryServiceUrl;

        public boolean isEnabled() {
            return enabled;
        }

        public void setEnabled(boolean enabled) {
            this.enabled = enabled;
        }

        public String getQueryServiceUrl() {
            return queryServiceUrl;
        }

        public void setQueryServiceUrl(String queryServiceUrl) {
            this.queryServiceUrl = queryServiceUrl;
        }
    }

    /**
     * Configuration for meta-tools (search_tools, load_tools).
     */
    public static class MetaTools {
        private boolean enabled = false;
        private int maxResultsPerSearch = 10;
        private Discovery discovery = new Discovery();

        public boolean isEnabled() {
            return enabled;
        }

        public void setEnabled(boolean enabled) {
            this.enabled = enabled;
        }

        public int getMaxResultsPerSearch() {
            return maxResultsPerSearch;
        }

        public void setMaxResultsPerSearch(int maxResultsPerSearch) {
            this.maxResultsPerSearch = maxResultsPerSearch;
        }

        public Discovery getDiscovery() {
            return discovery;
        }

        public void setDiscovery(Discovery discovery) {
            this.discovery = discovery;
        }
    }

    /**
     * Configuration for on-demand tool discovery via the mcp-catalog-discovery agent.
     */
    public static class Discovery {
        private boolean enabled = false;
        private String agentEndpoint;

        public boolean isEnabled() {
            return enabled;
        }

        public void setEnabled(boolean enabled) {
            this.enabled = enabled;
        }

        public String getAgentEndpoint() {
            return agentEndpoint;
        }

        public void setAgentEndpoint(String agentEndpoint) {
            this.agentEndpoint = agentEndpoint;
        }
    }
}
