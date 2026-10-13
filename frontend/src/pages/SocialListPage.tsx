import { useEffect, useState } from "react";
import { Users } from "lucide-react";
import { Link, Navigate, useParams } from "react-router-dom";
import { post } from "../api";
import { getSession } from "../auth";
import { Avatar, EmptyState, Loading } from "../components";
import type { AccountSummary } from "../types";

export default function SocialListPage({ kind }: { kind: "followers" | "following" }) {
  const id = Number(useParams().id);
  const session = getSession();
  const [items, setItems] = useState<AccountSummary[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!session) return;
    const path = kind === "followers" ? "/social/getAllFollowers" : "/social/getAllVloggers";
    const body = kind === "followers" ? { vlogger_id: id } : { follower_id: id };
    post<{ followers?: AccountSummary[]; vloggers?: AccountSummary[] }>(path, body, true)
      .then((data) => setItems(data.followers || data.vloggers || []))
      .catch((e) => setError(e instanceof Error ? e.message : "加载失败"));
  }, [id, kind, session?.account_id]);
  if (!session) return <Navigate to="/auth" replace />;
  return <section className="page-section"><div className="page-heading"><span>SOCIAL</span><h1>{kind === "followers" ? "关注者" : "正在关注"}</h1><p>查看这个账号的社交关系。</p></div>{error && <div className="error-banner">{error}</div>}{!items && !error ? <Loading /> : items?.length ? <div className="people-grid social-list">{items.map((account) => <Link className="person-card" to={`/profile/${account.id}`} key={account.id}><Avatar name={account.username} url={account.avatar_url} size={52} /><div><strong>{account.username}</strong><p>{account.bio || "暂时没有个人简介"}</p></div><Users /></Link>)}</div> : <EmptyState title="这里还没有人" text="新的关系会显示在这里。" />}</section>;
}
