import { FormEvent, useEffect, useMemo, useState } from "react";
import { CalendarDays, Check, Clock3, Plus, Ticket } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { post } from "./api";
import { getSession } from "./auth";
import type { ContentActivity } from "./types";

type RegistrationStatus = "NOT_REGISTERED" | "PENDING" | "SUCCESS" | "FAILED";
type Props = { contentID: number; authorID: number; activities: ContentActivity[]; onActivitiesChange: (items: ContentActivity[]) => void };

function toLocalInput(date: Date) {
  const offset = date.getTimezoneOffset() * 60000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}

export default function ActivityPanel({ contentID, authorID, activities, onActivitiesChange }: Props) {
  const session = getSession();
  const navigate = useNavigate();
  const [statuses, setStatuses] = useState<Record<number, RegistrationStatus>>({});
  const [creating, setCreating] = useState(false);
  const [busyID, setBusyID] = useState(0);
  const [error, setError] = useState("");
  const [form, setForm] = useState(() => ({ title: "", description: "", quota: 20, startAt: toLocalInput(new Date()), endAt: toLocalInput(new Date(Date.now() + 7 * 86400000)) }));
  const isOwner = session?.account_id === authorID;
  const activeCount = useMemo(() => activities.filter((a) => new Date(a.start_at).getTime() <= Date.now() && new Date(a.end_at).getTime() > Date.now()).length, [activities]);

  async function reload() {
    onActivitiesChange(await post<ContentActivity[]>("/content/activity/list", { content_id: contentID }) || []);
  }

  useEffect(() => {
    if (!session || !activities.length) return;
    let active = true;
    void Promise.all(activities.map(async (a) => {
      try { const r = await post<{ status: RegistrationStatus }>("/content/activity/result", { activity_id: a.id }, true); return [a.id, r.status] as const; }
      catch { return [a.id, "NOT_REGISTERED"] as const; }
    })).then((entries) => { if (active) setStatuses(Object.fromEntries(entries)); });
    return () => { active = false; };
  }, [activities.map((a) => a.id).join(","), session?.account_id]);

  async function register(activityID: number) {
    if (!session) { navigate("/auth"); return; }
    setBusyID(activityID); setError("");
    try {
      await post("/content/activity/register", { activity_id: activityID }, true);
      setStatuses((old) => ({ ...old, [activityID]: "PENDING" }));
      for (let attempt = 0; attempt < 6; attempt += 1) {
        await new Promise((resolve) => window.setTimeout(resolve, 450));
        const result = await post<{ status: RegistrationStatus }>("/content/activity/result", { activity_id: activityID }, true);
        setStatuses((old) => ({ ...old, [activityID]: result.status }));
        if (result.status !== "PENDING") break;
      }
      await reload();
    } catch (e) { setError(e instanceof Error ? e.message : "报名失败"); }
    finally { setBusyID(0); }
  }

  async function createActivity(event: FormEvent) {
    event.preventDefault();
    const startAt = Math.floor(new Date(form.startAt).getTime() / 1000);
    const endAt = Math.floor(new Date(form.endAt).getTime() / 1000);
    if (!form.title.trim() || !startAt || endAt <= startAt) { setError("请填写完整活动信息，并确保结束时间晚于开始时间"); return; }
    setBusyID(-1); setError("");
    try {
      await post("/content/activity/create", { content_id: contentID, title: form.title.trim(), description: form.description.trim(), quota: form.quota, start_at: startAt, end_at: endAt }, true);
      setForm((old) => ({ ...old, title: "", description: "" })); setCreating(false); await reload();
    } catch (e) { setError(e instanceof Error ? e.message : "创建活动失败"); }
    finally { setBusyID(0); }
  }

  return <div className="activity-panel">
    <div className="activity-summary"><div><CalendarDays /><span><strong>{activities.length}</strong> 个活动</span></div><span>{activeCount} 个进行中</span></div>
    {isOwner && <button className="activity-create-toggle" onClick={() => setCreating((v) => !v)}><Plus />{creating ? "收起创建表单" : "发起新活动"}</button>}
    {creating && <form className="activity-create-form" onSubmit={createActivity}>
      <label>活动名称<input value={form.title} maxLength={100} onChange={(e) => setForm({ ...form, title: e.target.value })} placeholder="例如：周末城市摄影同行" /></label>
      <label>活动说明<textarea value={form.description} maxLength={500} onChange={(e) => setForm({ ...form, description: e.target.value })} placeholder="告诉大家活动内容和参与方式" /></label>
      <div><label>名额<input type="number" min="1" max="100000" value={form.quota} onChange={(e) => setForm({ ...form, quota: Number(e.target.value) })} /></label><label>开始时间<input type="datetime-local" value={form.startAt} onChange={(e) => setForm({ ...form, startAt: e.target.value })} /></label><label>结束时间<input type="datetime-local" value={form.endAt} onChange={(e) => setForm({ ...form, endAt: e.target.value })} /></label></div>
      <button disabled={busyID === -1}>{busyID === -1 ? "正在创建" : "确认发布"}</button>
    </form>}
    {error && <div className="activity-error">{error}</div>}
    <div className="activity-list">{activities.length ? activities.map((a) => {
      const now = Date.now(), notStarted = new Date(a.start_at).getTime() > now, ended = new Date(a.end_at).getTime() <= now, full = a.remaining_quota <= 0;
      const status = statuses[a.id] || "NOT_REGISTERED";
      const disabled = isOwner || notStarted || ended || full || status === "PENDING" || status === "SUCCESS" || busyID === a.id;
      const label = isOwner ? "你发起的活动" : status === "SUCCESS" ? "报名成功" : status === "PENDING" ? "处理中" : ended ? "已结束" : notStarted ? "未开始" : full ? "名额已满" : "立即报名";
      return <article className="activity-card" key={a.id}><div className="activity-card-heading"><Ticket /><div><h3>{a.title}</h3><span>剩余 {a.remaining_quota} / {a.quota} 个名额</span></div></div>{a.description && <p>{a.description}</p>}<div className="activity-time"><Clock3 />{formatTime(a.start_at)} - {formatTime(a.end_at)}</div><button className={status === "SUCCESS" ? "registered" : ""} disabled={disabled} onClick={() => void register(a.id)}>{status === "SUCCESS" && <Check />}{label}</button></article>;
    }) : <div className="activity-empty"><CalendarDays /><strong>暂时没有活动</strong><span>{isOwner ? "发起一个活动，邀请大家一起参与。" : "作者还没有发布相关活动。"}</span></div>}</div>
  </div>;
}
