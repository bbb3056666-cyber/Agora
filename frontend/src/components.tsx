import { Heart, Image as ImageIcon, MessageCircle, Play } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import { useEffect, useState } from "react";
import { getSession } from "./auth";
import { post } from "./api";
import type { AccountSummary, Author, FeedItem, Profile } from "./types";

const authorCache = new Map<number, AccountSummary>();
const authorRequests = new Map<number, Promise<AccountSummary>>();

function requestAuthor(author: Author) {
  const cached = authorCache.get(author.id);
  if (cached) return Promise.resolve(cached);
  const pending = authorRequests.get(author.id);
  if (pending) return pending;
  const request = post<Profile>("/account/getProfile", { account_id: author.id })
    .then((profile) => {
      authorCache.set(author.id, profile.account);
      authorRequests.delete(author.id);
      return profile.account;
    })
    .catch((error) => {
      authorRequests.delete(author.id);
      throw error;
    });
  authorRequests.set(author.id, request);
  return request;
}

export function Avatar({ name, url, size = 38 }: { name: string; url?: string; size?: number }) {
  return url ? (
    <img className="avatar" src={url} alt={name} style={{ width: size, height: size }} />
  ) : (
    <span className="avatar avatar-fallback" style={{ width: size, height: size }}>
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}

export function useAuthorProfile(author: Author) {
  const [account, setAccount] = useState<AccountSummary>(() => authorCache.get(author.id) || author);
  useEffect(() => {
    setAccount(authorCache.get(author.id) || author);
    if (!author.id) return;
    let active = true;
    void requestAuthor(author).then((next) => { if (active) setAccount(next); }).catch(() => undefined);
    return () => { active = false; };
  }, [author.id, author.username]);
  return account;
}

export function AuthorAvatar({ author, size = 38 }: { author: Author; size?: number }) {
  const account = useAuthorProfile(author);
  return <Avatar name={account.username} url={account.avatar_url} size={size} />;
}

function itemTime(value: number | string) {
  const date = typeof value === "number" ? new Date(value > 10_000_000_000 ? value : value * 1000) : new Date(value);
  return Number.isNaN(date.getTime()) ? "刚刚" : new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric" }).format(date);
}

export function ContentCard({ item }: { item: FeedItem }) {
  const media = item.cover_url || item.assets?.[0]?.url;
  const [liked, setLiked] = useState(item.is_liked);
  const [likes, setLikes] = useState(item.likes_count);
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();
  useEffect(() => { setLiked(item.is_liked); setLikes(item.likes_count); }, [item.id, item.is_liked, item.likes_count]);

  async function toggleLike() {
    if (!getSession()) { navigate("/auth"); return; }
    if (busy) return;
    const previous = { liked, likes };
    setBusy(true); setLiked(!liked); setLikes(Math.max(0, likes + (liked ? -1 : 1)));
    try { await post(liked ? "/like/unlike" : "/like/like", { content_id: item.id }, true); }
    catch { setLiked(previous.liked); setLikes(previous.likes); }
    finally { setBusy(false); }
  }
  return (
    <article className="content-card">
      <Link className="card-media" to={`/view/${item.id}`}>
        {media ? (
          <img src={media} alt={item.title} loading="lazy" />
        ) : (
          <div className="article-preview">
            <span>{item.content_type === "article" ? "ARTICLE" : item.content_type.toUpperCase()}</span>
            <strong>{item.title}</strong>
          </div>
        )}
        {item.content_type === "video" && <Play className="media-play" fill="currentColor" />}
        {item.content_type === "image" && <ImageIcon className="media-type" />}
      </Link>
      <div className="card-body">
        <div className="card-author">
          <Link to={`/profile/${item.author.id}`}><AuthorAvatar author={item.author} size={36} /></Link>
          <div>
            <Link to={`/profile/${item.author.id}`}>{item.author.username}</Link>
            <span>{itemTime(item.create_time)}</span>
          </div>
        </div>
        <Link to={`/view/${item.id}`} className="card-title">{item.title || "未命名内容"}</Link>
        {item.description && <p>{item.description}</p>}
        <div className="card-meta">
          <button className={liked ? "liked" : ""} onClick={toggleLike} disabled={busy} aria-label={liked ? "取消喜欢" : "喜欢"}><Heart size={17} fill={liked ? "currentColor" : "none"} />{likes}</button>
          <Link to={`/content/${item.id}`}><MessageCircle size={17} />查看讨论</Link>
        </div>
      </div>
    </article>
  );
}

export function EmptyState({ title, text }: { title: string; text: string }) {
  return <div className="empty-state"><strong>{title}</strong><p>{text}</p></div>;
}

export function Loading() {
  return <div className="loading"><span /><span /><span /></div>;
}
