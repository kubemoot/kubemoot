package ai.kubemoot.agent;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertNotNull;

/**
 * Basic smoke test — verifies compilation and class loading.
 * Full integration tests require a running Ollama instance.
 */
class AgentRuntimeApplicationTests {

	@Test
	void configMappingExists() {
		assertNotNull(ai.kubemoot.agent.config.AgentProperties.class);
	}

	@Test
	void chatServiceExists() {
		assertNotNull(ai.kubemoot.agent.chat.ChatService.class);
	}
}
