import { useEffect, useState } from "react";
import { Heart, MessageCircle, Settings, UserCheck, UserPlus } from "lucide-react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { getSession } from "../auth";
import { post } from "../api";
import { Avatar, ContentCard, EmptyState, Loading } from "../components";
import type { FeedItem, Profile } from "../types";

export default function ProfilePage() {
  const id = Number(useParams().id);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [items, setItems] = useState<FeedItem[]>([]);
  const [followed, setFollowed] = useState(false);
  const [error, setError] = useState("");
  const session = getSession(); const navigate = useNavigate();
  useEffect(() => {
    const base = Promise.all([post<Profile>("/account/getProfile", { account_id: id }), post<FeedItem[]>("/content/listByAuthorID", { author_id: id })]);
    const social = session && session.account_id !== id ? post<{ vloggers: { id: number }[] }>("/social/getAllVloggers", { follower_id: session.account_id }, true) : Promise.resolve({ vloggers: [] });
    const liked = session ? post<FeedItem[]>("/like/listMyLikedContents", {}, true) : Promise.resolve([]);
    Promise.all([base, social, liked]).then(([[p, list], relation, likedItems]) => {
      const likedIDs = new Set(likedItems.map((item) => item.id));
      setProfile(p); setFollowed(relation.vloggers.some((account) => account.id === id));
      setItems(list.map((item) => ({ ...item, author: { id: item.author_id || id, username: item.username || p.account.username }, is_liked: likedIDs.has(item.id) })));
    }).catch((e) => setError(e instanceof Error ? e.message : "加载失败"));
  }, [id, session?.account_id]);

  async function follow() {
    if (!session) { navigate("/auth"); return; }
    try { await post(followed ? "/social/unfollow" : "/social/follow", { vlogger_id: id }, true); setFollowed(!followed); setProfile((old) => old ? { ...old, follower_count: Math.max(0, old.follower_count + (followed ? -1 : 1)) } : old); }
    catch (e) { setError(e instanceof Error ? e.message : "操作失败"); }
  }
  if (!profile) return error ? <div className="error-banner">{error}</div> : <Loading />;
  return <section className="profile-page">{error && <div className="error-banner">{error}</div>}<div className="profile-header"><Avatar name={profile.account.username} url={profile.account.avatar_url} size={84} /><div className="profile-identity"><span>CREATOR</span><h1>{profile.account.username}</h1><p>{profile.account.bio || "这个人还没有写简介。"}</p></div><div className="profile-actions">{session?.account_id === id ? <Link className="secondary-button" to="/settings"><Settings size={18} />编辑资料</Link> : <><button className={followed ? "secondary-button" : "primary-button"} onClick={follow}>{followed ? <UserCheck size={18} /> : <UserPlus size={18} />}{followed ? "已关注" : "关注"}</button><Link className="secondary-button" to={`/messages/${id}`}><MessageCircle size={18} />私信</Link></>}</div></div>
    <div className="profile-stats"><div><strong>{profile.content_count}</strong><span>内容</span></div><Link to={`/profile/${id}/followers`}><strong>{profile.follower_count}</strong><span>关注者</span></Link><Link to={`/profile/${id}/following`}><strong>{profile.vlogger_count}</strong><span>正在关注</span></Link><div><strong>{profile.total_likes}</strong><span><Heart size={15} />获赞</span></div></div>
    <h2 className="section-title">发布的内容</h2>{items.length ? <div className="feed-grid">{items.map((item) => <ContentCard key={item.id} item={item} />)}</div> : <EmptyState title="暂时没有作品" text="第一条内容正在路上。" />}
  </section>;
}
