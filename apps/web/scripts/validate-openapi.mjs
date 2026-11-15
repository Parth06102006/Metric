import SwaggerParser from "@apidevtools/swagger-parser";

await SwaggerParser.validate("../api/openapi.yaml");
console.log("OpenAPI contract is valid.");
