package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.math.BigDecimal;
import java.math.BigInteger;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.regex.Pattern;

/**
 * Brings a tool call's argument values to the JSON Schema types the tool declares, when the
 * conversion loses nothing. Models often send a number as a string ("8" for an integer) or a
 * list as one comma-separated string; the tool server rejects those and the agent burns turns
 * retrying. Conversions:
 * <ul>
 *   <li>a numeric string (or an integral decimal) to an integer; a numeric string to a number</li>
 *   <li>"true" / "false" to a boolean</li>
 *   <li>a comma-separated string to an array when the items are plain strings</li>
 *   <li>a single value to a one-item array</li>
 * </ul>
 * Objects and arrays are walked recursively. Everything else is left as sent, so the server's
 * own error comes back unchanged. A property marked secret (writeOnly, format password, or a
 * secret / x-secret flag) is never touched. The debug log names the tool and the field, never a
 * value.
 */
final class ToolArgumentCoercion {

    private static final Logger log = LoggerFactory.getLogger(ToolArgumentCoercion.class);
    private static final String TYPE = "type";
    private static final String ITEMS = "items";
    private static final String PROPERTIES = "properties";
    private static final String STRING = "string";
    private static final String ARRAY = "array";
    private static final String OBJECT = "object";
    private static final List<String> SECRET_FLAGS = List.of("writeOnly", "secret", "x-secret");
    private static final Pattern NUMERIC = Pattern.compile("[+-]?\\d+(\\.\\d+)?([eE][+-]?\\d+)?");

    private ToolArgumentCoercion() {
    }

    /**
     * The request with the arguments of a tools/call coerced to the named tool's input schema;
     * the same instance when nothing changes, and for any other method or an unknown tool.
     */
    static McpMessage coerceCall(McpMessage request, List<ToolInfo> tools) {
        Map<String, Object> params = request.params();
        if (tools == null || params == null || !"tools/call".equals(request.method())) {
            return request;
        }
        if (!(params.get("name") instanceof String name) || !(params.get("arguments") instanceof Map<?, ?> arguments)) {
            return request;
        }
        Map<String, Object> schema = tools.stream().filter(t -> name.equals(t.name()))
            .map(ToolInfo::inputSchema).filter(Objects::nonNull).findFirst().orElse(null);
        if (schema == null) {
            return request;
        }
        Map<?, ?> coerced = coerceObject(name, "", arguments, schema);
        if (coerced == arguments) {
            return request;
        }
        Map<String, Object> changed = new LinkedHashMap<>(params);
        changed.put("arguments", coerced);
        return request.withParams(changed);
    }

    private static Map<?, ?> coerceObject(String tool, String path, Map<?, ?> value, Map<?, ?> schema) {
        if (!(schema.get(PROPERTIES) instanceof Map<?, ?> properties)) {
            return value;
        }
        Map<Object, Object> result = new LinkedHashMap<>();
        boolean changed = false;
        for (Map.Entry<?, ?> entry : value.entrySet()) {
            Object before = entry.getValue();
            Object after = properties.get(entry.getKey()) instanceof Map<?, ?> propertySchema
                ? coerceValue(tool, join(path, entry.getKey()), before, propertySchema) : before;
            changed |= after != before;
            result.put(entry.getKey(), after);
        }
        return changed ? result : value;
    }

    private static Object coerceValue(String tool, String field, Object value, Map<?, ?> declared) {
        Map<?, ?> schema = effective(declared);
        if (value == null || isSecret(schema)) {
            return value;
        }
        List<String> types = typesOf(schema);
        if (types.stream().anyMatch(type -> matches(value, type))) {
            return descend(tool, field, value, schema);
        }
        for (String type : types) {
            Object converted = convert(tool, field, value, type, schema);
            if (converted != null) {
                log.debug("Coerced argument {} of tool {} to {}", field, tool, type);
                return descend(tool, field, converted, schema);
            }
        }
        return value;
    }

    private static Object descend(String tool, String field, Object value, Map<?, ?> schema) {
        if (value instanceof Map<?, ?> map) {
            return coerceObject(tool, field, map, schema);
        }
        if (value instanceof List<?> list && schema.get(ITEMS) instanceof Map<?, ?> items) {
            return coerceItems(tool, field + "[]", list, items);
        }
        return value;
    }

    private static List<?> coerceItems(String tool, String field, List<?> list, Map<?, ?> items) {
        List<Object> result = new ArrayList<>(list.size());
        boolean changed = false;
        for (Object item : list) {
            Object after = coerceValue(tool, field, item, items);
            changed |= after != item;
            result.add(after);
        }
        return changed ? result : list;
    }

    /** The converted value, or null when the value cannot be converted to the type without loss. */
    private static Object convert(String tool, String field, Object value, String type, Map<?, ?> schema) {
        return switch (type) {
            case "integer" -> toInteger(value);
            case "number" -> toNumber(value);
            case "boolean" -> toBoolean(value);
            case ARRAY -> toArray(tool, field, value, schema);
            default -> null;
        };
    }

    private static Object toInteger(Object value) {
        BigDecimal decimal = decimalOf(value);
        if (decimal == null) {
            return null;
        }
        try {
            return decimal.longValueExact();
        } catch (ArithmeticException e) {
            return null;
        }
    }

    private static Object toNumber(Object value) {
        BigDecimal decimal = value instanceof String ? decimalOf(value) : null;
        if (decimal == null) {
            return null;
        }
        Object whole = toInteger(value);
        if (whole != null) {
            return whole;
        }
        double number = decimal.doubleValue();
        boolean exact = Double.isFinite(number) && BigDecimal.valueOf(number).compareTo(decimal) == 0;
        return exact ? (Object) number : null;
    }

    private static BigDecimal decimalOf(Object value) {
        if (value instanceof String text && NUMERIC.matcher(text.trim()).matches()) {
            return new BigDecimal(text.trim());
        }
        if (value instanceof Double || value instanceof Float) {
            double number = ((Number) value).doubleValue();
            return Double.isFinite(number) ? BigDecimal.valueOf(number) : null;
        }
        return null;
    }

    private static Object toBoolean(Object value) {
        if (value instanceof String text) {
            String lower = text.trim().toLowerCase(Locale.ROOT);
            if ("true".equals(lower) || "false".equals(lower)) {
                return Boolean.valueOf(lower);
            }
        }
        return null;
    }

    private static Object toArray(String tool, String field, Object value, Map<?, ?> schema) {
        Map<?, ?> items = schema.get(ITEMS) instanceof Map<?, ?> declared ? effective(declared) : Map.of();
        if (isSecret(items)) {
            return null;
        }
        if (value instanceof String text) {
            return fromString(text, items, tool, field);
        }
        return wrap(tool, field, value, items);
    }

    private static Object fromString(String text, Map<?, ?> items, String tool, String field) {
        String trimmed = text.trim();
        if (trimmed.isEmpty() || trimmed.startsWith("[") || trimmed.startsWith("{")) {
            return null;
        }
        if (!wantsPlainStrings(items)) {
            return wrap(tool, field, text, items);
        }
        List<String> parts = new ArrayList<>();
        for (String part : trimmed.split(",", -1)) {
            if (part.trim().isEmpty()) {
                return null;
            }
            parts.add(part.trim());
        }
        return parts;
    }

    private static Object wrap(String tool, String field, Object value, Map<?, ?> items) {
        Object item = coerceValue(tool, field + "[]", value, items);
        boolean fits = typesOf(items).isEmpty() || typesOf(items).stream().anyMatch(type -> matches(item, type));
        return fits ? List.of(item) : null;
    }

    private static boolean wantsPlainStrings(Map<?, ?> items) {
        return STRING.equals(items.get(TYPE)) && !items.containsKey("format")
            && !items.containsKey("enum") && !items.containsKey("const");
    }

    private static boolean matches(Object value, String type) {
        return switch (type) {
            case STRING -> value instanceof String;
            case "integer" -> isInteger(value);
            case "number" -> value instanceof Number;
            case "boolean" -> value instanceof Boolean;
            case ARRAY -> value instanceof List;
            case OBJECT -> value instanceof Map;
            default -> false;
        };
    }

    private static boolean isInteger(Object value) {
        return value instanceof Number
            && !(value instanceof Double || value instanceof Float || value instanceof BigDecimal);
    }

    private static List<String> typesOf(Map<?, ?> schema) {
        Object type = schema.get(TYPE);
        if (type instanceof String single) {
            return List.of(single);
        }
        if (type instanceof List<?> many) {
            return many.stream().filter(String.class::isInstance).map(String.class::cast).toList();
        }
        if (schema.containsKey(PROPERTIES)) {
            return List.of(OBJECT);
        }
        return schema.containsKey(ITEMS) ? List.of(ARRAY) : List.of();
    }

    /** A schema of the form anyOf / oneOf [X, {type: null}] is X; any other schema is itself. */
    private static Map<?, ?> effective(Map<?, ?> schema) {
        for (String key : List.of("anyOf", "oneOf")) {
            if (schema.get(key) instanceof List<?> branches) {
                List<Map<?, ?>> real = branches.stream()
                    .filter(Map.class::isInstance).<Map<?, ?>>map(Map.class::cast)
                    .filter(branch -> !"null".equals(branch.get(TYPE))).toList();
                if (real.size() == 1 && real.size() == branches.size() - 1) {
                    return merged(schema, real.get(0));
                }
            }
        }
        return schema;
    }

    private static Map<?, ?> merged(Map<?, ?> outer, Map<?, ?> branch) {
        Map<Object, Object> result = new LinkedHashMap<>(outer);
        result.putAll(branch);
        return result;
    }

    private static boolean isSecret(Map<?, ?> schema) {
        return "password".equals(schema.get("format"))
            || SECRET_FLAGS.stream().anyMatch(flag -> Boolean.TRUE.equals(schema.get(flag)));
    }

    private static String join(String path, Object key) {
        return path.isEmpty() ? String.valueOf(key) : path + "." + key;
    }
}
