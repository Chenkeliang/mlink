#!/usr/bin/env node

import { main } from "../src/bootstrap.mjs";

try {
  process.exitCode = await main();
} catch (error) {
  console.error(`mlink bootstrap failed: ${error?.message ?? "unknown error"}`);
  process.exitCode = 1;
}
