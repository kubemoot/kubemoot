package ai.kubemoot.agent.chat;

/** The system and user text of one no-tool call, as built once the call's window is known. */
public record SimplePrompt(String system, String user) {}
