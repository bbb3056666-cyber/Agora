import { FormEvent, useEffect, useState } from "react";
import { Camera, KeyRound, Save } from "lucide-react";
import { Navigate, useNavigate } from "react-router-dom";
import { post, upload } from "../api";
import { clearSession, getSession, saveSession } from "../auth";
import { Avatar, Loading } from "../components";
import type { Profile } from "../types";

export default function EditProfilePage() {
  const session = getSession();
  const navigate = useNavigate();
  const [profile, setProfile] = useState<Profile | null>(null);
  const [username, setUsername] = useState(session?.username || "");
  const [bio, setBio] = useState("");
  const [avatar, setAvatar] = useState<File | null>(null);
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (!session) return;
    post<Profile>("/account/getProfile", { account_id: session.account_id }).then((data) => { setProfile(data); setBio(data.account.bio || ""); setUsername(data.account.username); });
  }, [session?.account_id]);

  if (!session) return <Navigate to="/auth" replace />;
  if (!profile) return <Loading />;

  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(""); setMessage("");
    try {
      let avatarURL = profile!.account.avatar_url || "";
      if (avatar) { const form = new FormData(); form.append("file", avatar); avatarURL = (await upload<{ avatar_url: string }>("/account/uploadAvatar", form)).avatar_url; }
      await post("/account/updateProfile", { avatar_url: avatarURL, bio }, true);
      if (username.trim() !== session!.username) {
        const renamed = await post<{ token: string }>("/account/rename", { new_username: username.trim() }, true);
        saveSession({ ...session!, username: username.trim(), token: renamed.token });
      }
      setMessage("资料已保存"); setTimeout(() => navigate(`/profile/${session!.account_id}`), 500);
    } catch (e) { setError(e instanceof Error ? e.message : "保存失败"); }
    finally { setBusy(false); }
  }

  async function changePassword(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      await post("/account/changePassword", { username: session!.username, old_password: oldPassword, new_password: newPassword });
      clearSession(); navigate("/auth");
    } catch (e) { setError(e instanceof Error ? e.message : "密码修改失败"); }
    finally { setBusy(false); }
  }

  return <section className="profile-page settings-page">
    <div className="page-heading"><span>SETTINGS</span><h1>个人资料</h1><p>更新头像、用户名和个人简介。</p></div>
    <form className="settings-panel" onSubmit={save}><div className="avatar-editor"><Avatar name={username} url={profile.account.avatar_url} size={82} /><label><Camera />选择新头像<input type="file" accept="image/jpeg,image/png,image/webp" onChange={(e) => setAvatar(e.target.files?.[0] || null)} /></label></div><label><span>用户名</span><input value={username} onChange={(e) => setUsername(e.target.value)} minLength={3} maxLength={32} pattern="[A-Za-z0-9]+" required /></label><label><span>个人简介</span><textarea value={bio} onChange={(e) => setBio(e.target.value)} maxLength={255} rows={4} /></label><button className="primary-button" disabled={busy}><Save />保存资料</button></form>
    <form className="settings-panel" onSubmit={changePassword}><h2><KeyRound />修改密码</h2><label><span>当前密码</span><input type="password" value={oldPassword} onChange={(e) => setOldPassword(e.target.value)} required /></label><label><span>新密码</span><input type="password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} minLength={8} maxLength={72} required /></label><button className="secondary-button" disabled={busy}>修改并退出所有设备</button></form>
    {message && <div className="success-banner">{message}</div>}{error && <div className="error-banner">{error}</div>}
  </section>;
}
