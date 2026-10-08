import { NextRequest, NextResponse } from "next/server";

export const dynamic = "force-dynamic";
type Context = { params: Promise<{ path: string[] }> };

async function proxy(request: NextRequest, context: Context) {
  const { path } = await context.params;
  const base = (process.env.BACKEND_URL || "http://localhost:8080").replace(/\/$/, "");
  try {
    const response = await fetch(`${base}/api/${path.map(encodeURIComponent).join("/")}${request.nextUrl.search}`, {
      method: request.method,
      headers: { "Content-Type": "application/json" },
      body: request.method === "GET" ? undefined : await request.text(),
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
    return new NextResponse(await response.text(), { status: response.status, headers: { "Content-Type": response.headers.get("Content-Type") || "application/json", "Cache-Control": "no-store" } });
  } catch {
    return NextResponse.json({ error: "backend_unavailable", message: "The traffic backend is unavailable. Check the connection and try again." }, { status: 503 });
  }
}

export const GET = proxy;
export const POST = proxy;
