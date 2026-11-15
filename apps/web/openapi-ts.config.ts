import { defineConfig } from "@hey-api/openapi-ts";

// openapi.yaml is the checked-in wire contract; generated files are never edited.
export default defineConfig({
  input: "../api/openapi.yaml",
  output: { path: process.env.METRIC_OPENAPI_OUTPUT || "src/generated/api" },
  plugins: [
    "@hey-api/typescript",
    { name: "zod", compatibilityVersion: 4, dates: { offset: true } },
  ],
});
