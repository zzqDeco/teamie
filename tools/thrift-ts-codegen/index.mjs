#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT_DIR = path.resolve(__dirname, "../..");
const CONTRACTS_DIR = path.join(ROOT_DIR, "contracts");
const OUTPUT_FILE = path.join(ROOT_DIR, "packages/api-client/src/generated.ts");

const CONTRACT_FILES = [
  "common/common.thrift",
  "im/http_identity.thrift",
  "im/http_chat.thrift",
  "im/ws_realtime.thrift",
  "agent/http_control.thrift",
  "agent/http_gateway.thrift",
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

export function mapThriftType(thriftType, resolveStructName = (name) => name) {
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
    return `${mapThriftType(listMatch[1], resolveStructName)}[]`;
  }
  const setMatch = /^set<(.+)>$/.exec(t);
  if (setMatch) {
    return `${mapThriftType(setMatch[1], resolveStructName)}[]`;
  }
  const mapMatch = /^map<([^,]+),\s*(.+)>$/.exec(t);
  if (mapMatch) {
    return `Record<${mapThriftType(mapMatch[1], resolveStructName)}, ${mapThriftType(mapMatch[2], resolveStructName)}>`;
  }
  return resolveStructName(t);
}

function extractReferencedStructNames(thriftType, out = new Set()) {
  const t = thriftType.trim();
  if (
    t === "string" ||
    t === "bool" ||
    t === "byte" ||
    t === "i8" ||
    t === "i16" ||
    t === "i32" ||
    t === "i64" ||
    t === "double" ||
    t === "binary" ||
    t === "void"
  ) {
    return out;
  }
  const listMatch = /^list<(.+)>$/.exec(t);
  if (listMatch) {
    return extractReferencedStructNames(listMatch[1], out);
  }
  const setMatch = /^set<(.+)>$/.exec(t);
  if (setMatch) {
    return extractReferencedStructNames(setMatch[1], out);
  }
  const mapMatch = /^map<([^,]+),\s*(.+)>$/.exec(t);
  if (mapMatch) {
    extractReferencedStructNames(mapMatch[1], out);
    extractReferencedStructNames(mapMatch[2], out);
    return out;
  }
  out.add(t);
  return out;
}

function isBrowserPublicRoute(routePath) {
  return routePath.startsWith("/api/") || routePath === "/ws" || routePath.startsWith("/ws/");
}

export function parseThriftSource(raw, filename) {
  const clean = stripComments(raw);

  const nsMatch = /^\s*namespace\s+go\s+([a-zA-Z0-9_.]+)/m.exec(clean);
  const namespace = nsMatch ? nsMatch[1] : filename.replace(/\.thrift$/, "");

  const allStructs = [];
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
        rawThriftType: thriftType.trim(),
        annotations,
      });
    }
    allStructs.push({ name: structName, namespace, filename, fields });
  }

  const routes = [];
  let hasInternalRoutes = false;
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
        if (!isBrowserPublicRoute(routePath)) {
          hasInternalRoutes = true;
          continue;
        }
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

  // If the file defines internal runtime-only routes (e.g., /internal/v1/*),
  // include only structs transitively reachable from browser-public routes.
  let structs = allStructs;
  if (hasInternalRoutes) {
    const byName = new Map(allStructs.map((s) => [s.name, s]));
    const reachable = new Set();
    const queue = [];
    for (const r of routes) {
      queue.push(r.requestType, r.responseType);
    }
    while (queue.length > 0) {
      const current = queue.shift();
      if (!current || reachable.has(current)) continue;
      const struct = byName.get(current);
      if (!struct) continue;
      reachable.add(current);
      for (const f of struct.fields) {
        for (const ref of extractReferencedStructNames(f.rawThriftType)) {
          if (!reachable.has(ref)) {
            queue.push(ref);
          }
        }
      }
    }
    structs = allStructs.filter((s) => reachable.has(s.name));
  }

  return { filename, namespace, structs, routes };
}

function parseThriftFile(filename) {
  const fullPath = path.join(CONTRACTS_DIR, filename);
  const raw = fs.readFileSync(fullPath, "utf8");
  return parseThriftSource(raw, filename);
}

function namespacePrefix(namespace) {
  return namespace
    .split(/[._-]/)
    .map((segment) => segment.charAt(0).toUpperCase() + segment.slice(1))
    .join("");
}

function renderStructDeclaration(tsName, fields, resolveStructName) {
  if (fields.length === 0) {
    return {
      signature: "type = Record<string, never>;",
      declaration: `export type ${tsName} = Record<string, never>;`,
    };
  }
  const lines = fields.map(
    (f) =>
      `  ${f.name}${f.optional ? "?" : ""}: ${mapThriftType(f.rawThriftType, resolveStructName)};`,
  );
  const body = `{\n${lines.join("\n")}\n}`;
  return {
    signature: `interface ${body}`,
    declaration: `export interface ${tsName} ${body}`,
  };
}

export function generateFromParsedFiles(parsedFiles) {
  const emittedStructs = new Map();
  const outputLines = [
    "// Code generated by tools/thrift-ts-codegen from contracts/*.thrift. DO NOT EDIT.",
    "",
  ];

  const renameMapByNamespace = new Map();

  for (const file of parsedFiles) {
    outputLines.push(`// ============================================================================`);
    outputLines.push(`// Source: contracts/${file.filename} (namespace ${file.namespace})`);
    outputLines.push(`// ============================================================================`);
    outputLines.push("");

    const prefix = namespacePrefix(file.namespace);
    const localRenameMap = new Map();
    renameMapByNamespace.set(file.namespace, localRenameMap);
    const resolveStructName = (name) => localRenameMap.get(name) || name;

    for (const struct of file.structs) {
      const candidate = renderStructDeclaration(
        struct.name,
        struct.fields,
        resolveStructName,
      );
      const existing = emittedStructs.get(struct.name);
      if (!existing) {
        localRenameMap.set(struct.name, struct.name);
        emittedStructs.set(struct.name, {
          signature: candidate.signature,
          namespace: file.namespace,
        });
        outputLines.push(candidate.declaration);
        outputLines.push("");
      } else if (existing.signature === candidate.signature) {
        localRenameMap.set(struct.name, struct.name);
      } else {
        const prefixedName = `${prefix}${struct.name}`;
        localRenameMap.set(struct.name, prefixedName);
        const prefixedRendered = renderStructDeclaration(
          prefixedName,
          struct.fields,
          resolveStructName,
        );
        emittedStructs.set(prefixedName, {
          signature: prefixedRendered.signature,
          namespace: file.namespace,
        });
        outputLines.push(prefixedRendered.declaration);
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
    const localRenameMap = renameMapByNamespace.get(file.namespace) || new Map();
    const resolveStructName = (name) => localRenameMap.get(name) || name;
    for (const route of file.routes) {
      outputLines.push(`  ${route.name}: {`);
      outputLines.push(`    service: ${JSON.stringify(route.service)},`);
      outputLines.push(`    namespace: ${JSON.stringify(route.namespace)},`);
      outputLines.push(`    method: ${JSON.stringify(route.method)},`);
      outputLines.push(`    path: ${JSON.stringify(route.path)},`);
      outputLines.push(`    clientPath: ${JSON.stringify(route.clientPath)},`);
      outputLines.push(
        `    requestType: ${JSON.stringify(resolveStructName(route.requestType))},`,
      );
      outputLines.push(
        `    responseType: ${JSON.stringify(resolveStructName(route.responseType))},`,
      );
      outputLines.push("  },");
    }
  }

  outputLines.push("} as const;");
  outputLines.push("");
  outputLines.push("export type IdlRouteKey = keyof typeof IDL_ROUTES;");
  outputLines.push("export type IdlRoute = (typeof IDL_ROUTES)[IdlRouteKey];");
  outputLines.push("");

  return outputLines.join("\n");
}

export function generateTypeScriptSDK() {
  const parsedFiles = CONTRACT_FILES.map(parseThriftFile);
  return generateFromParsedFiles(parsedFiles);
}

const isMain =
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);

if (isMain) {
  const generated = generateTypeScriptSDK();
  if (process.argv.includes("--check")) {
    const current = fs.existsSync(OUTPUT_FILE)
      ? fs.readFileSync(OUTPUT_FILE, "utf8")
      : "";
    if (current !== generated) {
      console.error(
        "packages/api-client/src/generated.ts is out of date with contracts/*.thrift. Run node tools/thrift-ts-codegen/index.mjs.",
      );
      process.exit(1);
    }
  } else {
    fs.mkdirSync(path.dirname(OUTPUT_FILE), { recursive: true });
    fs.writeFileSync(OUTPUT_FILE, generated, "utf8");
  }
}
