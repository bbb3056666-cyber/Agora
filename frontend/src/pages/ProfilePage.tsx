import { useEffect, useState } from "react";
import { Heart, UserPlus } from "lucide-react";
import { useNavigate, useParams } from "react-router-dom";
import { getSession } from "../auth";
import { post } from "../api";
import { Avatar, ContentCard, EmptyState, Loading } from "../components";
import type { FeedItem, Profile } from "../types";

export default function ProfilePage() {
  const id = Number(useParams().id);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [items, setItems] = useState<FeedItem[]>([]);
  const [followed, setFollowed] = useState(false);
  const session = getSession(); const navigate = useNavigate();
  useEffect(() => { Promise.all([post<Profile>("/account/getProfile", { account_id: id }), post<FeedItem[]>("/content/listByAuthorID", { author_id: id })]).then(([p, list]) => { setProfile(p); setItems(list.map((item) => ({ ...item, author: { id: item.author_id || id, username: item.username || p.account.username }, is_liked: false }))); }); }, [id]);

  async function follow() {
    if (!session) { navigate("/auth"); return; }
    await post(followed ? "/social/unfollow" : "/social/follow", { vlogger_id: id }, true); setFollowed(!followed);
  }
  if (!profile) return <Loading />;
  return <section className="profile-page"><div className="profile-header"><Avatar name={profile.account.username} url={profile.account.avatar_url} size={84} /><div className="profile-identity"><span>CREATOR</span><h1>{profile.account.username}</h1><p>{profile.account.bio || "这个人还没有写简介。"}</p></div>{session?.account_id !== id && <button className="primary-button" onClick={follow}><UserPlus size={18} />{followed ? "已关注" : "关注"}</button>}</div>
    <div className="profile-stats"><div><strong>{profile.content_count}</strong><span>内容</span></div><div><strong>{profile.follower_count}</strong><span>关注者</span></div><div><strong>{profile.vlogger_count}</strong><span>正在关注</span></div><div><strong>{profile.total_likes}</strong><span><Heart size={15} />获赞</span></div></div>
    <h2 className="section-title">发布的内容</h2>{items.length ? <div className="feed-grid">{items.map((item) => <ContentCard key={item.id} item={item} />)}</div> : <EmptyState title="暂时没有作品" text="第一条内容正在路上。" />}
  </section>;
}
