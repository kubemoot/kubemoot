package kubemoot.ai.mcpgateway.service;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.read.ListAppender;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.slf4j.LoggerFactory;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ToolArgumentCoercionTest {

    private static final String INTEGER = "integer";
    private static final String BOOLEAN = "boolean";
    private static final String STRING = "string";
    private static final String NUMBER = "number";
    private static final String NUMBER_OF_ITEMS = "n_items";

    private final ListAppender<ILoggingEvent> logs = new ListAppender<>();
    private final Logger logger = (Logger) LoggerFactory.getLogger(ToolArgumentCoercion.class);
    private Level level;

    @BeforeEach
    void captureLogs() {
        level = logger.getLevel();
        logger.setLevel(Level.DEBUG);
        logs.start();
        logger.addAppender(logs);
    }

    @AfterEach
    void releaseLogs() {
        logger.detachAppender(logs);
        logger.setLevel(level);
    }

    private static Map<String, Object> prop(String type) {
        return Map.of("type", type);
    }

    private static Map<String, Object> arrayOf(Map<String, Object> items) {
        return Map.of("type", "array", "items", items);
    }

    private static Map<String, Object> schema(Object... namesAndSchemas) {
        Map<String, Object> properties = new LinkedHashMap<>();
        for (int i = 0; i < namesAndSchemas.length; i += 2) {
            properties.put((String) namesAndSchemas[i], namesAndSchemas[i + 1]);
        }
        return Map.of("type", "object", "properties", properties);
    }

    private static Map<String, Object> args(Object... namesAndValues) {
        Map<String, Object> arguments = new LinkedHashMap<>();
        for (int i = 0; i < namesAndValues.length; i += 2) {
            arguments.put((String) namesAndValues[i], namesAndValues[i + 1]);
        }
        return arguments;
    }

    /** The arguments the tool would receive after coercion. */
    @SuppressWarnings("unchecked")
    private static Map<String, Object> coerce(Map<String, Object> inputSchema, Map<String, Object> arguments) {
        ToolInfo tool = new ToolInfo("search", "d", inputSchema, "id", "srv");
        McpMessage call = McpMessage.request(1, "tools/call", Map.of("name", "search", "arguments", arguments));
        return (Map<String, Object>) ToolArgumentCoercion.coerceCall(call, List.of(tool)).params().get("arguments");
    }

    @Test
    void aNumericStringBecomesAnInteger() {
        Map<String, Object> result = coerce(schema(NUMBER_OF_ITEMS, prop(INTEGER)), args(NUMBER_OF_ITEMS, "8"));
        assertEquals(8L, result.get(NUMBER_OF_ITEMS));
    }

    @Test
    void anIntegralDecimalBecomesAnIntegerAndSpacesAreTrimmed() {
        Map<String, Object> result = coerce(schema("a", prop(INTEGER), "b", prop(INTEGER), "c", prop(INTEGER)),
            args("a", "8.0", "b", " 12 ", "c", 3.0));
        assertEquals(8L, result.get("a"));
        assertEquals(12L, result.get("b"));
        assertEquals(3L, result.get("c"));
    }

    @Test
    void aNumericStringBecomesANumber() {
        Map<String, Object> result = coerce(schema("ratio", prop(NUMBER), "count", prop(NUMBER)),
            args("ratio", "0.25", "count", "7"));
        assertEquals(0.25, result.get("ratio"));
        assertEquals(7L, result.get("count"));
    }

    @Test
    void trueAndFalseStringsBecomeBooleans() {
        Map<String, Object> result = coerce(schema("a", prop(BOOLEAN), "b", prop(BOOLEAN), "c", prop(BOOLEAN)),
            args("a", "true", "b", "False", "c", " TRUE "));
        assertEquals(true, result.get("a"));
        assertEquals(false, result.get("b"));
        assertEquals(true, result.get("c"));
    }

    @Test
    void aCommaSeparatedStringBecomesAnArrayOfStrings() {
        Map<String, Object> result = coerce(schema("content_types", arrayOf(prop(STRING))),
            args("content_types", "books,videos, courses"));
        assertEquals(List.of("books", "videos", "courses"), result.get("content_types"));
    }

    @Test
    void aSingleValueBecomesAOneItemArray() {
        Map<String, Object> result = coerce(
            schema("tags", arrayOf(prop(STRING)), "ids", arrayOf(prop(INTEGER)), "flags", arrayOf(prop(BOOLEAN))),
            args("tags", "books", "ids", 5, "flags", "true"));
        assertEquals(List.of("books"), result.get("tags"));
        assertEquals(List.of(5), result.get("ids"));
        assertEquals(List.of(true), result.get("flags"));
    }

    @Test
    void aSingleObjectBecomesAOneItemArrayOfObjects() {
        Map<String, Object> filter = Map.of("limit", "3");
        Map<String, Object> result = coerce(
            schema("filters", arrayOf(schema("limit", prop(INTEGER)))), args("filters", filter));
        assertEquals(List.of(Map.of("limit", 3L)), result.get("filters"));
    }

    @Test
    void nestedObjectsAreCoercedRecursively() {
        Map<String, Object> inner = schema("limit", prop(INTEGER), "exact", prop(BOOLEAN));
        Map<String, Object> result = coerce(schema("options", schema("page", inner)),
            args("options", args("page", args("limit", "10", "exact", "true", "other", "5"))));
        Map<?, ?> page = (Map<?, ?>) ((Map<?, ?>) result.get("options")).get("page");
        assertEquals(10L, page.get("limit"));
        assertEquals(true, page.get("exact"));
        assertEquals("5", page.get("other"), "a property the schema does not declare is untouched");
    }

    @Test
    void itemsOfAnArrayAreCoerced() {
        Map<String, Object> result = coerce(schema("ids", arrayOf(prop(INTEGER))), args("ids", List.of("1", 2, "3")));
        assertEquals(List.of(1L, 2, 3L), result.get("ids"));
    }

    @Test
    void anOptionalParameterDeclaredAsAnyOfWithNullIsCoerced() {
        Map<String, Object> optionalInt = Map.of("anyOf", List.of(prop(INTEGER), prop("null")));
        Map<String, Object> result = coerce(schema("limit", optionalInt), args("limit", "4"));
        assertEquals(4L, result.get("limit"));
    }

    @Test
    void aTypeListIsTriedInOrderAndAMatchingTypeIsKept() {
        Map<String, Object> either = Map.of("type", List.of(STRING, INTEGER));
        Map<String, Object> result = coerce(schema("v", either, "w", Map.of("type", List.of(INTEGER, "null"))),
            args("v", "8", "w", "9"));
        assertEquals("8", result.get("v"), "the value already has an allowed type");
        assertEquals(9L, result.get("w"));
    }

    @Test
    void valuesThatAlreadyHaveTheDeclaredTypeAreLeftAlone() {
        Map<String, Object> arguments = args("n", 8, "flag", true, "name", "x", "tags", List.of("a"));
        Map<String, Object> inputSchema = schema("n", prop(INTEGER), "flag", prop(BOOLEAN),
            "name", prop(STRING), "tags", arrayOf(prop(STRING)));
        assertSame(arguments, coerce(inputSchema, arguments));
    }

    @Test
    void valuesThatCannotBeConvertedWithoutLossAreSentAsTheyWere() {
        Map<String, Object> inputSchema = schema(
            "i1", prop(INTEGER), "i2", prop(INTEGER), "i3", prop(INTEGER), "i4", prop(INTEGER),
            "n1", prop(NUMBER), "n2", prop(NUMBER), "n3", prop(NUMBER),
            "b1", prop(BOOLEAN), "b2", prop(BOOLEAN),
            "a1", arrayOf(prop(STRING)), "a2", arrayOf(prop(STRING)), "a3", arrayOf(prop(STRING)),
            "a4", arrayOf(prop(INTEGER)), "o", prop("object"));
        Map<String, Object> arguments = args(
            "i1", "8.5", "i2", "eight", "i3", "99999999999999999999", "i4", "",
            "n1", "1,5", "n2", "NaN", "n3", "0.1234567890123456789012345",
            "b1", "yes", "b2", "1",
            "a1", "", "a2", "[\"a\", \"b\"]", "a3", "a,,b", "a4", "1,2",
            "o", "{\"k\": 1}");
        assertSame(arguments, coerce(inputSchema, arguments));
    }

    @Test
    void aStringIsNotSplitWhenItsItemsHaveAFormatOrEnum() {
        Map<String, Object> dated = arrayOf(Map.of("type", STRING, "format", "date"));
        Map<String, Object> enumerated = arrayOf(Map.of("type", STRING, "enum", List.of("a,b", "c")));
        Map<String, Object> result = coerce(schema("dates", dated, "kinds", enumerated),
            args("dates", "2026-01-01,2026-02-01", "kinds", "a,b"));
        assertEquals(List.of("2026-01-01,2026-02-01"), result.get("dates"), "kept whole as the single item");
        assertEquals(List.of("a,b"), result.get("kinds"));
    }

    @Test
    void aSecretPropertyIsNeverCoerced() {
        Map<String, Object> inputSchema = schema(
            "a", Map.of("type", INTEGER, "writeOnly", true),
            "b", Map.of("type", INTEGER, "format", "password"),
            "c", Map.of("type", INTEGER, "x-secret", true),
            "d", Map.of("type", "object", "writeOnly", true, "properties", Map.of("n", prop(INTEGER))));
        Map<String, Object> arguments = args("a", "1", "b", "2", "c", "3", "d", args("n", "4"));
        assertSame(arguments, coerce(inputSchema, arguments));
    }

    @Test
    void aSecretItemSchemaKeepsTheArrayValueAsSent() {
        for (Map<String, Object> flag : List.of(Map.<String, Object>of("writeOnly", true),
                Map.<String, Object>of("format", "password"), Map.<String, Object>of("secret", true),
                Map.<String, Object>of("x-secret", true))) {
            Map<String, Object> items = new LinkedHashMap<>(flag);
            items.put("type", STRING);
            Map<String, Object> arguments = args("keys", "p,w", "one", "p");
            assertSame(arguments, coerce(schema("keys", arrayOf(items), "one", arrayOf(items)), arguments));
        }
    }

    @Test
    void oneOfWithNullIsCoercedAndNonStringNumbersAreNotForcedToIntegers() {
        Map<String, Object> optionalInt = Map.of("oneOf", List.of(prop(INTEGER), prop("null")));
        assertEquals(4L, coerce(schema("limit", optionalInt), args("limit", "4")).get("limit"));
        Map<String, Object> arguments = args("a", 8.5, "b", 1.0e30, "c", "x");
        assertSame(arguments, coerce(schema("a", prop(INTEGER), "b", prop(INTEGER), "c", optionalInt), arguments));
    }

    @Test
    void whitespaceAroundASingleNonSplitItemIsKept() {
        Map<String, Object> enumerated = arrayOf(Map.of("type", STRING, "enum", List.of(" a,b ")));
        Map<String, Object> result = coerce(schema("kinds", enumerated), args("kinds", " a,b "));
        assertEquals(List.of(" a,b "), result.get("kinds"));
    }

    @Test
    void theLogNamesTheToolAndFieldsButNeverTheValues() {
        coerce(schema(NUMBER_OF_ITEMS, prop(INTEGER), "opts", schema("deep", prop(BOOLEAN))),
            args(NUMBER_OF_ITEMS, "8675309", "opts", args("deep", "true")));

        List<String> messages = logs.list.stream().map(ILoggingEvent::getFormattedMessage).toList();
        assertEquals(2, messages.size());
        assertTrue(messages.get(0).contains(NUMBER_OF_ITEMS) && messages.get(0).contains("search"));
        assertTrue(messages.get(1).contains("opts.deep"));
        assertFalse(messages.stream().anyMatch(m -> m.contains("8675309")));
    }

    @Test
    void theOriginalArgumentsAreNotModified() {
        Map<String, Object> arguments = args(NUMBER_OF_ITEMS, "8");
        coerce(schema(NUMBER_OF_ITEMS, prop(INTEGER)), arguments);
        assertEquals("8", arguments.get(NUMBER_OF_ITEMS));
    }

    @Test
    void otherMethodsUnknownToolsAndMissingSchemasPassThrough() {
        Map<String, Object> arguments = args("n", "8");
        ToolInfo tool = new ToolInfo("search", "d", schema("n", prop(INTEGER)), "id", "srv");
        McpMessage list = McpMessage.request(1, "tools/list", Map.of("name", "search", "arguments", arguments));
        McpMessage unknown = McpMessage.request(1, "tools/call", Map.of("name", "other", "arguments", arguments));
        McpMessage noArguments = McpMessage.request(1, "tools/call", Map.of("name", "search"));
        McpMessage noParams = McpMessage.request(1, "tools/call", null);
        assertSame(list, ToolArgumentCoercion.coerceCall(list, List.of(tool)));
        assertSame(unknown, ToolArgumentCoercion.coerceCall(unknown, List.of(tool)));
        assertSame(noArguments, ToolArgumentCoercion.coerceCall(noArguments, List.of(tool)));
        assertSame(noParams, ToolArgumentCoercion.coerceCall(noParams, List.of(tool)));
        assertSame(unknown, ToolArgumentCoercion.coerceCall(unknown, null));

        ToolInfo bare = new ToolInfo("search", "d", null, "id", "srv");
        McpMessage call = McpMessage.request(1, "tools/call", Map.of("name", "search", "arguments", arguments));
        assertSame(call, ToolArgumentCoercion.coerceCall(call, List.of(bare)));
        ToolInfo noProperties = new ToolInfo("search", "d", Map.of("type", "object"), "id", "srv");
        assertSame(call, ToolArgumentCoercion.coerceCall(call, List.of(noProperties)));
    }
}
