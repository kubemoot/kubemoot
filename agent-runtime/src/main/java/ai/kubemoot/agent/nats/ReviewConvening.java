package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.ArrayList;
import java.util.Collection;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.TreeSet;

/**
 * Which analysts the coordinator convenes for the review phase, as the crew's consensus
 * archetype declares. In {@code domain} mode an analyst reviews when its declared domains
 * touch the question's domains (named by the coordinator's own selection call) or the
 * domains of the agents that gathered the data. When no analyst matches, the fallback
 * names who reviews, so a crew that has analysts never reviews with none. In {@code all}
 * mode the coordinator's own analyst selection stands, or every analyst when it made none.
 *
 * <p>An agent's domains are the {@code domains} of its resume. An analyst that declares none is a
 * generalist (a cross-cutting reviewer such as arithmetic over any data) and reviews every
 * discussion in domain mode. Terms are compared as lower-case alphanumeric tokens.
 * Parsed with readTree only, never records or readValue (GraalVM native).
 */
final class ReviewConvening {

    /** Which analysts review. */
    enum Convene { ALL, DOMAIN }

    /** Who reviews when {@link Convene#DOMAIN} matches no analyst. */
    enum Fallback { PRIMARY, ALL }

    /** The archetype's review declaration. */
    record Policy(Convene convene, Fallback fallback) {
        static final Policy LEGACY = new Policy(Convene.ALL, Fallback.PRIMARY);
    }

    /**
     * The analysts to convene. {@code circle} is empty when every analyst is convened (review_ready
     * then carries no inner circle); {@code basis} says why, for the log and the bus message.
     */
    record Plan(List<String> circle, String basis) {}

    static final String BASIS_ALL = "all";
    static final String BASIS_SELECTED = "selected";
    static final String BASIS_DOMAIN = "domain";
    static final String BASIS_PRIMARY = "primary";
    static final String BASIS_FALLBACK_ALL = "fallback-all";

    private static final String FIELD_NAME = "name";
    private static final String FIELD_DOMAINS = "domains";
    private static final String TOKEN_SEPARATOR = "[^a-z0-9]+";
    private static final int MIN_TOKEN_CHARS = 2;

    private ReviewConvening() {}

    /** The policy from the two environment values; a missing or unknown value keeps today's behavior. */
    static Policy policy(Optional<String> convene, Optional<String> fallback) {
        var conveneMode = "domain".equalsIgnoreCase(convene.orElse("").strip()) ? Convene.DOMAIN : Convene.ALL;
        var fallbackMode = "all".equalsIgnoreCase(fallback.orElse("").strip()) ? Fallback.ALL : Fallback.PRIMARY;
        return new Policy(conveneMode, fallbackMode);
    }

    /**
     * Plans the review. In domain mode an analyst that declares no domains reviews every discussion.
     *
     * @param resumes         the crew resumes array (agents and skills)
     * @param questionDomains the domains the coordinator named for the question
     * @param gatherers       the agents that gathered data in this discussion
     * @param selected        the analysts the coordinator itself selected, possibly none
     * @param userQuery       the question, used only to rank a fallback
     */
    static Plan plan(Policy policy, JsonNode resumes, Collection<String> questionDomains,
                     Collection<String> gatherers, Collection<String> selected, String userQuery) {
        var analysts = CrewAnalysts.analysts(resumes);
        if (analysts.isEmpty()) return new Plan(List.of(), BASIS_ALL);
        var chosen = new ArrayList<String>(selected);
        if (policy.convene() == Convene.ALL) {
            return chosen.isEmpty() ? new Plan(List.of(), BASIS_ALL) : new Plan(chosen, BASIS_SELECTED);
        }
        var terms = termsByAgent(resumes);
        var context = context(questionDomains, gatherers, terms);
        var base = new LinkedHashSet<>(chosen);
        base.addAll(namesWithOverlap(analysts, terms, context));
        String basis = BASIS_DOMAIN;
        if (base.isEmpty()) {
            var fallback = fallback(policy.fallback(), analysts, terms, context, userQuery);
            if (fallback.circle().isEmpty()) return fallback;
            base.addAll(fallback.circle());
            basis = fallback.basis();
        }
        base.addAll(generalists(analysts, terms));
        return new Plan(List.copyOf(base), basis);
    }

    /** The analysts that declare no domain: they review every discussion in domain mode. */
    private static List<String> generalists(List<JsonNode> analysts, Map<String, Set<String>> terms) {
        var names = new ArrayList<String>();
        for (var node : analysts) {
            String name = node.path(FIELD_NAME).asText();
            if (terms.getOrDefault(name, Set.of()).isEmpty()) names.add(name);
        }
        return names;
    }

    private static Plan fallback(Fallback fallback, List<JsonNode> analysts, Map<String, Set<String>> terms,
                                 Set<String> context, String userQuery) {
        if (fallback == Fallback.ALL) return new Plan(List.of(), BASIS_FALLBACK_ALL);
        var withQuery = new TreeSet<>(context);
        withQuery.addAll(tokens(userQuery));
        String best = bestMatch(analysts, terms, withQuery);
        return best == null ? new Plan(List.of(), BASIS_FALLBACK_ALL) : new Plan(List.of(best), BASIS_PRIMARY);
    }

    /** The analyst sharing the most terms with {@code context}, the first by name on a tie; null when none shares any. */
    private static String bestMatch(List<JsonNode> analysts, Map<String, Set<String>> terms, Set<String> context) {
        String best = null;
        int bestScore = 0;
        for (var node : analysts.stream().sorted(Comparator.comparing(n -> n.path(FIELD_NAME).asText())).toList()) {
            String name = node.path(FIELD_NAME).asText();
            int score = overlap(terms.getOrDefault(name, Set.of()), context);
            if (score > bestScore) {
                best = name;
                bestScore = score;
            }
        }
        return best;
    }

    private static List<String> namesWithOverlap(List<JsonNode> analysts, Map<String, Set<String>> terms,
                                                 Set<String> context) {
        var names = new ArrayList<String>();
        for (var node : analysts) {
            String name = node.path(FIELD_NAME).asText();
            if (overlap(terms.getOrDefault(name, Set.of()), context) > 0) names.add(name);
        }
        return names;
    }

    private static int overlap(Set<String> agentTerms, Set<String> context) {
        int shared = 0;
        for (String term : agentTerms) {
            if (context.contains(term)) shared++;
        }
        return shared;
    }

    /**
     * The agents that gathered data in a discussion: those that agreed or raised a concern, and the
     * selected agents that are not analysts.
     */
    static Set<String> gatherers(Collection<String> agreed, Collection<String> concerned,
                                 Collection<String> selected, Set<String> analysts) {
        var names = new LinkedHashSet<String>(agreed);
        names.addAll(concerned);
        if (selected != null) {
            selected.stream().filter(name -> !analysts.contains(name)).forEach(names::add);
        }
        return names;
    }

    /** The question's domains plus the declared terms of every agent that gathered data. */
    private static Set<String> context(Collection<String> questionDomains, Collection<String> gatherers,
                                       Map<String, Set<String>> terms) {
        var context = new TreeSet<String>();
        if (questionDomains != null) questionDomains.forEach(d -> context.addAll(tokens(d)));
        if (gatherers != null) {
            gatherers.forEach(g -> context.addAll(terms.getOrDefault(g, Set.of())));
        }
        return context;
    }

    /** Each agent's terms: its declared domains. */
    static Map<String, Set<String>> termsByAgent(JsonNode resumes) {
        var byAgent = new LinkedHashMap<String, Set<String>>();
        if (resumes == null || !resumes.isArray()) return byAgent;
        for (var node : resumes) {
            String name = node.path(FIELD_NAME).asText("");
            if (name.isEmpty()) continue;
            byAgent.put(name, tokensOf(node.path(FIELD_DOMAINS)));
        }
        return byAgent;
    }

    private static Set<String> tokensOf(JsonNode array) {
        var out = new TreeSet<String>();
        if (array != null && array.isArray()) array.forEach(n -> out.addAll(tokens(n.asText(""))));
        return out;
    }

    /** Lower-case alphanumeric tokens of {@code text}, dropping one-character fragments. */
    static Set<String> tokens(String text) {
        var out = new TreeSet<String>();
        if (text == null) return out;
        for (String token : text.toLowerCase(Locale.ROOT).split(TOKEN_SEPARATOR)) {
            if (token.length() >= MIN_TOKEN_CHARS) out.add(token);
        }
        return out;
    }
}
