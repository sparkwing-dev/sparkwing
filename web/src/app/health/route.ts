export const dynamic = "force-static";

export function GET() {
  return new Response("ok", {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
}
