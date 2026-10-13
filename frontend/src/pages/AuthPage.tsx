import { FormEvent, useState } from "react";
import { ArrowRight, LockKeyhole, UserRound } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { post } from "../api";
import { saveSession } from "../auth";
import type { UserSession } from "../types";

export default function AuthPage() {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();

  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      if (mode === "register") {
        await post("/account/register", { username, password });
      }
      const session = await post<UserSession>("/account/login", { username, password });
      saveSession(session); navigate("/");
    } catch (e) { setError(e instanceof Error ? e.message : "操作失败"); }
    finally { setBusy(false); }
  }

  return <section className="auth-page">
    <div className="auth-copy"><span>AGORA COMMUNITY</span><h1>分享所见，<br />也听见彼此。</h1><p>一个容纳视频、图片与长文的内容社区。关注感兴趣的人，让每次打开都有新的发现。</p><div className="auth-stat"><strong>3</strong><span>种内容形态</span><strong>实时</strong><span>互动与通知</span></div></div>
    <form className="auth-form" onSubmit={submit}>
      <div className="segmented"><button type="button" className={mode === "login" ? "active" : ""} onClick={() => setMode("login")}>登录</button><button type="button" className={mode === "register" ? "active" : ""} onClick={() => setMode("register")}>注册</button></div>
      <h2>{mode === "login" ? "欢迎回来" : "创建账户"}</h2>
      <p>{mode === "login" ? "继续浏览你的内容世界。" : "用户名仅支持字母与数字，至少 3 位。"}</p>
      <label><span>用户名</span><div className="input-shell"><UserRound /><input value={username} onChange={(e) => setUsername(e.target.value)} minLength={3} maxLength={32} required autoComplete="username" /></div></label>
      <label><span>密码</span><div className="input-shell"><LockKeyhole /><input type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} maxLength={72} required autoComplete={mode === "login" ? "current-password" : "new-password"} /></div></label>
      {error && <div className="form-error">{error}</div>}
      <button className="submit-button" disabled={busy}>{busy ? "请稍候" : mode === "login" ? "登录" : "注册并登录"}<ArrowRight /></button>
    </form>
  </section>;
}
