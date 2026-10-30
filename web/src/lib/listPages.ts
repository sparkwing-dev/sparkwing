// Lists used as complete inventories follow every cursor before returning.
export async function listPages(
  path: string,
  fields: string[],
  send: (path: string) => Promise<Response>,
): Promise<Response> {
  let combined: Record<string, unknown> | undefined;
  const rows = new Map<string, unknown[]>();
  const seen = new Set<string>();
  let pagePath = path;
  for (;;) {
    const res = await send(pagePath);
    if (!res.ok) return res;
    const body = await res.json();
    if (body === null || typeof body !== "object" || Array.isArray(body)) {
      throw new Error("Controller returned an invalid collection envelope");
    }
    combined ??= { ...body };
    for (const field of fields) {
      if (body[field] != null && !Array.isArray(body[field])) {
        throw new Error(`Invalid ${field} collection`);
      }
      rows.set(field, [...(rows.get(field) ?? []), ...(body[field] ?? [])]);
    }
    const cursor = body.next_cursor ?? "";
    if (typeof cursor !== "string" || Boolean(body.truncated) !== (cursor !== "") || seen.has(cursor)) {
      throw new Error("Controller returned inconsistent collection pagination");
    }
    if (!cursor) break;
    seen.add(cursor);
    const split = path.indexOf("?");
    const params = new URLSearchParams(split < 0 ? "" : path.slice(split + 1));
    params.set("cursor", cursor);
    pagePath = `${split < 0 ? path : path.slice(0, split)}?${params}`;
  }
  const complete = { ...combined };
  for (const [field, entries] of rows) complete[field] = entries;
  delete complete.next_cursor;
  delete complete.truncated;
  return Response.json(complete);
}
