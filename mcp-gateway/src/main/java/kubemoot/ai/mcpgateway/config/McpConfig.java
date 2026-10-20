package kubemoot.ai.mcpgateway.config;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.SerializationFeature;
import com.fasterxml.jackson.datatype.jsr310.JavaTimeModule;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.netty.http.client.HttpClient;
import reactor.netty.resources.ConnectionProvider;

@Configuration
@EnableConfigurationProperties(McpGatewayProperties.class)
public class McpConfig {

    private final McpGatewayProperties properties;

    public McpConfig(McpGatewayProperties properties) {
        this.properties = properties;
    }

    @Bean
    public ObjectMapper objectMapper() {
        ObjectMapper mapper = new ObjectMapper();
        mapper.setSerializationInclusion(JsonInclude.Include.NON_NULL);
        mapper.disable(SerializationFeature.WRITE_DATES_AS_TIMESTAMPS);
        mapper.registerModule(new JavaTimeModule());
        return mapper;
    }

    /** The pool every MCP server connection is drawn from, with the idle and life limits of the properties. */
    @Bean(destroyMethod = "dispose")
    public ConnectionProvider mcpConnectionProvider() {
        var connections = properties.getConnections();
        return ConnectionProvider.builder("mcp-gateway")
            .maxIdleTime(connections.getMaxIdleTime())
            .maxLifeTime(connections.getMaxLifeTime())
            .evictInBackground(connections.getEvictInterval())
            .build();
    }

    @Bean
    public WebClient.Builder webClientBuilder(ConnectionProvider mcpConnectionProvider) {
        return WebClient.builder()
            .clientConnector(new ReactorClientHttpConnector(HttpClient.create(mcpConnectionProvider)));
    }
}
