import type { UserSession } from "./types";

const SESSION_KEY = "agora_session";

export function getSession(): UserSession | null {
  try {
    const value = localStorage.getItem(SESSION_KEY);
    return value ? (JSON.parse(value) as UserSession) : null;
  } catch {
    return null;
  }
}

export function saveSession(session: UserSession) {
  localStorage.setItem(SESSION_KEY, JSON.stringify(session));
  window.dispatchEvent(new Event("agora-auth"));
}

export function clearSession() {
  localStorage.removeItem(SESSION_KEY);
  window.dispatchEvent(new Event("agora-auth"));
}
