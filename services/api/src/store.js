import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

export function emptyState() {
  return {
    accounts: {},
    workspaces: {},
    billingLedger: [],
    audit: []
  };
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

export class MemoryStore {
  constructor(initialState = emptyState()) {
    this.state = clone(initialState);
  }

  async read() {
    return clone(this.state);
  }

  async write(nextState) {
    this.state = clone(nextState);
    return this.read();
  }

  async update(mutator) {
    const nextState = await this.read();
    const result = await mutator(nextState);
    await this.write(nextState);
    return result;
  }
}

export class JsonFileStore {
  constructor(filePath) {
    this.filePath = filePath;
  }

  async read() {
    try {
      const raw = await readFile(this.filePath, "utf8");
      return { ...emptyState(), ...JSON.parse(raw) };
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      return emptyState();
    }
  }

  async write(nextState) {
    await mkdir(dirname(this.filePath), { recursive: true });
    await writeFile(this.filePath, `${JSON.stringify(nextState, null, 2)}\n`);
    return this.read();
  }

  async update(mutator) {
    const nextState = await this.read();
    const result = await mutator(nextState);
    await this.write(nextState);
    return result;
  }
}
