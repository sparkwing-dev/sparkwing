import { readFileSync } from "node:fs";
import { Masker } from "../src/diagnostics.ts";

type Case = {
  Values: string[];
  Lists: string[];
  MergeValues: string[];
  Order: { Kind: string; Index: number }[];
  Input: string;
  JSONInput: unknown;
};

const cases = JSON.parse(readFileSync(0, "utf8")) as Case[];
const results = cases.map((test) => {
  const masker = new Masker();
  for (const operation of test.Order) {
    if (operation.Kind === "value") masker.register(test.Values[operation.Index]!);
    else if (operation.Kind === "list") masker.registerList(test.Lists[operation.Index]!);
    else {
      const merged = new Masker();
      merged.register(test.MergeValues[operation.Index]!);
      masker.merge(merged);
    }
  }
  return { Text: masker.mask(test.Input), JSON: masker.maskJSON(test.JSONInput) };
});
process.stdout.write(JSON.stringify(results));
