import { NextResponse } from "next/server";
import { normalizeHostUrl, redactDeep } from "@/lib/rc/format";

type RcRequest = {
  url?: string;
  user?: string;
  pass?: string;
  path?: string;
  params?: Record<string, unknown>;
};

const RC_PATH = /^[A-Za-z0-9][A-Za-z0-9_/-]*$/;

export async function POST(request: Request) {
  let body: RcRequest;
  try {
    body = (await request.json()) as RcRequest;
  } catch {
    return NextResponse.json({ error: "请求体不是合法 JSON" }, { status: 400 });
  }

  if (!body.path || !RC_PATH.test(body.path)) {
    return NextResponse.json({ error: "非法的 RC 路径" }, { status: 400 });
  }

  let base: string;
  try {
    base = normalizeHostUrl(body.url ?? "");
  } catch (error) {
    return NextResponse.json(
      { error: error instanceof Error ? error.message : "非法主机地址" },
      { status: 400 },
    );
  }

  const target = `${base}/${body.path}`;
  const headers = new Headers({
    Accept: "application/json",
    "Content-Type": "application/json",
  });
  if (body.user) {
    headers.set("Authorization", `Basic ${Buffer.from(`${body.user}:${body.pass ?? ""}`).toString("base64")}`);
  }

  try {
    const upstream = await fetch(target, {
      method: "POST",
      headers,
      body: JSON.stringify(body.params ?? {}),
      signal: AbortSignal.timeout(15_000),
    });
    const text = await upstream.text();
    let data: unknown = {};
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        return NextResponse.json(
          { error: `rclone 返回了非 JSON 响应 (${upstream.status})` },
          { status: 502 },
        );
      }
    }
    if (!upstream.ok) {
      const err = data as { error?: string };
      return NextResponse.json(
        { error: err.error || `rclone RC 返回 ${upstream.status}` },
        { status: upstream.status },
      );
    }
    return NextResponse.json({ data: redactDeep(data) });
  } catch (error) {
    const message =
      error instanceof Error && error.name === "TimeoutError"
        ? "连接 rclone RC 超时"
        : error instanceof Error
          ? error.message
          : "连接 rclone RC 失败";
    return NextResponse.json({ error: message }, { status: 502 });
  }
}
