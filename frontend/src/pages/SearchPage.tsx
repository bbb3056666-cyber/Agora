import { FormEvent, useEffect, useState } from "react";
import { Search, UserRound } from "lucide-react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { post } from "../api";
import { Avatar, ContentCard, EmptyState, Loading } from "../components";
import type { FeedItem, SearchResponse } from "../types";

export default function SearchPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const query = (params.get("q") || "").trim();
  const [value, setValue] = useState(query);
  const [result, setResult] = useState<SearchResponse | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    setValue(query); setError("");
    if (!query) { setResult({ accounts: [], contents: [] }); return; }
    setResult(null);
    post<SearchResponse>("/search", { query, limit: 18 })
      .then((data) => setResult({ ...data, contents: data.contents.map((item: FeedItem) => ({
        ...item,
        author: item.author || { id: item.author_id || 0, username: item.username || "未知用户" },
        is_liked: item.is_liked || false
      })) }))
      .catch((e) => setError(e instanceof Error ? e.message : "搜索失败"));
  }, [query]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (value.trim()) navigate(`/search?q=${encodeURIComponent(value.trim())}`);
  }

  const empty = result && result.accounts.length === 0 && result.contents.length === 0;
  return <section className="page-section search-page">
    <div className="page-heading"><span>SEARCH</span><h1>搜索</h1><p>查找创作者、标题与正文内容。</p></div>
    <form className="search-page-form" onSubmit={submit}><Search /><input value={value} onChange={(e) => setValue(e.target.value)} placeholder="输入用户名或内容关键词" autoFocus /><button>搜索</button></form>
    {error && <div className="error-banner">{error}</div>}
    {!result && !error && <Loading />}
    {empty && <EmptyState title="没有找到结果" text="换一个更短或更常见的关键词试试。" />}
    {!!result?.accounts.length && <><h2 className="section-title">创作者</h2><div className="people-grid">{result.accounts.map((account) => <Link className="person-card" to={`/profile/${account.id}`} key={account.id}><Avatar name={account.username} url={account.avatar_url} size={50} /><div><strong>{account.username}</strong><p>{account.bio || "暂时没有个人简介"}</p></div><UserRound /></Link>)}</div></>}
    {!!result?.contents.length && <><h2 className="section-title">内容</h2><div className="feed-grid">{result.contents.map((item) => <ContentCard key={item.id} item={item} />)}</div></>}
  </section>;
}
