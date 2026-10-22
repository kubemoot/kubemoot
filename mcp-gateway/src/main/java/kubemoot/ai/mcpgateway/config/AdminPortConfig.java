package kubemoot.ai.mcpgateway.config;

import org.apache.catalina.connector.Connector;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.tomcat.servlet.TomcatServletWebServerFactory;
import org.springframework.boot.web.server.WebServerFactoryCustomizer;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.Ordered;

/**
 * Opens the admin port next to the tool port and installs the filter that keeps the
 * registration routes on it. {@code mcp.gateway.admin.port} unset or 0 opens no admin
 * port, so the registration routes are not served at all.
 */
@Configuration
public class AdminPortConfig {

    @Bean
    public WebServerFactoryCustomizer<TomcatServletWebServerFactory> adminConnectorCustomizer(
            @Value("${mcp.gateway.admin.port:0}") int adminPort) {
        return factory -> {
            if (adminPort > 0) {
                Connector connector = new Connector(TomcatServletWebServerFactory.DEFAULT_PROTOCOL);
                connector.setPort(adminPort);
                factory.addAdditionalConnectors(connector);
            }
        };
    }

    @Bean
    public FilterRegistrationBean<AdminAccessFilter> adminAccessFilter(
            @Value("${mcp.gateway.admin.port:0}") int adminPort,
            @Value("${mcp.gateway.admin.token:}") String token) {
        FilterRegistrationBean<AdminAccessFilter> registration =
                new FilterRegistrationBean<>(new AdminAccessFilter(adminPort, token));
        registration.setOrder(Ordered.HIGHEST_PRECEDENCE);
        return registration;
    }
}
