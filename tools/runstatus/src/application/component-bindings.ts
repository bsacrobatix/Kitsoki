import type {
  ApplicationComponentBody,
  ApplicationComponentDescriptor,
  ApplicationComponentInputBinding,
  JSONValue,
} from "./types.js";
import type { ApplicationRuntime } from "./runtime.js";

export function componentEventListeners(
  body: ApplicationComponentBody,
  descriptor: ApplicationComponentDescriptor | undefined,
  runtime: ApplicationRuntime
): Readonly<Record<string, (...args: unknown[]) => void>> {
  const listeners: Record<string, (...args: unknown[]) => void> = {};
  for (const [event, binding] of Object.entries(body.events ?? {})) {
    listeners[event] = (...args: unknown[]) => {
      const payload = args.length <= 1 ? args[0] : args;
      try {
        const schema = descriptor?.events?.[event];
        if (!schema) throw new Error(`Component event ${event} has no declared payload schema.`);
        assertPortableSchema(schema, payload);
        const input: Record<string, JSONValue> = {};
        for (const [name, source] of Object.entries(binding.input ?? {})) {
          input[name] = resolveInput(source, payload);
        }
        void runtime.dispatch({
          action: binding.action,
          input,
          session_id: runtime.frame.session_id,
          frame_revision: runtime.frame.revision,
        });
      } catch (error) {
        runtime.reportError(error);
      }
    };
  }
  return listeners;
}

function resolveInput(source: ApplicationComponentInputBinding, payload: unknown): JSONValue {
  if (source.source === "literal") {
    if (!Object.prototype.hasOwnProperty.call(source, "value")) {
      throw new Error("Component event literal input is missing its value.");
    }
    return source.value as JSONValue;
  }
  let value = payload;
  for (const segment of source.path ?? []) {
    if (!isRecord(value) || !(segment in value)) {
      throw new Error(`Component event payload is missing ${source.path?.join(".")}.`);
    }
    value = value[segment];
  }
  if (!isJSONValue(value)) {
    throw new Error("Component event input is not JSON serializable.");
  }
  return value;
}

export function assertPortableSchema(schema: JSONValue, value: unknown, path = "$"): void {
  if (typeof schema === "boolean") {
    if (!schema) throw new Error(`${path} is denied by the component event schema.`);
    return;
  }
  if (!isRecord(schema)) throw new Error("Component event schema must be an object or boolean.");
  const supported = new Set([
    "$schema", "$id", "title", "description", "default", "examples",
    "type", "enum", "const", "required", "properties", "additionalProperties",
    "items", "contains", "prefixItems", "allOf", "anyOf", "oneOf", "not",
    "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
    "minLength", "maxLength", "pattern", "minItems", "maxItems", "uniqueItems",
    "minProperties", "maxProperties",
  ]);
  for (const keyword of Object.keys(schema)) {
    if (!supported.has(keyword)) {
      throw new Error(`Unsupported component event schema keyword ${keyword}.`);
    }
  }

  if (schema.type !== undefined && !matchesType(schema.type, value)) {
    throw new Error(`${path} does not match type ${display(schema.type)}.`);
  }
  if (Array.isArray(schema.enum) && !schema.enum.some((candidate) => equalJSON(candidate, value))) {
    throw new Error(`${path} is not one of the declared enum values.`);
  }
  if ("const" in schema && !equalJSON(schema.const, value)) {
    throw new Error(`${path} does not match the declared constant.`);
  }
  validateCompositions(schema, value, path);

  if (isRecord(value)) validateObject(schema, value, path);
  if (Array.isArray(value)) validateArray(schema, value, path);
  if (typeof value === "string") validateString(schema, value, path);
  if (typeof value === "number") validateNumber(schema, value, path);
}

function validateCompositions(schema: Readonly<Record<string, JSONValue>>, value: unknown, path: string): void {
  for (const member of asSchemas(schema.allOf)) assertPortableSchema(member, value, path);
  const anyOf = asSchemas(schema.anyOf);
  if (anyOf.length && !anyOf.some((member) => accepts(member, value, path))) {
    throw new Error(`${path} does not match anyOf.`);
  }
  const oneOf = asSchemas(schema.oneOf);
  if (oneOf.length && oneOf.filter((member) => accepts(member, value, path)).length !== 1) {
    throw new Error(`${path} does not match exactly one oneOf schema.`);
  }
  if (schema.not !== undefined && accepts(schema.not, value, path)) {
    throw new Error(`${path} matches a denied schema.`);
  }
}

function validateObject(
  schema: Readonly<Record<string, JSONValue>>,
  value: Readonly<Record<string, unknown>>,
  path: string
): void {
  const required = Array.isArray(schema.required)
    ? schema.required.filter((item): item is string => typeof item === "string")
    : [];
  for (const name of required) {
    if (!(name in value)) throw new Error(`${path}.${name} is required.`);
  }
  const properties = isRecord(schema.properties) ? schema.properties : {};
  for (const [name, member] of Object.entries(value)) {
    if (name in properties) {
      assertPortableSchema(properties[name], member, `${path}.${name}`);
    } else if (schema.additionalProperties === false) {
      throw new Error(`${path}.${name} is not an allowed property.`);
    } else if (isRecord(schema.additionalProperties) || typeof schema.additionalProperties === "boolean") {
      assertPortableSchema(schema.additionalProperties, member, `${path}.${name}`);
    }
  }
  bound(schema.minProperties, value, path, "properties", Object.keys(value).length, "min");
  bound(schema.maxProperties, value, path, "properties", Object.keys(value).length, "max");
}

function validateArray(schema: Readonly<Record<string, JSONValue>>, value: readonly unknown[], path: string): void {
  bound(schema.minItems, value, path, "items", value.length, "min");
  bound(schema.maxItems, value, path, "items", value.length, "max");
  if (schema.uniqueItems === true && new Set(value.map(display)).size !== value.length) {
    throw new Error(`${path} items must be unique.`);
  }
  const prefix = asSchemas(schema.prefixItems);
  prefix.forEach((member, index) => {
    if (index < value.length) assertPortableSchema(member, value[index], `${path}[${index}]`);
  });
  if (schema.items !== undefined) {
    for (let index = prefix.length; index < value.length; index += 1) {
      assertPortableSchema(schema.items, value[index], `${path}[${index}]`);
    }
  }
  if (schema.contains !== undefined && !value.some((member, index) =>
    accepts(schema.contains as JSONValue, member, `${path}[${index}]`))) {
    throw new Error(`${path} does not contain a matching item.`);
  }
}

function validateString(schema: Readonly<Record<string, JSONValue>>, value: string, path: string): void {
  bound(schema.minLength, value, path, "characters", [...value].length, "min");
  bound(schema.maxLength, value, path, "characters", [...value].length, "max");
  if (typeof schema.pattern === "string" && !new RegExp(schema.pattern, "u").test(value)) {
    throw new Error(`${path} does not match the declared pattern.`);
  }
}

function validateNumber(schema: Readonly<Record<string, JSONValue>>, value: number, path: string): void {
  if (typeof schema.minimum === "number" && value < schema.minimum) throw new Error(`${path} is below minimum.`);
  if (typeof schema.maximum === "number" && value > schema.maximum) throw new Error(`${path} is above maximum.`);
  if (typeof schema.exclusiveMinimum === "number" && value <= schema.exclusiveMinimum) {
    throw new Error(`${path} is below exclusiveMinimum.`);
  }
  if (typeof schema.exclusiveMaximum === "number" && value >= schema.exclusiveMaximum) {
    throw new Error(`${path} is above exclusiveMaximum.`);
  }
  if (typeof schema.multipleOf === "number" && value % schema.multipleOf !== 0) {
    throw new Error(`${path} is not a declared multiple.`);
  }
}

function matchesType(type: JSONValue, value: unknown): boolean {
  if (Array.isArray(type)) return type.some((member) => matchesType(member, value));
  switch (type) {
  case "null": return value === null;
  case "boolean": return typeof value === "boolean";
  case "number": return typeof value === "number" && Number.isFinite(value);
  case "integer": return typeof value === "number" && Number.isInteger(value);
  case "string": return typeof value === "string";
  case "array": return Array.isArray(value);
  case "object": return isRecord(value);
  default: throw new Error(`Unsupported component event schema type ${display(type)}.`);
  }
}

function bound(
  raw: JSONValue | undefined,
  _value: unknown,
  path: string,
  unit: string,
  actual: number,
  direction: "min" | "max"
): void {
  if (typeof raw !== "number") return;
  if ((direction === "min" && actual < raw) || (direction === "max" && actual > raw)) {
    throw new Error(`${path} must have ${direction === "min" ? "at least" : "at most"} ${raw} ${unit}.`);
  }
}

function accepts(schema: JSONValue, value: unknown, path: string): boolean {
  try {
    assertPortableSchema(schema, value, path);
    return true;
  } catch {
    return false;
  }
}

function asSchemas(value: JSONValue | undefined): readonly JSONValue[] {
  return Array.isArray(value) ? value : [];
}

function equalJSON(left: unknown, right: unknown): boolean {
  return display(left) === display(right);
}

function display(value: unknown): string {
  return JSON.stringify(value);
}

function isRecord(value: unknown): value is Readonly<Record<string, JSONValue>> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function isJSONValue(value: unknown, seen = new Set<object>()): value is JSONValue {
  if (value === null || typeof value === "string" || typeof value === "boolean") return true;
  if (typeof value === "number") return Number.isFinite(value);
  if (Array.isArray(value)) {
    if (seen.has(value)) return false;
    seen.add(value);
    return value.every((member) => isJSONValue(member, seen));
  }
  if (!isRecord(value)) return false;
  if (seen.has(value)) return false;
  seen.add(value);
  return Object.values(value).every((member) => isJSONValue(member, seen));
}
