import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";

import { normalizeNativeArgs, readBoundedResponse, selectAsset, verifyDigest } from "../src/bootstrap.mjs";

test("selectAsset accepts only the released local platform", () => {
  const release = {
    assets: {
      "darwin-arm64": { name: "mlink_0.1.0_darwin_arm64.tar.gz", sha256: "a".repeat(64) },
    },
  };
  assert.equal(selectAsset(release, "darwin", "arm64").name, "mlink_0.1.0_darwin_arm64.tar.gz");
  assert.throws(() => selectAsset(release, "linux", "arm64"), /unsupported platform/);
});

test("setup launches the native guided TUI without inventing native flags", () => {
  assert.deepEqual(normalizeNativeArgs(["setup"]), []);
  assert.deepEqual(normalizeNativeArgs(["doctor", "--json"]), ["doctor", "--json"]);
});

test("verifyDigest rejects a different release asset", () => {
  const data = Buffer.from("native-binary");
  const digest = createHash("sha256").update(data).digest("hex");
  assert.doesNotThrow(() => verifyDigest(data, digest));
  assert.throws(() => verifyDigest(data, "0".repeat(64)), /checksum mismatch/);
});

test("readBoundedResponse stops a streamed oversized download", async () => {
  const response = new Response(new Uint8Array([1, 2, 3, 4, 5, 6]));
  await assert.rejects(() => readBoundedResponse(response, 5), /too large/);
  const accepted = await readBoundedResponse(new Response(new Uint8Array([1, 2, 3])), 5);
  assert.deepEqual([...accepted], [1, 2, 3]);
});
