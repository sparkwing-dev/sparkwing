export function parseJSON(text: string): unknown {
  const value: unknown = JSON.parse(text);
  const stack: ({ keys: Set<string>; key: boolean } | undefined)[] = [];
  for (const [token] of text.matchAll(/"(?:[^"\\]|\\[\s\S])*"|[{}\[\],:]/g)) {
    const current = stack.at(-1);
    if (token === "{") stack.push({ keys: new Set(), key: true });
    else if (token === "[") stack.push(undefined);
    else if (token === "}" || token === "]") stack.pop();
    else if (current) {
      if (token === ",") current.key = true;
      else if (token === ":") current.key = false;
      else if (current.key) {
        const key: string = JSON.parse(token);
        if (current.keys.has(key)) throw new SyntaxError("duplicate JSON field");
        current.keys.add(key);
      }
    }
  }
  return value;
}
