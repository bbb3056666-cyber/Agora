import { useEffect, useState } from "react";
import { Bell, Compass, Flame, Home, LogIn, LogOut, Menu, Plus, Search, UserRound, Users, X } from "lucide-react";
import { Link, NavLink, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { clearSession, getSession } from "./auth";
import { post } from "./api";
import type { UserSession } from "./types";
import FeedPage from "./pages/FeedPage";
import AuthPage from "./pages/AuthPage";
import DetailPage from "./pages/DetailPage";
import PublishPage from "./pages/PublishPage";
import ProfilePage from "./pages/ProfilePage";
import NotificationsPage from "./pages/NotificationsPage";

const nav = [
  { to: "/", label: "最新", icon: Home },
  { to: "/popular", label: "热门", icon: Flame },
  { to: "/following", label: "关注", icon: Users }
];

export default function App() {
  const [session, setSession] = useState<UserSession | null>(getSession());
  const [mobileOpen, setMobileOpen] = useState(false);
  const [unread, setUnread] = useState(0);
  const location = useLocation();
  const navigate = useNavigate();

  useEffect(() => {
    const sync = () => setSession(getSession());
    window.addEventListener("agora-auth", sync);
    window.addEventListener("storage", sync);
    return () => { window.removeEventListener("agora-auth", sync); window.removeEventListener("storage", sync); };
  }, []);

  useEffect(() => setMobileOpen(false), [location.pathname]);

  useEffect(() => {
    if (!session) { setUnread(0); return; }
    post<{ count: number }>("/notification/unreadCount", {}, true).then((r) => setUnread(r.count)).catch(() => undefined);
    const source = new EventSource(`/notification/stream?token=${encodeURIComponent(session.token)}`);
    source.onmessage = () => setUnread((count) => count + 1);
    return () => source.close();
  }, [session?.token]);

  async function logout() {
    try { await post("/account/logout", { refresh_token: session?.refresh_token }, true); } catch { /* clear local session regardless */ }
    clearSession();
    navigate("/");
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <button className="icon-button mobile-menu" onClick={() => setMobileOpen(true)} aria-label="打开导航"><Menu /></button>
        <Link to="/" className="brand"><span className="brand-mark">A</span><strong>Agora</strong></Link>
        <div className="search-shell"><Search size={18} /><input aria-label="搜索" placeholder="搜索内容与创作者" disabled /><kbd>即将开放</kbd></div>
        <div className="top-actions">
          {session ? <>
            <Link className="icon-button notification-button" to="/notifications" aria-label="通知"><Bell />{unread > 0 && <i>{Math.min(unread, 99)}</i>}</Link>
            <Link className="primary-button" to="/publish"><Plus size={18} />发布</Link>
          </> : <Link className="primary-button" to="/auth"><LogIn size={18} />登录</Link>}
        </div>
      </header>

      <aside className={`sidebar ${mobileOpen ? "open" : ""}`}>
        <button className="icon-button sidebar-close" onClick={() => setMobileOpen(false)} aria-label="关闭导航"><X /></button>
        <nav>
          <span className="nav-label">浏览</span>
          {nav.map(({ to, label, icon: Icon }) => <NavLink key={to} to={to} end={to === "/"}><Icon />{label}</NavLink>)}
          <span className="nav-label">我的</span>
          {session ? <>
            <NavLink to={`/profile/${session.account_id}`}><UserRound />个人主页</NavLink>
            <NavLink to="/notifications"><Bell />通知{unread > 0 && <b className="nav-badge">{unread}</b>}</NavLink>
            <button className="nav-action" onClick={logout}><LogOut />退出登录</button>
          </> : <NavLink to="/auth"><LogIn />登录账户</NavLink>}
        </nav>
        <div className="sidebar-note"><Compass /><div><strong>发现好内容</strong><span>视频、图片与文章都在这里。</span></div></div>
      </aside>
      {mobileOpen && <button className="scrim" onClick={() => setMobileOpen(false)} aria-label="关闭导航" />}

      <main className="main-content">
        <Routes>
          <Route path="/" element={<FeedPage mode="latest" />} />
          <Route path="/popular" element={<FeedPage mode="popular" />} />
          <Route path="/following" element={<FeedPage mode="following" />} />
          <Route path="/auth" element={<AuthPage />} />
          <Route path="/content/:id" element={<DetailPage />} />
          <Route path="/publish" element={<PublishPage />} />
          <Route path="/profile/:id" element={<ProfilePage />} />
          <Route path="/notifications" element={<NotificationsPage onRead={() => setUnread(0)} />} />
          <Route path="*" element={<FeedPage mode="latest" />} />
        </Routes>
      </main>
    </div>
  );
}
