import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, Heart, MessageCircle, Plus, Send, X } from "lucide-react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { post } from "../api";
import { getSession } from "../auth";
import { AuthorAvatar, Avatar, Loading, useAuthorProfile } from "../components";
import type { Comment, FeedItem } from "../types";

function normalize(item: FeedItem): FeedItem {
  return {
    ...item,
    author: item.author || { id: item.author_id || 0, username: item.username || "未知用户" },
    is_liked: item.is_liked || false
  };
}

function storedFeed() {
  try {
    const value = sessionStorage.getItem("agora-viewer-feed");
    return value ? (JSON.parse(value) as FeedItem[]).map(normalize) : [];
  } catch {
    return [];
  }
}

export default function ImmersivePage() {
  const id = Number(useParams().id);
  const navigate = useNavigate();
  const session = getSession();
  const [sequence, setSequence] = useState<FeedItem[]>(storedFeed);
  const [item, setItem] = useState<FeedItem | null>(null);
  const [comments, setComments] = useState<Comment[]>([]);
  const [commentsOpen, setCommentsOpen] = useState(false);
  const [text, setText] = useState("");
  const [followed, setFollowed] = useState(false);
  const [imageIndex, setImageIndex] = useState(0);
  const [contentDirection, setContentDirection] = useState<"next" | "previous">("next");
  const [imageDirection, setImageDirection] = useState<"next" | "previous">("next");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const wheelLocked = useRef(false);

  const index = useMemo(() => sequence.findIndex((entry) => entry.id === id), [sequence, id]);
  const author = item?.author || { id: 0, username: "未知用户" };
  const authorProfile = useAuthorProfile(author);
  const images = useMemo(() => {
    if (!item) return [];
    const urls = item.assets?.map((asset) => asset.url).filter(Boolean) || [];
    if (!urls.length && item.cover_url) urls.push(item.cover_url);
    return urls;
  }, [item]);

  const move = useCallback((direction: number) => {
    if (index < 0) return;
    const next = sequence[index + direction];
    if (next) {
      setContentDirection(direction > 0 ? "next" : "previous");
      navigate(`/view/${next.id}`, { replace: true });
    }
  }, [index, navigate, sequence]);

  function moveImage(direction: number) {
    setImageDirection(direction > 0 ? "next" : "previous");
    setImageIndex((current) => (current + direction + images.length) % images.length);
  }

  useEffect(() => {
    if (sequence.length) return;
    void post<{ content_list: FeedItem[] }>("/feed/listLatest", { limit: 30, latest_time: 0 })
      .then((result) => setSequence((current) => {
        const incoming = result.content_list.map(normalize);
        if (!current.length) return incoming;
        const known = new Set(incoming.map((entry) => entry.id));
        return [...current.filter((entry) => !known.has(entry.id)), ...incoming];
      }))
      .catch((e) => setError(e instanceof Error ? e.message : "内容流加载失败"));
  }, [sequence.length]);

  useEffect(() => {
    setImageIndex(0); setCommentsOpen(false); setError("");
    const detail = post<FeedItem>("/content/getDetail", { id });
    const commentList = post<Comment[]>("/comment/listAll", { content_id: id });
    const liked = session ? post<{ is_liked: boolean }>("/like/isLiked", { content_id: id }, true) : Promise.resolve({ is_liked: false });
    Promise.all([detail, commentList, liked]).then(([content, nextComments, likeState]) => {
      const next = { ...normalize(content), is_liked: likeState.is_liked };
      setItem(next); setComments(nextComments || []);
      setSequence((old) => old.some((entry) => entry.id === id) ? old : [next, ...old]);
    }).catch((e) => setError(e instanceof Error ? e.message : "内容加载失败"));
  }, [id, session?.account_id]);

  useEffect(() => {
    if (!session || !item || session.account_id === item.author.id) { setFollowed(false); return; }
    void post<{ vloggers: { id: number }[] }>("/social/getAllVloggers", { follower_id: session.account_id }, true)
      .then((result) => setFollowed(result.vloggers.some((entry) => entry.id === item.author.id)))
      .catch(() => undefined);
  }, [item?.author.id, session?.account_id]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (commentsOpen) return;
      if (event.key === "ArrowDown") move(1);
      if (event.key === "ArrowUp") move(-1);
      if (event.key === "Escape") navigate(-1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [commentsOpen, move, navigate]);

  function onWheel(event: React.WheelEvent) {
    if (commentsOpen || wheelLocked.current || Math.abs(event.deltaY) < 18) return;
    wheelLocked.current = true;
    move(event.deltaY > 0 ? 1 : -1);
    window.setTimeout(() => { wheelLocked.current = false; }, 650);
  }

  async function toggleLike() {
    if (!session) { navigate("/auth"); return; }
    if (!item || busy) return;
    const before = item;
    setBusy(true);
    setItem({ ...item, is_liked: !item.is_liked, likes_count: Math.max(0, item.likes_count + (item.is_liked ? -1 : 1)) });
    try { await post(item.is_liked ? "/like/unlike" : "/like/like", { content_id: id }, true); }
    catch (e) { setItem(before); setError(e instanceof Error ? e.message : "点赞失败"); }
    finally { setBusy(false); }
  }

  async function toggleFollow() {
    if (!session) { navigate("/auth"); return; }
    if (!item || busy) return;
    setBusy(true);
    try {
      await post(followed ? "/social/unfollow" : "/social/follow", { vlogger_id: item.author.id }, true);
      setFollowed(!followed);
    } catch (e) { setError(e instanceof Error ? e.message : "关注失败"); }
    finally { setBusy(false); }
  }

  async function publishComment(event: FormEvent) {
    event.preventDefault();
    if (!session) { navigate("/auth"); return; }
    if (!text.trim()) return;
    try {
      await post("/comment/publish", { content_id: id, content: text.trim() }, true);
      setText("");
      setComments(await post<Comment[]>("/comment/listAll", { content_id: id }));
    } catch (e) { setError(e instanceof Error ? e.message : "评论失败"); }
  }

  if (!item && !error) return <div className="immersive-loading"><Loading /></div>;
  if (!item) return <div className="immersive-loading"><div className="error-banner">{error}</div><button onClick={() => navigate(-1)}>返回</button></div>;

  return <main className="immersive-viewer" onWheel={onWheel}>
    <button className="viewer-close" onClick={() => navigate(-1)} aria-label="退出全屏浏览"><X /></button>
    <div className="viewer-progress"><span>{Math.max(index + 1, 1)}</span><i>/</i><span>{Math.max(sequence.length, 1)}</span></div>
    <section key={item.id} className={`viewer-stage viewer-${item.content_type} content-slide-${contentDirection}`}>
      {item.content_type === "video" && item.play_url && <video key={item.id} src={item.play_url} poster={item.cover_url} autoPlay muted playsInline controls />}
      {item.content_type === "image" && images.length > 0 && <div className="viewer-image-wrap"><img key={images[imageIndex]} className={`image-slide-${imageDirection}`} src={images[imageIndex]} alt={item.title} />{images.length > 1 && <><button className="image-prev" onClick={() => moveImage(-1)} aria-label="上一张图片"><ArrowLeft /></button><button className="image-next" onClick={() => moveImage(1)} aria-label="下一张图片"><ArrowRight /></button><span className="image-count">{imageIndex + 1} / {images.length}</span></>}</div>}
      {item.content_type === "article" && <article className="viewer-article"><span>ARTICLE</span><h1>{item.title}</h1>{item.description && <p className="viewer-description">{item.description}</p>}<div className="viewer-article-body">{item.body}</div><Link to={`/content/${item.id}`} className="viewer-detail-link">查看完整内容</Link></article>}
      {item.content_type !== "article" && <div className="viewer-caption"><h1>{item.title}</h1>{item.description && <p>{item.description}</p>}</div>}
    </section>

    <aside className="viewer-actions">
      <div className="viewer-author-action"><Link to={`/profile/${item.author.id}`} title={authorProfile.username}><AuthorAvatar author={item.author} size={52} /></Link>{session?.account_id !== item.author.id && <button className={followed ? "following" : ""} onClick={toggleFollow} disabled={busy} aria-label={followed ? "取消关注" : "关注作者"}>{followed ? "✓" : <Plus />}</button>}</div>
      <button className={item.is_liked ? "active" : ""} onClick={toggleLike} disabled={busy}><Heart fill={item.is_liked ? "currentColor" : "none"} /><strong>{item.likes_count}</strong><span>喜欢</span></button>
      <button onClick={() => setCommentsOpen(true)}><MessageCircle /><strong>{comments.length}</strong><span>评论</span></button>
    </aside>

    <nav className="viewer-nav" aria-label="切换内容"><button onClick={() => move(-1)} disabled={index <= 0} aria-label="上一条内容"><ArrowUp /></button><button onClick={() => move(1)} disabled={index < 0 || index >= sequence.length - 1} aria-label="下一条内容"><ArrowDown /></button></nav>
    <div className="viewer-wheel-hint">滚轮或方向键切换内容</div>
    {error && <div className="viewer-error">{error}</div>}

    {commentsOpen && <><button className="viewer-drawer-scrim" onClick={() => setCommentsOpen(false)} aria-label="关闭评论" /><aside className="viewer-comments"><header><div><strong>评论</strong><span>{comments.length} 条讨论</span></div><button onClick={() => setCommentsOpen(false)} aria-label="关闭评论"><X /></button></header><div className="viewer-comment-list">{comments.length ? comments.map((comment) => <div className="viewer-comment" key={comment.id}><Avatar name={comment.username} size={34} /><div><strong>{comment.username}</strong><p>{comment.content}</p></div></div>) : <div className="conversation-empty">还没有评论，来说点什么吧。</div>}</div><form onSubmit={publishComment}><input value={text} onChange={(event) => setText(event.target.value)} maxLength={500} placeholder={session ? "写下你的评论" : "登录后参与评论"} /><button aria-label="发送评论"><Send /></button></form></aside></>}
  </main>;
}
