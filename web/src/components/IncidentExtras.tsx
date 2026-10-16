import { useEffect, useState } from "react";
import { BookOpen, MessageSquare, Send } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { dateTime, relTime } from "../mock/util";
import { Button, Textarea } from "./ui";

interface Comment {
  id: number;
  incidentId: string;
  ts: number;
  author: string;
  text: string;
}

/** The "how to fix" steps the owner wrote for this kind of incident. */
export function RunbookBox({ typeKey }: { typeKey: string }) {
  const { data } = useApi<{ runbooks: Record<string, string> }>("/api/runbooks");
  const text = data?.runbooks[typeKey];
  if (!text) return null;
  return (
    <div className="rounded-2xl border border-info/30 bg-info-soft p-4">
      <h4 className="mb-2 flex items-center gap-2 text-sm font-semibold"><BookOpen size={15} /> How to fix</h4>
      <div className="whitespace-pre-wrap text-sm">{text}</div>
    </div>
  );
}

/** The team's discussion of one incident, live. */
export function IncidentComments({ incidentId }: { incidentId: string }) {
  const { me, subscribeLive, toast } = useStore();
  const list = useApi<Comment[]>(`/api/incidents/${incidentId}/comments`);
  const { setData } = list;
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const canWrite = !me || me.role !== "viewer";
  useEffect(() => subscribeLive((m) => {
    if (m.type === "incident.comment" && m.data.incidentId === incidentId) setData((l) => (l && !l.some((c) => c.id === m.data.id) ? [...l, m.data] : l));
  }), [subscribeLive, setData, incidentId]);
  const send = async () => {
    setBusy(true);
    try {
      const c = await api<Comment>("POST", `/api/incidents/${incidentId}/comments`, { text });
      setData((l) => (l && !l.some((x) => x.id === c.id) ? [...l, c] : l));
      setText("");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div>
      <h4 className="mb-3 flex items-center gap-2 text-sm font-semibold"><MessageSquare size={15} /> Discussion</h4>
      <ul className="space-y-3">
        {list.data?.length === 0 && <li className="text-sm text-muted">No comments yet.</li>}
        {list.data?.map((c) => (
          <li key={c.id} className="rounded-2xl bg-surface2 px-4 py-3">
            <div className="mb-1 flex items-baseline justify-between gap-2 text-xs">
              <span className="font-semibold">{c.author}</span>
              <span className="text-muted" title={dateTime(c.ts)}>{relTime(c.ts)}</span>
            </div>
            <div className="whitespace-pre-wrap break-words text-sm">{c.text}</div>
          </li>
        ))}
      </ul>
      {canWrite && (
        <div className="mt-3 flex items-end gap-2">
          <Textarea rows={2} value={text} placeholder="What did you find? What are you trying?" onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && text.trim()) send(); }} />
          <Button size="sm" disabled={busy || !text.trim()} onClick={send} aria-label="Send comment"><Send size={14} /></Button>
        </div>
      )}
    </div>
  );
}
