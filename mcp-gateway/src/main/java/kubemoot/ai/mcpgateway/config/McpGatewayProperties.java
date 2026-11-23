package kubemoot.ai.mcpgateway.config;

import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.util.unit.DataSize;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;

@ConfigurationProperties(prefix = "mcp.gateway")
public class McpGatewayProperties {

    private int port = 8080;
    private List<ServerConfig> servers = new ArrayList<>();
    private ToolIndex toolIndex = new ToolIndex();
    private MetaTools metaTools = new MetaTools();
    private Connections connections = new Connections();
    private Reconnect reconnect = new Reconnect();
    private CredentialBroker credentialBroker = new CredentialBroker();
    /**
     * Safety net: a connected server whose tool list is older than this is re-listed when
     * the operator next re-registers it. The primary signals are a lost session and the
     * server's notifications/tools/list_changed; this only catches a change neither reported.
     */
    private Duration toolListMaxAge = Duration.ofMinutes(10);
    /**
     * The largest reply the gateway reads from an MCP server for one request. A reply over this
     * is answered with a tool error naming the size and this limit. 4MB covers the Proxmox RRD
     * day window (about 0.6 MB) and a Prometheus targets list (over 1 MB) with room to spare,
     * and stays above the 1 MiB cap the mcp-bridge sidecar applies to stdio servers.
     */
    private DataSize maxResponseSize = DataSize.ofMegabytes(4);

    public DataSize getMaxResponseSize() {
        return maxResponseSize;
    }

    public void setMaxResponseSize(DataSize maxResponseSize) {
        this.maxResponseSize = maxResponseSize;
    }

    /** The limit in bytes; fails startup with the key name when it is not between 1 byte and 2 GB. */
    public int maxResponseBytes() {
        if (maxResponseSize == null || maxResponseSize.toBytes() <= 0
                || maxResponseSize.toBytes() > Integer.MAX_VALUE) {
            throw new IllegalStateException("mcp.gateway.max-response-size must be a size between 1B and 2GB,"
                + " for example 4MB, but is " + maxResponseSize);
        }
        return (int) maxResponseSize.toBytes();
    }

    public Duration getToolListMaxAge() {
        return toolListMaxAge;
    }

    public void setToolListMaxAge(Duration toolListMaxAge) {
        this.toolListMaxAge = toolListMaxAge;
    }

    public CredentialBroker getCredentialBroker() {
        return credentialBroker;
    }

    public void setCredentialBroker(CredentialBroker credentialBroker) {
        this.credentialBroker = credentialBroker;
    }

    public Reconnect getReconnect() {
        return reconnect;
    }

    public void setReconnect(Reconnect reconnect) {
        this.reconnect = reconnect;
    }

    public Connections getConnections() {
        return connections;
    }

    public void setConnections(Connections connections) {
        this.connections = connections;
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

    /**
     * How long a request waits out after a failed connect before it may start another. The
     * backoff doubles with each consecutive failure up to the maximum and ends the moment a
     * connect succeeds. It limits how often a down server is tried; it is not what makes a
     * server usable again: the next request after the backoff connects.
     */
    public static class Reconnect {
        private Duration initialBackoff = Duration.ofSeconds(1);
        private Duration maxBackoff = Duration.ofSeconds(30);

        /** The backoff after the given number of consecutive failed connects (1 for the first). */
        public Duration backoffAfter(int failedConnects) {
            if (initialBackoff == null || initialBackoff.isNegative() || maxBackoff == null
                    || maxBackoff.isNegative()) {
                throw new IllegalStateException("mcp.gateway.reconnect.initial-backoff and max-backoff"
                    + " must be durations of zero or more, for example 1s and 30s");
            }
            long doublings = Math.max(0, Math.min(failedConnects - 1, 20));
            Duration backoff = initialBackoff.multipliedBy(1L << doublings);
            return backoff.compareTo(maxBackoff) > 0 ? maxBackoff : backoff;
        }

        public Duration getInitialBackoff() {
            return initialBackoff;
        }

        public void setInitialBackoff(Duration initialBackoff) {
            this.initialBackoff = initialBackoff;
        }

        public Duration getMaxBackoff() {
            return maxBackoff;
        }

        public void setMaxBackoff(Duration maxBackoff) {
            this.maxBackoff = maxBackoff;
        }
    }

    /**
     * The pool of HTTP connections the gateway keeps open to MCP servers. A hosted server's
     * edge closes idle connections, so the pool drops its own first: a request never starts on
     * a connection the remote side already closed.
     */
    public static class Connections {
        private Duration maxIdleTime = Duration.ofSeconds(15);
        private Duration evictInterval = Duration.ofSeconds(10);
        private Duration maxLifeTime = Duration.ofMinutes(5);

        /** Fails startup with the name of the offending key when a limit is not a positive duration. */
        public void validate() {
            requirePositive("max-idle-time", maxIdleTime);
            requirePositive("evict-interval", evictInterval);
            requirePositive("max-life-time", maxLifeTime);
        }

        private static void requirePositive(String key, Duration value) {
            if (value == null || value.isNegative() || value.isZero()) {
                throw new IllegalStateException("mcp.gateway.connections." + key
                    + " must be a positive duration, for example 15s, but is " + value);
            }
        }

        public Duration getMaxIdleTime() {
            return maxIdleTime;
        }

        public void setMaxIdleTime(Duration maxIdleTime) {
            this.maxIdleTime = maxIdleTime;
        }

        public Duration getEvictInterval() {
            return evictInterval;
        }

        public void setEvictInterval(Duration evictInterval) {
            this.evictInterval = evictInterval;
        }

        public Duration getMaxLifeTime() {
            return maxLifeTime;
        }

        public void setMaxLifeTime(Duration maxLifeTime) {
            this.maxLifeTime = maxLifeTime;
        }
    }

    /**
     * Where the gateway leases a person's token for a server in caller-credential mode
     * {@code Required}: the broker's base address, the file holding the gateway's projected
     * ServiceAccount token (read on every lease, because the kubelet rotates it), and how long
     * the lease may take.
     */
    public static class CredentialBroker {
        private String url = "";
        private String tokenFile = "/var/run/secrets/kubemoot/credential-broker/token";
        private Duration timeout = Duration.ofSeconds(3);
        private Duration callTimeout = Duration.ofMinutes(2);

        public String getUrl() {
            return url;
        }

        public void setUrl(String url) {
            this.url = url;
        }

        public String getTokenFile() {
            return tokenFile;
        }

        public void setTokenFile(String tokenFile) {
            this.tokenFile = tokenFile;
        }

        public Duration getTimeout() {
            return timeout;
        }

        public void setTimeout(Duration timeout) {
            this.timeout = timeout;
        }

        /** Safety net for one upstream call made with a person's token; the normal path ends sooner. */
        public Duration getCallTimeout() {
            return callTimeout;
        }

        public void setCallTimeout(Duration callTimeout) {
            this.callTimeout = callTimeout;
        }
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
