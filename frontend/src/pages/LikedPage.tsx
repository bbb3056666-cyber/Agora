import { useEffect, useState } from "react";
import { Navigate } from "react-router-dom";
import { post } from "../api";
import { getSession } from "../auth";
import { ContentCard, EmptyState, Loading } from "../components";
import type { FeedItem } from "../types";

export default function LikedPage() {
  const session = getSession();
  const [items, setItems] = useState<FeedItem[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!session) return;
    post<FeedItem[]>("/like/listMyLikedContents", {}, true)
      .then((contents) => setItems(contents.map((item) => ({
        ...item,
        author: item.author || {
          id: item.author_id || 0,
          username: item.username || "未知用户"
        },
        is_liked: true
      }))))
      .catch((e) => setError(e instanceof Error ? e.message : "加载失败"));
  }, [session?.account_id]);

  if (!session) return <Navigate to="/auth" replace />;

  return <section className="page-section">
    <div className="page-heading"><span>LIBRARY</span><h1>喜欢的内容</h1><p>你点过喜欢的作品会集中显示在这里。</p></div>
    {error && <div className="error-banner">{error}</div>}
    {!items && !error ? <Loading /> : items?.length ? <div className="feed-grid">{items.map((item) => <ContentCard key={item.id} item={item} />)}</div> : <EmptyState title="还没有喜欢的内容" text="在内容卡片或详情页点一下喜欢，它就会出现在这里。" />}
  </section>;
}
