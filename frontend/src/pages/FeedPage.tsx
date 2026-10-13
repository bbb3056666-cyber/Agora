import { useEffect, useState } from "react";
import { ArrowDown, RefreshCw } from "lucide-react";
import { Link } from "react-router-dom";
import { getSession } from "../auth";
import { post } from "../api";
import { ContentCard, EmptyState, Loading } from "../components";
import type { FeedItem } from "../types";

type Mode = "latest" | "popular" | "following";

const copy: Record<Mode, { title: string; text: string }> = {
  latest: { title: "此刻新鲜", text: "按发布时间看看社区里正在发生什么。" },
  popular: { title: "正在流行", text: "由互动热度与时间衰减共同选出的内容。" },
  following: { title: "我的关注", text: "只看你关心的创作者。" }
};

export default function FeedPage({ mode }: { mode: Mode }) {
  const [items, setItems] = useState<FeedItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState<Record<string, unknown>>({});

  async function load(reset = false) {
    setLoading(true); setError("");
    try {
      const current = reset ? {} : cursor;
      let result: { content_list: FeedItem[]; has_more: boolean; [key: string]: unknown };
      if (mode === "popular") {
        result = await post("/feed/listByPopularity", { limit: 12, as_of: current.as_of || 0, offset: current.next_offset || 0 });
        setCursor({ as_of: result.as_of, next_offset: result.next_offset });
      } else {
        const path = mode === "following" ? "/feed/listByFollowing" : "/feed/listLatest";
        result = await post(path, { limit: 12, latest_time: current.next_time || 0 }, mode === "following");
        setCursor({ next_time: result.next_time });
      }
      setItems((old) => reset ? result.content_list : [...old, ...result.content_list]);
      setHasMore(result.has_more);
    } catch (e) { setError(e instanceof Error ? e.message : "加载失败"); }
    finally { setLoading(false); }
  }

  useEffect(() => { setItems([]); setCursor({}); void load(true); }, [mode]);
  useEffect(() => {
    if (items.length) sessionStorage.setItem("agora-viewer-feed", JSON.stringify(items));
  }, [items]);

  if (mode === "following" && !getSession()) {
    return <section className="page-section"><div className="page-heading"><span>FOLLOWING</span><h1>{copy[mode].title}</h1><p>{copy[mode].text}</p></div><EmptyState title="登录后查看关注流" text="关注喜欢的创作者，他们的新内容会出现在这里。" /><Link className="primary-button centered" to="/auth">去登录</Link></section>;
  }

  return <section className="page-section">
    <div className="page-heading feed-heading"><div><span>{mode.toUpperCase()}</span><h1>{copy[mode].title}</h1><p>{copy[mode].text}</p></div><button className="secondary-button" onClick={() => load(true)} disabled={loading}><RefreshCw size={17} />刷新</button></div>
    {error && <div className="error-banner">{error}</div>}
    {!loading && items.length === 0 && <EmptyState title="这里还没有内容" text="发布第一条内容，让时间线开始流动。" />}
    <div className="feed-grid">{items.map((item) => <ContentCard item={item} key={`${mode}-${item.id}`} />)}</div>
    {loading && <Loading />}
    {!loading && hasMore && <button className="load-more" onClick={() => load()}><ArrowDown size={18} />加载更多</button>}
  </section>;
}
