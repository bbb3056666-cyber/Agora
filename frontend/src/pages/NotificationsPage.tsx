import { useEffect, useState } from "react";
import { Bell, CheckCheck } from "lucide-react";
import { Navigate } from "react-router-dom";
import { getSession } from "../auth";
import { post } from "../api";
import { EmptyState, Loading } from "../components";
import type { Notification } from "../types";

export default function NotificationsPage({ onRead }: { onRead: () => void }) {
  const session = getSession(); const [items, setItems] = useState<Notification[] | null>(null);
  useEffect(() => { if (session) post<{ notifications: Notification[] }>("/notification/list", {}, true).then((r) => setItems(r.notifications)); }, []);
  if (!session) return <Navigate to="/auth" replace />;
  async function readAll() { await post("/notification/markRead", {}, true); setItems((old) => old?.map((n) => ({ ...n, is_read: true })) || []); onRead(); }
  return <section className="notifications-page"><div className="page-heading feed-heading"><div><span>INBOX</span><h1>通知</h1><p>点赞、评论与关注动态会汇集在这里。</p></div><button className="secondary-button" onClick={readAll}><CheckCheck size={18} />全部已读</button></div>
    {!items ? <Loading /> : items.length === 0 ? <EmptyState title="还没有通知" text="发生新的互动时，我们会在这里告诉你。" /> : <div className="notification-list">{items.map((item) => <div className={`notification-item ${item.is_read ? "" : "unread"}`} key={item.id}><span className="notification-icon"><Bell /></span><div><strong>{item.sender_id ? `用户 ${item.sender_id}` : "Agora"}</strong><p>{item.content || ({ like: "喜欢了你的内容", comment: "评论了你的内容", follow: "关注了你" }[String(item.type)] || "与你产生了新的互动")}</p><time>{new Date(item.created_at).toLocaleString("zh-CN")}</time></div></div>)}</div>}
  </section>;
}
