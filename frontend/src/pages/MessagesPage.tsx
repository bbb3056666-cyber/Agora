import { FormEvent, useEffect, useMemo, useState } from "react";
import { ArrowLeft, Send } from "lucide-react";
import { Link, Navigate, useNavigate, useParams } from "react-router-dom";
import { post } from "../api";
import { getSession } from "../auth";
import { Avatar, Loading } from "../components";
import type { Message, Profile } from "../types";

export default function MessagesPage() {
  const peerID = Number(useParams().id);
  const session = getSession();
  const navigate = useNavigate();
  const [peer, setPeer] = useState<Profile | null>(null);
  const [messages, setMessages] = useState<Message[] | null>(null);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const ordered = useMemo(() => [...(messages || [])].reverse(), [messages]);

  async function load() {
    try {
      const [profile, result] = await Promise.all([
        post<Profile>("/account/getProfile", { account_id: peerID }),
        post<{ messages: Message[] }>("/message/list", { peer_id: peerID }, true)
      ]);
      setPeer(profile); setMessages(result.messages);
    } catch (e) { setError(e instanceof Error ? e.message : "加载失败"); }
  }
  useEffect(() => { if (session) void load(); }, [peerID, session?.account_id]);
  if (!session) return <Navigate to="/auth" replace />;
  if (peerID === session.account_id) return <Navigate to={`/profile/${peerID}`} replace />;

  async function send(event: FormEvent) {
    event.preventDefault(); if (!text.trim()) return;
    try { const message = await post<Message>("/message/send", { to_id: peerID, content: text.trim() }, true); setMessages((old) => [message, ...(old || [])]); setText(""); }
    catch (e) { setError(e instanceof Error ? e.message : "发送失败"); }
  }

  if (!peer || !messages) return error ? <div className="error-banner">{error}</div> : <Loading />;
  return <section className="messages-page"><button className="back-button" onClick={() => navigate(-1)}><ArrowLeft />返回</button><div className="conversation"><header><Link to={`/profile/${peerID}`}><Avatar name={peer.account.username} url={peer.account.avatar_url} size={44} /></Link><div><strong>{peer.account.username}</strong><span>{peer.account.bio || "私信对话"}</span></div></header><div className="message-list">{ordered.length ? ordered.map((message) => <div className={`message-bubble ${message.from_id === session.account_id ? "mine" : ""}`} key={message.id}><p>{message.content}</p><time>{new Date(message.created_at).toLocaleString("zh-CN")}</time></div>) : <div className="conversation-empty">发送第一条消息，开始对话。</div>}</div><form onSubmit={send}><input value={text} onChange={(e) => setText(e.target.value)} maxLength={1000} placeholder={`给 ${peer.account.username} 发消息`} /><button aria-label="发送"><Send /></button></form></div>{error && <div className="error-banner">{error}</div>}</section>;
}
