export function now() {
  return new Date().toISOString();
}

export function stableHash(input) {
  let hash = 0;
  for (const char of input) {
    hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  }
  return hash.toString(36).padStart(6, "0");
}

export function makeId(prefix, ...parts) {
  return `${prefix}-${stableHash(parts.join(":"))}`;
}

export function makeToken(workspaceId, sequence = "initial") {
  return `share_${stableHash(`${workspaceId}:${sequence}`)}${stableHash(`${sequence}:${workspaceId}`).slice(0, 6)}`;
}

export function money(value) {
  return Number(value.toFixed(4));
}

export function clone(value) {
  return JSON.parse(JSON.stringify(value));
}
