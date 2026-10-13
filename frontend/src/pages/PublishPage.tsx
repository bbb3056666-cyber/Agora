import { FormEvent, useState } from "react";
import { FileText, Image as ImageIcon, UploadCloud, Video } from "lucide-react";
import { Navigate, useNavigate } from "react-router-dom";
import { getSession } from "../auth";
import { post, upload } from "../api";

type Kind = "article" | "image" | "video";

export default function PublishPage() {
  const session = getSession();
  const navigate = useNavigate();
  const [kind, setKind] = useState<Kind>("article");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [body, setBody] = useState("");
  const [files, setFiles] = useState<FileList | null>(null);
  const [cover, setCover] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!session) return <Navigate to="/auth" replace />;

  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      let playURL = "", coverURL = ""; let imageURLs: string[] = [];
      if (kind === "image" && files?.length) {
        const form = new FormData(); Array.from(files).forEach((file) => form.append("files", file));
        imageURLs = (await upload<{ image_urls: string[] }>("/content/upload/images", form)).image_urls;
      }
      if (kind === "video" && files?.[0]) {
        const form = new FormData(); form.append("file", files[0]);
        playURL = (await upload<{ play_url: string }>("/content/upload/video", form)).play_url;
        if (cover) { const coverForm = new FormData(); coverForm.append("file", cover); coverURL = (await upload<{ cover_url: string }>("/content/upload/cover", coverForm)).cover_url; }
      }
      const content = await post<{ id: number }>("/content/publish", { content_type: kind, title, description, body, play_url: playURL, cover_url: coverURL, image_urls: imageURLs }, true);
      navigate(`/content/${content.id}`);
    } catch (e) { setError(e instanceof Error ? e.message : "发布失败"); }
    finally { setBusy(false); }
  }

  return <section className="publish-page"><div className="page-heading"><span>CREATE</span><h1>发布新内容</h1><p>选择最适合表达的方式，分享给正在这里的人。</p></div>
    <form onSubmit={submit} className="publish-form">
      <div className="content-type-picker"><button type="button" className={kind === "article" ? "active" : ""} onClick={() => setKind("article")}><FileText />文章</button><button type="button" className={kind === "image" ? "active" : ""} onClick={() => setKind("image")}><ImageIcon />图片</button><button type="button" className={kind === "video" ? "active" : ""} onClick={() => setKind("video")}><Video />视频</button></div>
      <label><span>标题</span><input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={255} required placeholder="一句话说清楚这是什么" /></label>
      <label><span>简介</span><textarea value={description} onChange={(e) => setDescription(e.target.value)} maxLength={500} rows={3} placeholder="给读者一个继续看的理由" /></label>
      {kind === "article" && <label><span>正文</span><textarea className="body-input" value={body} onChange={(e) => setBody(e.target.value)} rows={14} required placeholder="开始写作..." /></label>}
      {kind !== "article" && <label className="file-drop"><UploadCloud /><strong>{kind === "image" ? "选择 1 至 9 张图片" : "选择 MP4 视频"}</strong><span>{files?.length ? `已选择 ${files.length} 个文件` : "点击选择本地文件"}</span><input type="file" accept={kind === "image" ? "image/jpeg,image/png,image/webp" : "video/mp4"} multiple={kind === "image"} onChange={(e) => setFiles(e.target.files)} required /></label>}
      {kind === "video" && <label className="compact-file"><span>视频封面（可选）</span><input type="file" accept="image/jpeg,image/png,image/webp" onChange={(e) => setCover(e.target.files?.[0] || null)} /></label>}
      {error && <div className="form-error">{error}</div>}
      <div className="publish-footer"><span>发布后将进入最新内容流</span><button className="submit-button" disabled={busy}>{busy ? "正在处理" : "确认发布"}</button></div>
    </form>
  </section>;
}
