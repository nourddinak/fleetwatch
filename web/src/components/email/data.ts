import { useEffect, useState } from "react";
import { api } from "../../api/client";
import { useStore } from "../../store/store";

export interface Check { status: "ok" | "missing" | "wrong" | "error" | "unknown" | ""; detail?: string }
export interface Domain {
  id: string; name: string; zoneId: string; catchAll: boolean; catchAllTo: string; senderId: string; forwardTo: string[] | null;
  spamThreshold: number; checks: { mx: Check; spf: Check; dmarc: Check; dkim: Check; routing: Check }; checkedAt: number; createdAt: number; canManage: boolean;
}
export interface Address {
  id: string; domainId: string; local: string; address: string; displayName: string; inboxId: string; receive: boolean; send: boolean; disabled: boolean;
  forwardMode: "domain" | "custom" | "off"; forwardTo: string[] | null; notify: boolean; signature: string; createdAt: number;
  unread: number; total: number; bytes: number; canSend: boolean; canManage: boolean; canRead: boolean;
}
export interface Person { name?: string; address: string }
export interface Job { id: string; messageId: number; state: "scheduled" | "sending" | "accepted" | "failed" | "unknown"; attempts: number; sendAt: number; nextAt: number; lastError?: string; acceptedAt?: number; from: string; subject?: string }
export interface Message {
  id: number; mailboxId: string; domainId: string; threadId: number; direction: "in" | "out"; folder: string; messageId: string; inReplyTo?: string; references?: string;
  rcpt: string; from: Person; to: Person[]; cc: Person[]; bcc?: Person[]; replyTo?: string; subject: string; snippet: string; size: number; attachments: number;
  unread: boolean; starred: boolean; spamScore: number; spamWhy: string[]; auth: { spf?: string; dkim?: string; dmarc?: string }; sentAt: number; receivedAt: number;
  labels: string[]; threadCount?: number; outbox?: Job;
}
export interface Attachment { part: number; filename: string; type: string; size: number; contentId?: string; scan: "not_scanned" | "blocked" }
export interface Body { html: string; text: string; attachments: Attachment[]; remoteImages: boolean; headers?: string }
export interface Folder { id: string; kind: "folder" | "label"; name: string; color: string; count: number; unread: number }
export interface FileRef { id: string; name: string; type: string; size: number }
export interface Draft { id: string; fromId: string; to: string[]; cc: string[]; bcc: string[]; subject: string; html: string; replyToId?: number; forwardId?: number; files: FileRef[]; updatedAt: number }
export interface Counts { inbox: number; spam: number; drafts: number; folders: Record<string, number>; boxes: Record<string, number> }
export interface Options { undoSeconds: number; trashDays: number; spamDays: number }
export interface Overview { domains: Domain[]; addresses: Address[]; folders: Folder[]; counts: Counts; owner: boolean; options: Options; maxMessage: number }
export interface Contact { id: number; address: string; name: string; company?: string; notes?: string; uses: number; lastUsed?: number }

export const SYSTEM_FOLDERS = [
  { id: "inbox", label: "Inbox" },
  { id: "all", label: "All mail" },
  { id: "starred", label: "Starred" },
  { id: "sent", label: "Sent" },
  { id: "drafts", label: "Drafts" },
  { id: "archive", label: "Archive" },
  { id: "spam", label: "Spam" },
  { id: "trash", label: "Trash" },
] as const;

export const act = (ids: number[], action: string, extra: { folder?: string; label?: string } = {}) =>
  api<{ changed: number }>("POST", "/api/emails/messages/actions", { ids, action, ...extra });

export function personLabel(p?: Person) {
  if (!p) return "";
  return p.name || p.address;
}

export function shortDate(unix: number) {
  const d = new Date(unix * 1000);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (d.getFullYear() === now.getFullYear()) return d.toLocaleDateString([], { month: "short", day: "numeric" });
  return d.toLocaleDateString([], { year: "numeric", month: "short", day: "numeric" });
}

export const fullDate = (unix: number) => new Date(unix * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });

/** Unread inbox mail for the sidebar badge; null when this account has no mail. Live: refetches on "emails" events. */
export function useEmailUnread() {
  const { subscribeLive } = useStore();
  const [n, setN] = useState<number | null>(null);
  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = () =>
      api<{ enabled: boolean; counts?: Counts }>("GET", "/api/emails/counts")
        .then((r) => alive && setN(r.enabled ? r.counts?.inbox ?? 0 : null))
        .catch(() => alive && setN(null));
    load();
    const off = subscribeLive((m) => {
      if (m.type !== "emails") return;
      clearTimeout(timer);
      timer = setTimeout(load, 400); // a burst of changes, one fetch
    });
    return () => {
      alive = false;
      clearTimeout(timer);
      off();
    };
  }, [subscribeLive]);
  return n;
}
