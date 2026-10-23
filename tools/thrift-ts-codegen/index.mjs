#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT_DIR = path.resolve(__dirname, "../..");
const CONTRACTS_DIR = path.join(ROOT_DIR, "contracts");
const OUTPUT_FILE = path.join(ROOT_DIR, "packages/api-client/src/generated.ts");

const CONTRACT_FILES = [
  "im_identity_http.thrift",
  "im_chat_http.thrift",
  "im_realtime_ws.thrift",
  "agent_control_http.thrift",
  "agent_gateway_http.thrift",
];

const HTTP_ANNOTATION_METHODS = [
  ["api.get", "GET"],
  ["api.post", "POST"],
  ["api.patch", "PATCH"],
  ["api.put", "PUT"],
  ["api.delete", "DELETE"],
];

function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/\/\/.*$/gm, "");
}

function parseAnnotations(raw) {
  const annotations = {};
  if (!raw) return annotations;
  const regex = /([a-zA-Z0-9_.]+)\s*=\s*"((?:[^"\\]|\\.)*)"/g;
  let match;
  while ((match = regex.exec(raw)) !== null) {
    annotations[match[1]] = match[2].replace(/\\"/g, '"');
  }
  return annotations;
}

function splitTrailingAnnotations(line) {
  const trimmed = line.replace(/[,;]\s*$/, "").trim();
  if (!trimmed.endsWith(")")) {
    return { declaration: trimmed, annotations: {} };
  }
  let depth = 0;
  let inQuotes = false;
  for (let i = trimmed.length - 1; i >= 0; i--) {
    const ch = trimmed[i];
    const prev = i > 0 ? trimmed[i - 1] : "";
    if (ch === '"' && prev !== "\\") {
      inQuotes = !inQuotes;
      continue;
    }
    if (inQuotes) continue;
    if (ch === ")") depth++;
    else if (ch === "(") {
      depth--;
      if (depth === 0) {
        return {
          declaration: trimmed.slice(0, i).trim(),
          annotations: parseAnnotations(trimmed.slice(i + 1, -1)),
        };
      }
    }
  }
  return { declaration: trimmed, annotations: {} };
}

function mapThriftType(thriftType) {
  const t = thriftType.trim();
  switch (t) {
    case "string":
      return "string";
    case "bool":
      return "boolean";
    case "byte":
    case "i8":
    case "i16":
    case "i32":
    case "i64":
    case "double":
      return "number";
    case "binary":
      return "Uint8Array";
    case "void":
      return "void";
    default:
      break;
  }
  const listMatch = /^list<(.+)>$/.exec(t);
  if (listMatch) {
    return `${mapThriftType(listMatch[1])}[]`;
  }
  const setMatch = /^set<(.+)>$/.exec(t);
  if (setMatch) {
    return `${mapThriftType(setMatch[1])}[]`;
  }
  const mapMatch = /^map<([^,]+),\s*(.+)>$/.exec(t);
  if (mapMatch) {
    return `Record<${mapThriftType(mapMatch[1])}, ${mapThriftType(mapMatch[2])}>`;
  }
  return t;
}

function parseThriftFile(filename) {
  const fullPath = path.join(CONTRACTS_DIR, filename);
  const raw = fs.readFileSync(fullPath, "utf8");
  const clean = stripComments(raw);

  const nsMatch = /^\s*namespace\s+go\s+([a-zA-Z0-9_.]+)/m.exec(clean);
  const namespace = nsMatch ? nsMatch[1] : filename.replace(/\.thrift$/, "");

  const structs = [];
  const structRegex = /struct\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*\{([\s\S]*?)\}/g;
  let structMatch;
  while ((structMatch = structRegex.exec(clean)) !== null) {
    const structName = structMatch[1];
    const body = structMatch[2];
    const fields = [];
    for (const rawLine of body.split("\n")) {
      const line = rawLine.trim();
      if (!line) continue;
      const { declaration, annotations } = splitTrailingAnnotations(line);
      const fieldMatch =
        /^(\d+)\s*:\s*(?:(required|optional)\s+)?(.+?)\s+([a-zA-Z_][a-zA-Z0-9_]*)$/.exec(
          declaration,
        );
      if (!fieldMatch) {
        throw new Error(
          `Failed to parse struct field in ${filename} (${structName}): ${rawLine}`,
        );
      }
      const [, idStr, optionality, thriftType, rawFieldName] = fieldMatch;
      const fieldName =
        annotations["api.body"] ||
        annotations["api.query"] ||
        annotations["api.path"] ||
        rawFieldName;
      fields.push({
        id: Number(idStr),
        name: fieldName,
        optional: optionality === "optional",
        tsType: mapThriftType(thriftType),
        annotations,
      });
    }
    structs.push({ name: structName, namespace, filename, fields });
  }

  const routes = [];
  const serviceRegex = /service\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*\{([\s\S]*?)\}/g;
  let serviceMatch;
  while ((serviceMatch = serviceRegex.exec(clean)) !== null) {
    const serviceName = serviceMatch[1];
    const body = serviceMatch[2];
    for (const rawLine of body.split("\n")) {
      const line = rawLine.trim();
      if (!line) continue;
      const { declaration, annotations } = splitTrailingAnnotations(line);
      const methodMatch =
        /^([a-zA-Z_][a-zA-Z0-9_]*)\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*\(\s*\d+\s*:\s*([a-zA-Z_][a-zA-Z0-9_]*)\s+[a-zA-Z_][a-zA-Z0-9_]*\s*\)$/.exec(
          declaration,
        );
      if (!methodMatch) {
        throw new Error(
          `Failed to parse service method in ${filename} (${serviceName}): ${rawLine}`,
        );
      }
      const [, responseType, methodName, requestType] = methodMatch;
      let httpMethod = null;
      let routePath = null;
      for (const [annotationKey, verb] of HTTP_ANNOTATION_METHODS) {
        if (annotations[annotationKey]) {
          httpMethod = verb;
          routePath = annotations[annotationKey];
          break;
        }
      }
      if (httpMethod && routePath) {
        const clientPath = routePath.startsWith("/api/v1")
          ? routePath.slice("/api/v1".length) || "/"
          : routePath;
        routes.push({
          service: serviceName,
          namespace,
          name: methodName,
          method: httpMethod,
          path: routePath,
          clientPath,
          requestType,
          responseType,
        });
      }
    }
  }

  return { filename, namespace, structs, routes };
}

function renderStructBody(fields) {
  if (fields.length === 0) {
    return "{}";
  }
  const lines = fields.map(
    (f) => `  ${f.name}${f.optional ? "?" : ""}: ${f.tsType};`,
  );
  return `{\n${lines.join("\n")}\n}`;
}

function namespacePrefix(namespace) {
  return namespace
    .split(/[._-]/)
    .map((segment) => segment.charAt(0).toUpperCase() + segment.slice(1))
    .join("");
}

function generate() {
  const parsedFiles = CONTRACT_FILES.map(parseThriftFile);
  const emittedStructs = new Map();
  const outputLines = [
    "// Code generated by tools/thrift-ts-codegen from contracts/*.thrift. DO NOT EDIT.",
    "/* eslint-disable @typescript-eslint/no-empty-object-type */",
    "",
  ];

  for (const file of parsedFiles) {
    outputLines.push(`// ============================================================================`);
    outputLines.push(`// Source: contracts/${file.filename} (namespace ${file.namespace})`);
    outputLines.push(`// ============================================================================`);
    outputLines.push("");

    const prefix = namespacePrefix(file.namespace);
    for (const struct of file.structs) {
      const body = renderStructBody(struct.fields);
      const existing = emittedStructs.get(struct.name);
      if (!existing) {
        emittedStructs.set(struct.name, { body, namespace: file.namespace });
        outputLines.push(`export interface ${struct.name} ${body}`);
        outputLines.push("");
      } else if (existing.body !== body) {
        const prefixedName = `${prefix}${struct.name}`;
        emittedStructs.set(prefixedName, { body, namespace: file.namespace });
        outputLines.push(`export interface ${prefixedName} ${body}`);
        outputLines.push("");
      }
    }
  }

  outputLines.push("// ============================================================================");
  outputLines.push("// Typed IDL Route Metadata");
  outputLines.push("// ============================================================================");
  outputLines.push("");
  outputLines.push("export const IDL_ROUTES = {");

  for (const file of parsedFiles) {
    for (const route of file.routes) {
      outputLines.push(`  ${route.name}: {`);
      outputLines.push(`    service: ${JSON.stringify(route.service)},`);
      outputLines.push(`    namespace: ${JSON.stringify(route.namespace)},`);
      outputLines.push(`    method: ${JSON.stringify(route.method)},`);
      outputLines.push(`    path: ${JSON.stringify(route.path)},`);
      outputLines.push(`    clientPath: ${JSON.stringify(route.clientPath)},`);
      outputLines.push(`    requestType: ${JSON.stringify(route.requestType)},`);
      outputLines.push(`    responseType: ${JSON.stringify(route.responseType)},`);
      outputLines.push("  },");
    }
  }

  outputLines.push("} as const;");
  outputLines.push("");
  outputLines.push("export type IdlRouteKey = keyof typeof IDL_ROUTES;");
  outputLines.push("export type IdlRoute = (typeof IDL_ROUTES)[IdlRouteKey];");
  outputLines.push("");

  fs.mkdirSync(path.dirname(OUTPUT_FILE), { recursive: true });
  fs.writeFileSync(OUTPUT_FILE, outputLines.join("\n"), "utf8");
}

generate();
