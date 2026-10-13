import { Heart, Image as ImageIcon, MessageCircle, Play } from "lucide-react";
import { Link } from "react-router-dom";
import type { FeedItem } from "./types";

export function Avatar({ name, url, size = 38 }: { name: string; url?: string; size?: number }) {
  return url ? (
    <img className="avatar" src={url} alt={name} style={{ width: size, height: size }} />
  ) : (
    <span className="avatar avatar-fallback" style={{ width: size, height: size }}>
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}

function itemTime(value: number | string) {
  const date = typeof value === "number" ? new Date(value > 10_000_000_000 ? value : value * 1000) : new Date(value);
  return Number.isNaN(date.getTime()) ? "刚刚" : new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric" }).format(date);
}

export function ContentCard({ item }: { item: FeedItem }) {
  const media = item.cover_url || item.assets?.[0]?.url;
  return (
    <article className="content-card">
      <Link className="card-media" to={`/content/${item.id}`}>
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
          <Link to={`/profile/${item.author.id}`}><Avatar name={item.author.username} size={32} /></Link>
          <div>
            <Link to={`/profile/${item.author.id}`}>{item.author.username}</Link>
            <span>{itemTime(item.create_time)}</span>
          </div>
        </div>
        <Link to={`/content/${item.id}`} className="card-title">{item.title || "未命名内容"}</Link>
        {item.description && <p>{item.description}</p>}
        <div className="card-meta">
          <span><Heart size={17} fill={item.is_liked ? "currentColor" : "none"} />{item.likes_count}</span>
          <span><MessageCircle size={17} />查看讨论</span>
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
