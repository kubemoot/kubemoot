package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.File;
import java.util.List;
import org.junit.jupiter.api.Test;

class ResultTypeTest {

    @Test
    void sharedFixtureGivesTheSameTypesAsArtifactAccess() throws Exception {
        JsonNode cases = new ObjectMapper().readTree(new File("../test/fixtures/result-types.json")).get("cases");
        assertTrue(cases.size() > 0, "the fixture has cases");
        for (JsonNode c : cases) {
            assertEquals(c.get("type").asText(), ResultType.of(c.get("input").asText()), c.get("name").asText());
        }
    }

    /** Seeded random inputs, typed by workspace-access (see contenttype_corpus_test.go): the two detectors must agree on every one. */
    @Test
    void generatedCorpusGivesTheSameTypesAsArtifactAccess() throws Exception {
        JsonNode cases = new ObjectMapper().readTree(new File("../test/fixtures/result-types-generated.json")).get("cases");
        assertTrue(cases.size() >= 1000, "the corpus has cases");
        List<String> differences = new java.util.ArrayList<>();
        for (int i = 0; i < cases.size(); i++) {
            String input = cases.get(i).get("input").asText();
            String want = cases.get(i).get("type").asText();
            if (!want.equals(ResultType.of(input))) {
                differences.add(i + ": " + want + " vs " + ResultType.of(input) + " for " + input.replace("\n", "\\n"));
            }
        }
        assertEquals(List.of(), differences);
    }

    /** The YAML reader is the part of the detector that runs library code; the native image check starts the binary, these run it. */
    @Test
    void yamlIsReadAsAMappingAListSeveralDocumentsAndRefusedWhenItDoesNotParse() {
        assertEquals(ResultType.YAML, ResultType.of("kind: Pod\nmetadata:\n  name: a\n"));
        assertEquals(ResultType.YAML, ResultType.of("- a\n- b\n"));
        assertEquals(ResultType.YAML, ResultType.of("---\nkind: Pod\n---\nkind: Service\n"));
        assertEquals(ResultType.YAML, ResultType.of("a: &x 1\nb: *x\n"));
        assertEquals(ResultType.TEXT, ResultType.of("a: 1\na: 2\n"), "a duplicate key");
        assertEquals(ResultType.TEXT, ResultType.of("a: b: c\n"));
        assertEquals(ResultType.TEXT, ResultType.of("--- hello\n"), "a scalar is not data");
        assertEquals(ResultType.TEXT, ResultType.of("a: *missing\nb: 1\n"), "an alias with no anchor");
        assertEquals(ResultType.TEXT, ResultType.of("a: !!int abc\nb: 1\n"), "a value that does not fit its tag");
    }

    @Test
    void yamlOverTheSizeLimitIsNotTried() {
        assertEquals(ResultType.YAML, ResultType.of("a: " + "x".repeat(999_990) + "\n"));
        assertEquals(ResultType.TEXT, ResultType.of("a: " + "x".repeat(1_000_000) + "\n"));
        assertEquals(ResultType.TEXT, ResultType.of("a: " + "\u00e9".repeat(500_000) + "\n"), "the limit is in bytes");
    }

    @Test
    void spaceIsWhatGoCallsSpace() {
        assertEquals("a", ResultType.trim("\u00a0\u2003a\u3000\u0085"));
        assertEquals("\u001ca\u001c", ResultType.trim(" \u001ca\u001c "), "the separators are not space");
    }

    @Test
    void aMissingResultIsText() {
        assertEquals(ResultType.TEXT, ResultType.of(null));
        assertEquals(ResultType.TEXT, ResultType.of(""));
    }

    @Test
    void jsonIsRecognizedByItsWholeText() {
        assertTrue(ResultType.isJson(ResultType.of("[1,2]")));
        assertFalse(ResultType.isJson(ResultType.of("[1,2] tail")));
        assertFalse(ResultType.isJson(ResultType.of("\"just a string\"")));
        assertNotNull(ResultType.parseJson(" {\"a\":1}\n"));
        assertNull(ResultType.parseJson("{\"a\":"));
        assertNull(ResultType.parseJson("plain"));
    }

    @Test
    void kubectlTitlesAreTheUpperCaseCellsOfTheHeaderLine() {
        assertEquals(List.of("NAME", "NOMINATED NODE"), ResultType.kubectlTitles("NAME      NOMINATED NODE"));
        assertTrue(ResultType.kubectlTitles("NAME").isEmpty(), "one column is not a table");
        assertTrue(ResultType.kubectlTitles("NAME   ready").isEmpty(), "a lower case title is not a kubectl one");
        assertTrue(ResultType.kubectlTitles("").isEmpty());
    }

    @Test
    void onlyTheFirstFiveNonBlankLinesDecide() {
        String csvThenGarbage = "a,b\n1,2\n3,4\n5,6\n7,8\nthis line has no commas and is never read\n";
        assertEquals(ResultType.CSV, ResultType.of(csvThenGarbage));
        String sixth = "a,b\n1,2\n3,4\n5,6\n7,8\n9\n";
        assertEquals(ResultType.CSV, ResultType.of(sixth), "the sixth line is never read");
        assertEquals(ResultType.TEXT, ResultType.of("a,b\n1,2\n3\n"), "a short row inside the first five lines");
    }

    @Test
    void aSentenceWithCommasOverSeveralLinesIsText() {
        assertEquals(ResultType.TEXT, ResultType.of("Pods, services and nodes\nwere all healthy, ready, and fine.\n"));
    }

    @Test
    void kubectlTitlesSplitOnTwoSpacesOrATabAndAllowLowerCaseOnlyInParentheses() {
        assertEquals(List.of("NAME", "CPU(cores)", "MEMORY(bytes)"), ResultType.kubectlTitles("NAME  CPU(cores)\tMEMORY(bytes)"));
        assertEquals(List.of("NAME", "READY"), ResultType.kubectlTitles("NAME   READY  "));
        assertTrue(ResultType.kubectlTitles("NAME  CPU(co(res))").isEmpty(), "a nested parenthesis is not a title");
        assertTrue(ResultType.kubectlTitles("NAME  CPU(cores").isEmpty(), "an unclosed parenthesis is not a title");
        assertTrue(ResultType.kubectlTitles("NAME  1ST").isEmpty(), "a title starts with a capital letter");
        assertTrue(ResultType.kubectlTitles("   ").isEmpty());
    }

    @Test
    void aMarkdownTableNeedsADashSeparatorRowOfAtLeastTwoDashesPerColumn() {
        assertEquals(ResultType.MARKDOWN_TABLE, ResultType.of("| a | b |\n| --- | :--: |\n| 1 | 2 |\n"));
        assertEquals(ResultType.MARKDOWN_TABLE, ResultType.of("| a | b |\n|:--|--:|\n| 1 | 2 |\n"));
        assertNotEquals(ResultType.MARKDOWN_TABLE, ResultType.of("| a | b |\n| - | --- |\n| 1 | 2 |\n"));
        assertNotEquals(ResultType.MARKDOWN_TABLE, ResultType.of("| a | b |\n| --- || --- |\n| 1 | 2 |\n"));
        assertNotEquals(ResultType.MARKDOWN_TABLE, ResultType.of("| a | b |\n| -x- | --- |\n| 1 | 2 |\n"));
    }
}
