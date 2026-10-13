import { FormEvent, useEffect, useState } from "react";
import { ArrowLeft, Heart, MessageCircle, Send, Trash2 } from "lucide-react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { getSession } from "../auth";
import { post } from "../api";
import { Avatar, Loading } from "../components";
import ActivityPanel from "../ActivityPanel";
import type { Comment, ContentActivity, FeedItem } from "../types";

export default function DetailPage() {
  const id = Number(useParams().id);
  const [item, setItem] = useState<FeedItem | null>(null);
  const [comments, setComments] = useState<Comment[]>([]);
  const [activities, setActivities] = useState<ContentActivity[]>([]);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const session = getSession();
  const navigate = useNavigate();

  async function load() {
    try {
      const detail = await post<FeedItem>("/content/getDetail", { id });
      const author = detail.author || { id: detail.author_id || 0, username: detail.username || "未知用户" };
      let liked = false;
      if (session) liked = (await post<{ is_liked: boolean }>("/like/isLiked", { content_id: id }, true)).is_liked;
      setItem({ ...detail, author, is_liked: liked });
      setComments(await post<Comment[]>("/comment/listAll", { content_id: id }));
      setActivities(await post<ContentActivity[]>("/content/activity/list", { content_id: id }));
    } catch (e) { setError(e instanceof Error ? e.message : "加载失败"); }
  }
  useEffect(() => { void load(); }, [id]);

  async function toggleLike() {
    if (!session) { navigate("/auth"); return; }
    if (!item) return;
    const liked = item.is_liked;
    setItem({ ...item, is_liked: !liked, likes_count: item.likes_count + (liked ? -1 : 1) });
    try { await post(liked ? "/like/unlike" : "/like/like", { content_id: id }, true); }
    catch { setItem(item); }
  }

  async function publishComment(event: FormEvent) {
    event.preventDefault();
    if (!session) { navigate("/auth"); return; }
    if (!text.trim()) return;
    try { await post("/comment/publish", { content_id: id, content: text.trim() }, true); setText(""); setTimeout(() => void load(), 250); }
    catch (e) { setError(e instanceof Error ? e.message : "评论失败"); }
  }

  async function removeComment(commentID: number) {
    try { await post("/comment/delete", { comment_id: commentID }, true); setComments((old) => old.filter((comment) => comment.id !== commentID)); }
    catch (e) { setError(e instanceof Error ? e.message : "删除评论失败"); }
  }

  async function removeContent() {
    if (!window.confirm("确定删除这条内容吗？删除后无法恢复。")) return;
    try { await post("/content/delete", { id }, true); navigate(`/profile/${session?.account_id}`); }
    catch (e) { setError(e instanceof Error ? e.message : "删除内容失败"); }
  }

  if (!item && !error) return <Loading />;
  if (!item) return <div className="error-banner">{error}</div>;

  return <section className="detail-page">
    <button className="back-button" onClick={() => navigate(-1)}><ArrowLeft />返回</button>
    <article className="detail-article">
      <div className="detail-author"><Link to={`/profile/${item.author.id}`}><Avatar name={item.author.username} size={46} /></Link><div><Link to={`/profile/${item.author.id}`}>{item.author.username}</Link><span>{item.content_type === "video" ? "视频" : item.content_type === "image" ? "图片" : "文章"}</span></div></div>
      <h1>{item.title}</h1>{item.description && <p className="detail-description">{item.description}</p>}
      {item.content_type === "video" && item.play_url && <video className="detail-media" src={item.play_url} poster={item.cover_url} controls />}
      {item.content_type === "image" && <div className="detail-images">{item.assets?.map((asset) => <img key={asset.url} src={asset.url} alt={item.title} />)}</div>}
      {item.body && <div className="article-body">{item.body}</div>}
      <div className="detail-actions"><button className={item.is_liked ? "liked" : ""} onClick={toggleLike}><Heart fill={item.is_liked ? "currentColor" : "none"} />{item.likes_count} 喜欢</button><span><MessageCircle />{comments.length} 评论</span>{session?.account_id === item.author.id && <button className="danger-action" onClick={removeContent}><Trash2 />删除内容</button>}</div>
    </article>
    <section className="detail-activities"><div className="detail-section-heading"><span>ACTIVITIES</span><h2>相关活动</h2></div><ActivityPanel contentID={id} authorID={item.author.id} activities={activities} onActivitiesChange={setActivities} /></section>
    <section className="comments"><h2>讨论 <span>{comments.length}</span></h2><form onSubmit={publishComment}><input value={text} onChange={(e) => setText(e.target.value)} placeholder={session ? "写下你的想法" : "登录后参与讨论"} /><button aria-label="发送评论"><Send /></button></form>
      <div>{comments.map((comment) => <div className="comment" key={comment.id}><Avatar name={comment.username} size={34} /><div><strong>{comment.username}</strong><p>{comment.content}</p></div>{session?.account_id === comment.author_id && <button className="icon-button" onClick={() => removeComment(comment.id)} aria-label="删除评论"><Trash2 size={16} /></button>}</div>)}</div>
    </section>
  </section>;
}
