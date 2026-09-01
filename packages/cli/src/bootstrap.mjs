import { createHash, randomUUID } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { homedir, platform, arch, tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MAX_ARCHIVE_BYTES = 100 * 1024 * 1024;

export function selectAsset(release, operatingSystem = platform(), architecture = arch()) {
  const key = `${operatingSystem}-${architecture}`;
  const asset = release?.assets?.[key];
  if (!asset?.name) {
    throw new Error(`unsupported platform: ${key}`);
  }
  return asset;
}

export function normalizeNativeArgs(args) {
  return args.length === 1 && args[0] === "setup" ? [] : args;
}

export function verifyDigest(data, expected) {
  const actual = createHash("sha256").update(data).digest("hex");
  if (!/^[a-f0-9]{64}$/.test(expected ?? "") || actual !== expected) {
    throw new Error("MLink release checksum mismatch");
  }
}

async function loadReleaseManifest() {
  const path = fileURLToPath(new URL("../release.json", import.meta.url));
  const release = JSON.parse(await readFile(path, "utf8"));
  if (!release.version || !release.repo || !release.tag || !release.assets) {
    throw new Error("MLink release manifest is invalid");
  }
  return release;
}

async function download(url) {
  const response = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(60_000) });
  if (!response.ok) {
    throw new Error(`MLink release download failed with HTTP ${response.status}`);
  }
  const declaredLength = Number(response.headers.get("content-length") || 0);
  if (declaredLength > MAX_ARCHIVE_BYTES) {
    throw new Error("MLink release archive is too large");
  }
  const data = Buffer.from(await response.arrayBuffer());
  if (data.length === 0 || data.length > MAX_ARCHIVE_BYTES) {
    throw new Error("MLink release archive has an invalid size");
  }
  return data;
}

async function installNative(release, asset) {
  const target = join(homedir(), ".local", "bin", "mlink");
  try {
    const current = await readFile(target);
    verifyDigest(current, asset.binary_sha256);
    return target;
  } catch (error) {
    if (error?.message === "MLink release checksum mismatch") {
      // A different installed version is replaced only after the new asset verifies.
    } else if (error?.code !== "ENOENT") {
      throw error;
    }
  }

  const url = `https://github.com/${release.repo}/releases/download/${release.tag}/${asset.name}`;
  const archive = await download(url);
  verifyDigest(archive, asset.archive_sha256);

  const scratch = await mkdtemp(join(tmpdir(), "mlink-bootstrap-"));
  try {
    const archivePath = join(scratch, asset.name);
    await writeFile(archivePath, archive, { mode: 0o600 });
    execFileSync("tar", ["-xzf", archivePath, "-C", scratch], { stdio: "ignore" });
    const candidatePath = join(scratch, "mlink");
    const candidate = await readFile(candidatePath);
    verifyDigest(candidate, asset.binary_sha256);
    await mkdir(dirname(target), { recursive: true, mode: 0o700 });
    const temporary = `${target}.${randomUUID()}.tmp`;
    await writeFile(temporary, candidate, { mode: 0o700 });
    await chmod(temporary, 0o700);
    await rename(temporary, target);
    return target;
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}

export async function main(args = process.argv.slice(2)) {
  const release = await loadReleaseManifest();
  const asset = selectAsset(release);
  const binary = await installNative(release, asset);
  const result = spawnSync(binary, normalizeNativeArgs(args), { stdio: "inherit" });
  if (result.error) {
    throw result.error;
  }
  return result.status ?? 1;
}
