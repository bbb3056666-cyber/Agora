import { clearSession, getSession, saveSession } from "./auth";
import type { UserSession } from "./types";

type RequestOptions = RequestInit & { auth?: boolean; retry?: boolean };

async function parseResponse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    throw new Error(data?.error || data?.message || `请求失败 (${response.status})`);
  }
  return data as T;
}

async function refreshSession(): Promise<boolean> {
  const session = getSession();
  if (!session?.refresh_token) return false;
  try {
    const response = await fetch("/account/refresh", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: session.refresh_token })
    });
    const next = await parseResponse<UserSession>(response);
    saveSession(next);
    return true;
  } catch {
    clearSession();
    return false;
  }
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { auth = false, retry = true, ...init } = options;
  const headers = new Headers(init.headers);
  if (!(init.body instanceof FormData)) headers.set("Content-Type", "application/json");
  const session = getSession();
  if (session?.token) headers.set("Authorization", `Bearer ${session.token}`);

  const response = await fetch(path, { ...init, headers });
  if (response.status === 401 && auth && retry && (await refreshSession())) {
    return request<T>(path, { ...options, retry: false });
  }
  return parseResponse<T>(response);
}

export function post<T>(path: string, body: unknown, auth = false) {
  return request<T>(path, { method: "POST", body: JSON.stringify(body), auth });
}

export function upload<T>(path: string, body: FormData) {
  return request<T>(path, { method: "POST", body, auth: true });
}
