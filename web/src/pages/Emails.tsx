import { useEffect, useState } from "react";
import {
  ArrowLeft,
  Check,
  Copy,
  FileText,
  Globe,
  Inbox,
  Mail,
  Paperclip,
  Plus,
  RefreshCw,
  Reply,
  Search,
  Send,
  Settings,
  ShieldCheck,
  Star,
  Trash2,
  X,
} from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  Modal,
  PageHeader,
  Spinner,
  Table,
  Td,
  Tr,
} from "../components/ui";
import { relTime } from "../mock/util";

export interface EmailDomain {
  id: string;
  domain: string;
  cloudflareZoneId?: string;
  inboundMode: "inbox" | "forward" | "both";
  forwardEmail?: string;
  outboundProvider: "resend" | "smtp";
  resendDomainId?: string;
  hasResendKey?: boolean;
  resendApiKey?: string;
  smtpConfig?: {
    host: string;
    port: number;
    security: string;
    username: string;
    password?: string;
    hasPassword?: boolean;
    from: string;
    fromName?: string;
  };
  dkimStatus: string;
  spfStatus: string;
  mxStatus: string;
  createdAt: number;
}

export interface EmailAlias {
  id: string;
  domainId: string;
  domainName?: string;
  localPart: string;
  fullAddress: string;
  displayName?: string;
  deliveryMode: "inbox" | "forward" | "both";
  forwardTo?: string;
  isActive: boolean;
  createdAt: number;
}

export interface EmailMessage {
  id: string;
  threadId: string;
  domainId: string;
  aliasId?: string;
  direction: "inbound" | "outbound";
  folder: "inbox" | "sent" | "trash";
  fromAddress: string;
  fromName?: string;
  toAddresses: string[];
  ccAddresses?: string[];
  bccAddresses?: string[];
  replyTo?: string;
  subject: string;
  snippet?: string;
  bodyText?: string;
  bodyHtml?: string;
  messageId?: string;
  inReplyTo?: string;
  references?: string;
  hasAttachments: boolean;
  isRead: boolean;
  isStarred: boolean;
  receivedAt: number;
  attachments?: {
    id: string;
    messageId: string;
    filename: string;
    contentType: string;
    sizeBytes: number;
  }[];
}

type Tab = "inbox" | "sent" | "trash" | "domains";

export function Emails() {
  const { toast, subscribeLive } = useStore();
  const [tab, setTab] = useState<Tab>("inbox");
  const [selectedDomain, setSelectedDomain] = useState<string>("all");
  const [selectedMessage, setSelectedMessage] = useState<EmailMessage | null>(null);
  const [search, setSearch] = useState("");
  const [composeOpen, setComposeOpen] = useState(false);
  const [addDomainOpen, setAddDomainOpen] = useState(false);
  const [addAliasOpen, setAddAliasOpen] = useState(false);
  const [workerModalOpen, setWorkerModalOpen] = useState(false);

  // Data fetching
  const domains = useApi<EmailDomain[]>("/api/emails/domains");
  const aliases = useApi<EmailAlias[]>("/api/emails/aliases");
  const [messages, setMessages] = useState<EmailMessage[]>([]);
  const [totalMessages, setTotalMessages] = useState(0);
  const [loadingMessages, setLoadingMessages] = useState(false);

  // Load messages whenever folder, selected domain, or tab changes
  const loadMessages = async () => {
    if (tab === "domains") return;
    setLoadingMessages(true);
    try {
      const q = new URLSearchParams();
      q.set("folder", tab);
      if (selectedDomain !== "all") q.set("domainId", selectedDomain);
      if (search) q.set("q", search);
      q.set("limit", "50");

      const res = await api<{ messages: EmailMessage[]; total: number }>(
        "GET",
        `/api/emails/messages?${q.toString()}`
      );
      setMessages(res.messages || []);
      setTotalMessages(res.total || 0);

      // Refresh currently selected message if still present
      if (selectedMessage) {
        const updated = (res.messages || []).find((m) => m.id === selectedMessage.id);
        if (updated) setSelectedMessage(updated);
      }
    } catch (e) {
      toast((e as ApiError).message, "err");
    } finally {
      setLoadingMessages(false);
    }
  };

  useEffect(() => {
    loadMessages();
  }, [tab, selectedDomain, search]);

  // Live WebSocket updates
  useEffect(() => {
    return subscribeLive((m) => {
      if (m.type.startsWith("emails.")) {
        loadMessages();
        domains.reload();
        aliases.reload();
      }
    });
  }, [tab, selectedDomain]);

  // Read message details
  const openMessage = async (msg: EmailMessage) => {
    setSelectedMessage(msg);
    try {
      const full = await api<EmailMessage>("GET", `/api/emails/messages/${msg.id}`);
      setSelectedMessage(full);
      // update unread state in local list
      setMessages((prev) => prev.map((m) => (m.id === msg.id ? { ...m, isRead: true } : m)));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };

  // Toggle star
  const toggleStar = async (msg: EmailMessage, e?: React.MouseEvent) => {
    e?.stopPropagation();
    try {
      const next = !msg.isStarred;
      await api("PUT", `/api/emails/messages/${msg.id}/star`, { starred: next });
      setMessages((prev) => prev.map((m) => (m.id === msg.id ? { ...m, isStarred: next } : m)));
      if (selectedMessage?.id === msg.id) {
        setSelectedMessage({ ...selectedMessage, isStarred: next });
      }
    } catch (err) {
      toast((err as ApiError).message, "err");
    }
  };

  // Delete message
  const handleDelete = async (id: string) => {
    try {
      await api("DELETE", `/api/emails/messages/${id}`);
      toast("Message moved to trash");
      if (selectedMessage?.id === id) setSelectedMessage(null);
      loadMessages();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };

  // 1-Click Cloudflare DNS setup
  const setupDNS = async (domId: string) => {
    try {
      await api("POST", `/api/emails/domains/${domId}/setup-dns`);
      toast("Cloudflare DNS configured: MX, SPF, and DKIM records added!");
      domains.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };

  // Compose State
  const [replyToMsg, setReplyToMsg] = useState<EmailMessage | null>(null);

  const startReply = (msg: EmailMessage) => {
    setReplyToMsg(msg);
    setComposeOpen(true);
  };

  const startNewCompose = () => {
    setReplyToMsg(null);
    setComposeOpen(true);
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Emails"
        sub="Custom domain mailboxes: send, receive, and forward messages with your Cloudflare domains."
        actions={
          <div className="flex items-center gap-2">
            <Button variant="secondary" onClick={() => setWorkerModalOpen(true)}>
              <FileText size={16} className="mr-1.5" />
              Worker Setup
            </Button>
            <Button variant="secondary" onClick={() => setAddDomainOpen(true)}>
              <Globe size={16} className="mr-1.5" />
              Add Domain
            </Button>
            <Button variant="primary" onClick={startNewCompose}>
              <Plus size={16} className="mr-1.5" />
              Compose
            </Button>
          </div>
        }
      />

      {/* Tabs and Navigation */}
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-line pb-4">
        <div className="flex items-center gap-2">
          <Button
            variant={tab === "inbox" ? "primary" : "ghost"}
            size="sm"
            onClick={() => {
              setTab("inbox");
              setSelectedMessage(null);
            }}
          >
            <Inbox size={15} className="mr-1.5" />
            Inbox
          </Button>
          <Button
            variant={tab === "sent" ? "primary" : "ghost"}
            size="sm"
            onClick={() => {
              setTab("sent");
              setSelectedMessage(null);
            }}
          >
            <Send size={15} className="mr-1.5" />
            Sent
          </Button>
          <Button
            variant={tab === "trash" ? "primary" : "ghost"}
            size="sm"
            onClick={() => {
              setTab("trash");
              setSelectedMessage(null);
            }}
          >
            <Trash2 size={15} className="mr-1.5" />
            Trash
          </Button>
          <Button
            variant={tab === "domains" ? "primary" : "ghost"}
            size="sm"
            onClick={() => {
              setTab("domains");
              setSelectedMessage(null);
            }}
          >
            <Settings size={15} className="mr-1.5" />
            Domains & Aliases
          </Button>
        </div>

        {tab !== "domains" && (
          <div className="flex flex-wrap items-center gap-3">
            {/* Domain filter dropdown */}
            <select
              value={selectedDomain}
              onChange={(e) => setSelectedDomain(e.target.value)}
              className="h-9 rounded-lg border border-line bg-surface px-3 text-xs font-medium text-fg focus:outline-none focus:ring-1 focus:ring-accent"
            >
              <option value="all">All Domains</option>
              {(domains.data || []).map((d) => (
                <option key={d.id} value={d.id}>
                  @{d.domain}
                </option>
              ))}
            </select>

            {/* Search */}
            <div className="relative">
              <Search size={14} className="absolute left-3 top-2.5 text-muted" />
              <input
                type="text"
                placeholder="Search messages…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-9 w-48 rounded-lg border border-line bg-surface pl-8 pr-3 text-xs placeholder:text-muted/60 focus:outline-none focus:ring-1 focus:ring-accent sm:w-64"
              />
              {search && (
                <button
                  onClick={() => setSearch("")}
                  className="absolute right-2.5 top-2.5 text-muted hover:text-fg"
                >
                  <X size={14} />
                </button>
              )}
            </div>

            <Button variant="ghost" size="sm" onClick={loadMessages} title="Refresh">
              <RefreshCw size={14} />
            </Button>
          </div>
        )}
      </div>

      {/* Main Content Area */}
      {tab === "domains" ? (
        <DomainsAndAliasesView
          domains={domains.data || []}
          aliases={aliases.data || []}
          onReloadDomains={() => domains.reload()}
          onReloadAliases={() => aliases.reload()}
          onOpenAddAlias={() => setAddAliasOpen(true)}
          onSetupDNS={setupDNS}
          onOpenWorkerSetup={() => setWorkerModalOpen(true)}
        />
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-12">
          {/* Message List */}
          <div className={cn("lg:col-span-5", selectedMessage && "hidden lg:block")}>
            <Card pad={false} className="overflow-hidden">
              <div className="flex items-center justify-between border-b border-line bg-surface2 px-4 py-2.5 text-xs text-muted">
                <span>
                  {totalMessages} {totalMessages === 1 ? "message" : "messages"}
                </span>
                {loadingMessages && <Spinner size={14} />}
              </div>

              {messages.length === 0 ? (
                <EmptyState
                  icon={<Mail size={32} className="text-muted/60" />}
                  title={tab === "inbox" ? "Inbox is empty" : `No ${tab} messages`}
                  desc={
                    tab === "inbox"
                      ? "Incoming messages routed from your Cloudflare domains will show up here."
                      : "Messages you send will appear here."
                  }
                />
              ) : (
                <div className="divide-y divide-line max-h-[700px] overflow-y-auto">
                  {messages.map((m) => {
                    const active = selectedMessage?.id === m.id;
                    return (
                      <div
                        key={m.id}
                        onClick={() => openMessage(m)}
                        className={cn(
                          "group relative flex cursor-pointer items-start gap-3 p-3.5 transition hover:bg-surface2/60",
                          active ? "bg-accent/10 hover:bg-accent/15" : !m.isRead ? "bg-surface" : "bg-bg/40"
                        )}
                      >
                        {/* Star */}
                        <button
                          type="button"
                          onClick={(e) => toggleStar(m, e)}
                          className="mt-0.5 text-muted transition hover:text-warn"
                        >
                          <Star
                            size={16}
                            className={cn(m.isStarred ? "fill-warn text-warn" : "text-muted/40")}
                          />
                        </button>

                        {/* Content */}
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center justify-between gap-2">
                            <span
                              className={cn(
                                "truncate text-xs",
                                !m.isRead ? "font-semibold text-fg" : "text-muted"
                              )}
                            >
                              {tab === "sent"
                                ? `To: ${m.toAddresses.join(", ")}`
                                : m.fromName || m.fromAddress}
                            </span>
                            <span className="shrink-0 text-[11px] text-muted">
                              {relTime(m.receivedAt)}
                            </span>
                          </div>

                          <div
                            className={cn(
                              "truncate text-sm",
                              !m.isRead ? "font-medium text-fg" : "text-fg/80"
                            )}
                          >
                            {m.subject}
                          </div>

                          <p className="line-clamp-2 text-xs text-muted/80">
                            {m.snippet || "—"}
                          </p>
                        </div>

                        {/* Badges / indicators */}
                        <div className="flex flex-col items-end gap-1">
                          {!m.isRead && (
                            <span className="h-2 w-2 rounded-full bg-accent" title="Unread" />
                          )}
                          {m.hasAttachments && (
                            <span title="Has attachment">
                              <Paperclip size={13} className="text-muted/60" />
                            </span>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </Card>
          </div>

          {/* Message Reader Detail */}
          <div className="lg:col-span-7">
            {selectedMessage ? (
              <Card className="space-y-4">
                {/* Back button on mobile */}
                <div className="flex items-center justify-between border-b border-line pb-3">
                  <div className="flex items-center gap-2">
                    <button
                      onClick={() => setSelectedMessage(null)}
                      className="rounded-lg p-1 text-muted hover:bg-surface2 hover:text-fg lg:hidden"
                    >
                      <ArrowLeft size={18} />
                    </button>
                    <button
                      onClick={() => toggleStar(selectedMessage)}
                      className="text-muted hover:text-warn"
                    >
                      <Star
                        size={18}
                        className={cn(
                          selectedMessage.isStarred ? "fill-warn text-warn" : "text-muted/40"
                        )}
                      />
                    </button>
                    <h2 className="text-lg font-semibold text-fg truncate">
                      {selectedMessage.subject}
                    </h2>
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1.5">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => startReply(selectedMessage)}
                    >
                      <Reply size={14} className="mr-1.5" />
                      Reply
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleDelete(selectedMessage.id)}
                      className="text-crit hover:bg-crit/10"
                    >
                      <Trash2 size={14} />
                    </Button>
                  </div>
                </div>

                {/* Sender & Recipient Metadata */}
                <div className="flex flex-wrap items-start justify-between gap-2 rounded-xl bg-surface2/40 p-3 text-xs">
                  <div className="space-y-1">
                    <div>
                      <span className="font-semibold text-fg">From: </span>
                      <span className="text-muted">
                        {selectedMessage.fromName
                          ? `${selectedMessage.fromName} <${selectedMessage.fromAddress}>`
                          : selectedMessage.fromAddress}
                      </span>
                    </div>
                    <div>
                      <span className="font-semibold text-fg">To: </span>
                      <span className="text-muted">
                        {selectedMessage.toAddresses.join(", ")}
                      </span>
                    </div>
                    {selectedMessage.ccAddresses && selectedMessage.ccAddresses.length > 0 && (
                      <div>
                        <span className="font-semibold text-fg">Cc: </span>
                        <span className="text-muted">
                          {selectedMessage.ccAddresses.join(", ")}
                        </span>
                      </div>
                    )}
                  </div>
                  <div className="text-muted">
                    {new Date(selectedMessage.receivedAt).toLocaleString()}
                  </div>
                </div>

                {/* Attachments list if any */}
                {selectedMessage.attachments && selectedMessage.attachments.length > 0 && (
                  <div className="flex flex-wrap gap-2 pt-1">
                    {selectedMessage.attachments.map((att) => (
                      <a
                        key={att.id}
                        href={`/api/emails/attachments/${att.id}`}
                        download={att.filename}
                        className="flex items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-xs text-fg transition hover:bg-surface2"
                      >
                        <Paperclip size={14} className="text-accent" />
                        <span className="font-medium truncate max-w-[200px]">
                          {att.filename}
                        </span>
                        <span className="text-muted text-[11px]">
                          ({Math.round(att.sizeBytes / 1024)} KB)
                        </span>
                      </a>
                    ))}
                  </div>
                )}

                {/* Body Content */}
                <div className="min-h-[280px] rounded-xl border border-line/50 bg-surface/30 p-4 text-sm text-fg">
                  {selectedMessage.bodyHtml ? (
                    <div
                      className="prose dark:prose-invert max-w-none text-sm break-words"
                      dangerouslySetInnerHTML={{ __html: selectedMessage.bodyHtml }}
                    />
                  ) : (
                    <pre className="whitespace-pre-wrap font-sans text-sm leading-relaxed text-fg">
                      {selectedMessage.bodyText || "(No message content)"}
                    </pre>
                  )}
                </div>

                {/* Bottom Quick Reply Trigger */}
                <div className="pt-2">
                  <Button variant="secondary" onClick={() => startReply(selectedMessage)}>
                    <Reply size={15} className="mr-1.5" />
                    Quick Reply
                  </Button>
                </div>
              </Card>
            ) : (
              <Card className="flex min-h-[380px] flex-col items-center justify-center text-center text-muted">
                <Mail size={40} className="mb-2 text-muted/40" />
                <p className="text-sm font-medium">Select a message from the list to view</p>
                <p className="text-xs text-muted/70">Or click Compose to write an email</p>
              </Card>
            )}
          </div>
        </div>
      )}

      {/* Compose / Reply Modal */}
      <ComposeModal
        open={composeOpen}
        onClose={() => setComposeOpen(false)}
        replyTo={replyToMsg}
        aliases={aliases.data || []}
        onSent={() => {
          setComposeOpen(false);
          loadMessages();
        }}
      />

      {/* Add Domain Modal */}
      <AddDomainModal
        open={addDomainOpen}
        onClose={() => setAddDomainOpen(false)}
        onCreated={() => {
          setAddDomainOpen(false);
          domains.reload();
        }}
      />

      {/* Add Alias Modal */}
      <AddAliasModal
        open={addAliasOpen}
        onClose={() => setAddAliasOpen(false)}
        domains={domains.data || []}
        onCreated={() => {
          setAddAliasOpen(false);
          aliases.reload();
        }}
      />

      {/* Worker Setup Modal */}
      <WorkerSetupModal
        open={workerModalOpen}
        onClose={() => setWorkerModalOpen(false)}
      />
    </div>
  );
}

// ==========================================
// Domains and Aliases Management View
// ==========================================
function DomainsAndAliasesView({
  domains,
  aliases,
  onReloadDomains,
  onReloadAliases,
  onOpenAddAlias,
  onSetupDNS,
  onOpenWorkerSetup,
}: {
  domains: EmailDomain[];
  aliases: EmailAlias[];
  onReloadDomains: () => void;
  onReloadAliases: () => void;
  onOpenAddAlias: () => void;
  onSetupDNS: (id: string) => void;
  onOpenWorkerSetup: () => void;
}) {
  const { toast } = useStore();
  const [deleteDomId, setDeleteDomId] = useState<string | null>(null);
  const [deleteAliasId, setDeleteAliasId] = useState<string | null>(null);

  const confirmDeleteDomain = async () => {
    if (!deleteDomId) return;
    try {
      await api("DELETE", `/api/emails/domains/${deleteDomId}`);
      toast("Domain removed");
      onReloadDomains();
      onReloadAliases();
    } catch (e) {
      toast((e as ApiError).message, "err");
    } finally {
      setDeleteDomId(null);
    }
  };

  const confirmDeleteAlias = async () => {
    if (!deleteAliasId) return;
    try {
      await api("DELETE", `/api/emails/aliases/${deleteAliasId}`);
      toast("Alias removed");
      onReloadAliases();
    } catch (e) {
      toast((e as ApiError).message, "err");
    } finally {
      setDeleteAliasId(null);
    }
  };

  return (
    <div className="space-y-8">
      {/* Domains Section */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-base font-semibold text-fg">Configured Domains</h3>
            <p className="text-xs text-muted">
              Domains connected to Cloudflare for receiving and sending emails.
            </p>
          </div>
        </div>

        {domains.length === 0 ? (
          <Card>
            <EmptyState
              icon={<Globe size={32} className="text-muted/60" />}
              title="No email domains added yet"
              desc="Add your custom domain to create email addresses like contact@domain.com."
            />
          </Card>
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            {domains.map((d) => (
              <Card key={d.id} className="space-y-4">
                <div className="flex items-start justify-between">
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-base font-bold text-fg">@{d.domain}</span>
                      <Badge tone={d.outboundProvider === "resend" ? "ok" : "accent"}>
                        {d.outboundProvider === "resend" ? "Resend" : "SMTP"}
                      </Badge>
                    </div>
                    <p className="text-xs text-muted">
                      Inbound mode: <span className="font-semibold text-fg">{d.inboundMode}</span>
                      {d.forwardEmail && (
                        <span>
                          {" "}
                          → forwarding to <span className="text-fg">{d.forwardEmail}</span>
                        </span>
                      )}
                    </p>
                  </div>
                  <button
                    onClick={() => setDeleteDomId(d.id)}
                    className="text-muted transition hover:text-crit"
                    title="Remove domain"
                  >
                    <Trash2 size={16} />
                  </button>
                </div>

                {/* DNS Records status */}
                <div className="flex flex-wrap items-center gap-2 border-t border-line pt-3 text-xs">
                  <span className="text-muted">DNS Status:</span>
                  <Badge tone={d.mxStatus === "active" ? "ok" : "warn"}>
                    MX: {d.mxStatus}
                  </Badge>
                  <Badge tone={d.spfStatus === "active" ? "ok" : "warn"}>
                    SPF: {d.spfStatus}
                  </Badge>
                  <Badge tone={d.dkimStatus === "active" ? "ok" : "warn"}>
                    DKIM: {d.dkimStatus}
                  </Badge>
                </div>

                {/* Action buttons */}
                <div className="flex items-center justify-between border-t border-line pt-3">
                  <Button variant="secondary" size="sm" onClick={() => onSetupDNS(d.id)}>
                    <ShieldCheck size={14} className="mr-1.5 text-ok" />
                    1-Click Cloudflare DNS
                  </Button>
                  <Button variant="ghost" size="sm" onClick={onOpenWorkerSetup}>
                    Worker Script
                  </Button>
                </div>
              </Card>
            ))}
          </div>
        )}
      </div>

      {/* Aliases Section */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-base font-semibold text-fg">Email Addresses & Aliases</h3>
            <p className="text-xs text-muted">
              Individual mailboxes with custom routing rules (Inbox, Forward to Gmail, or Both).
            </p>
          </div>
          <Button variant="primary" size="sm" onClick={onOpenAddAlias}>
            <Plus size={14} className="mr-1.5" />
            Add Address
          </Button>
        </div>

        {aliases.length === 0 ? (
          <Card>
            <EmptyState
              icon={<Mail size={32} className="text-muted/60" />}
              title="No aliases configured"
              desc="Create aliases like contact@ or noreply@ to start sending and receiving."
            />
          </Card>
        ) : (
          <Card pad={false} className="overflow-hidden">
            <Table head={["Address", "Display Name", "Routing Mode", "Destination / Forward", "Actions"]}>
              {aliases.map((a) => (
                <Tr key={a.id}>
                  <Td className="font-mono font-medium text-fg">{a.fullAddress}</Td>
                  <Td className="text-muted">{a.displayName || "—"}</Td>
                  <Td>
                    <Badge
                      tone={
                        a.deliveryMode === "both"
                          ? "ok"
                          : a.deliveryMode === "forward"
                          ? "warn"
                          : "accent"
                      }
                    >
                      {a.deliveryMode === "both"
                        ? "Inbox + Forward"
                        : a.deliveryMode === "forward"
                        ? "Forward to Gmail"
                        : "Fleetwatch Inbox"}
                    </Badge>
                  </Td>
                  <Td className="text-muted">{a.forwardTo || "Fleetwatch Local"}</Td>
                  <Td className="text-right">
                    <button
                      onClick={() => setDeleteAliasId(a.id)}
                      className="p-1 text-muted transition hover:text-crit"
                      title="Delete alias"
                    >
                      <Trash2 size={16} />
                    </button>
                  </Td>
                </Tr>
              ))}
            </Table>
          </Card>
        )}
      </div>

      {/* Delete domain confirmation */}
      <ConfirmDialog
        open={!!deleteDomId}
        title="Remove Email Domain"
        message="Are you sure you want to remove this domain? All its aliases and stored messages will remain or be permanently affected."
        danger
        confirmLabel="Remove Domain"
        onConfirm={confirmDeleteDomain}
        onCancel={() => setDeleteDomId(null)}
      />

      {/* Delete alias confirmation */}
      <ConfirmDialog
        open={!!deleteAliasId}
        title="Remove Email Alias"
        message="Are you sure you want to delete this email address alias?"
        danger
        confirmLabel="Delete Alias"
        onConfirm={confirmDeleteAlias}
        onCancel={() => setDeleteAliasId(null)}
      />
    </div>
  );
}

// ==========================================
// Compose & Reply Modal
// ==========================================
function ComposeModal({
  open,
  onClose,
  replyTo,
  aliases,
  onSent,
}: {
  open: boolean;
  onClose: () => void;
  replyTo: EmailMessage | null;
  aliases: EmailAlias[];
  onSent: () => void;
}) {
  const { toast } = useStore();
  const [fromAddress, setFromAddress] = useState("");
  const [to, setTo] = useState("");
  const [cc, setCc] = useState("");
  const [bcc, setBcc] = useState("");
  const [subject, setSubject] = useState("");
  const [bodyText, setBodyText] = useState("");
  const [showCc, setShowCc] = useState(false);
  const [sending, setSending] = useState(false);
  const [files, setFiles] = useState<{ filename: string; contentType: string; dataBase64: string }[]>(
    []
  );

  // Initialize values when opened or replying
  useEffect(() => {
    if (replyTo) {
      // Find matching alias that received it or default
      const receivingTo = replyTo.toAddresses[0] || "";
      const matched = aliases.find(
        (a) => a.fullAddress.toLowerCase() === receivingTo.toLowerCase()
      );
      setFromAddress(matched ? matched.fullAddress : aliases[0]?.fullAddress || "");
      setTo(replyTo.fromAddress);
      setSubject(
        replyTo.subject.startsWith("Re:") ? replyTo.subject : `Re: ${replyTo.subject}`
      );
      setBodyText(
        `\n\nOn ${new Date(replyTo.receivedAt).toLocaleString()}, ${replyTo.fromAddress} wrote:\n> ${
          replyTo.bodyText ? replyTo.bodyText.replace(/\n/g, "\n> ") : ""
        }`
      );
    } else {
      setFromAddress(aliases[0]?.fullAddress || "");
      setTo("");
      setCc("");
      setBcc("");
      setSubject("");
      setBodyText("");
    }
    setFiles([]);
  }, [replyTo, aliases, open]);

  const handleFileUpload = (e: React.ChangeEvent<HTMLInputElement>) => {
    const picked = e.target.files;
    if (!picked || picked.length === 0) return;
    for (let i = 0; i < picked.length; i++) {
      const file = picked[i];
      const reader = new FileReader();
      reader.onload = () => {
        const result = reader.result as string;
        const b64 = result.split(",")[1] || "";
        setFiles((prev) => [
          ...prev,
          {
            filename: file.name,
            contentType: file.type || "application/octet-stream",
            dataBase64: b64,
          },
        ]);
      };
      reader.readAsDataURL(file);
    }
  };

  const removeFile = (idx: number) => {
    setFiles((prev) => prev.filter((_, i) => i !== idx));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!fromAddress) {
      toast("Please select a From address", "err");
      return;
    }
    const toList = to.split(",").map((s) => s.trim()).filter(Boolean);
    if (toList.length === 0) {
      toast("At least one recipient is required", "err");
      return;
    }

    setSending(true);
    try {
      await api("POST", "/api/emails/send", {
        fromAddress,
        to: toList,
        cc: cc ? cc.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
        bcc: bcc ? bcc.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
        subject,
        bodyText,
        inReplyTo: replyTo?.messageId,
        references: replyTo?.references ? `${replyTo.references} ${replyTo.messageId}` : replyTo?.messageId,
        files: files.length > 0 ? files : undefined,
      });
      toast("Email sent successfully!");
      onSent();
    } catch (err) {
      toast((err as ApiError).message, "err");
    } finally {
      setSending(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      wide
      title={replyTo ? "Reply to Email" : "Compose Email"}
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        {/* From address selector */}
        <div className="flex items-center gap-2">
          <label className="w-16 shrink-0 text-xs font-semibold text-muted">From:</label>
          <select
            value={fromAddress}
            onChange={(e) => setFromAddress(e.target.value)}
            className="h-9 flex-1 rounded-lg border border-line bg-surface px-3 font-mono text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          >
            {aliases.map((a) => (
              <option key={a.id} value={a.fullAddress}>
                {a.displayName ? `${a.displayName} <${a.fullAddress}>` : a.fullAddress}
              </option>
            ))}
          </select>
        </div>

        {/* To input */}
        <div className="flex items-center gap-2">
          <label className="w-16 shrink-0 text-xs font-semibold text-muted">To:</label>
          <input
            type="text"
            required
            placeholder="recipient@example.com, other@example.com"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            className="h-9 flex-1 rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          />
          <button
            type="button"
            onClick={() => setShowCc(!showCc)}
            className="text-xs text-muted hover:text-fg font-medium"
          >
            {showCc ? "Hide Cc" : "Cc / Bcc"}
          </button>
        </div>

        {/* Optional Cc / Bcc */}
        {showCc && (
          <div className="space-y-2 border-y border-line/50 py-2">
            <div className="flex items-center gap-2">
              <label className="w-16 shrink-0 text-xs font-semibold text-muted">Cc:</label>
              <input
                type="text"
                placeholder="cc@example.com"
                value={cc}
                onChange={(e) => setCc(e.target.value)}
                className="h-8 flex-1 rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
              />
            </div>
            <div className="flex items-center gap-2">
              <label className="w-16 shrink-0 text-xs font-semibold text-muted">Bcc:</label>
              <input
                type="text"
                placeholder="bcc@example.com"
                value={bcc}
                onChange={(e) => setBcc(e.target.value)}
                className="h-8 flex-1 rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
              />
            </div>
          </div>
        )}

        {/* Subject */}
        <div className="flex items-center gap-2">
          <label className="w-16 shrink-0 text-xs font-semibold text-muted">Subject:</label>
          <input
            type="text"
            required
            placeholder="Subject line"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            className="h-9 flex-1 rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </div>

        {/* Body Textarea */}
        <textarea
          rows={10}
          required
          placeholder="Write your email message here…"
          value={bodyText}
          onChange={(e) => setBodyText(e.target.value)}
          className="w-full rounded-xl border border-line bg-surface p-3 font-sans text-sm text-fg focus:outline-none focus:ring-1 focus:ring-accent"
        />

        {/* File attachments */}
        {files.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {files.map((f, i) => (
              <span
                key={i}
                className="flex items-center gap-1.5 rounded-lg border border-line bg-surface px-2.5 py-1 text-xs text-fg"
              >
                <Paperclip size={12} className="text-accent" />
                <span className="truncate max-w-[150px]">{f.filename}</span>
                <button
                  type="button"
                  onClick={() => removeFile(i)}
                  className="text-muted hover:text-crit"
                >
                  <X size={12} />
                </button>
              </span>
            ))}
          </div>
        )}

        {/* Actions bar */}
        <div className="flex items-center justify-between border-t border-line pt-3">
          <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted hover:text-fg">
            <Paperclip size={15} />
            <span>Attach file</span>
            <input
              type="file"
              multiple
              onChange={handleFileUpload}
              className="hidden"
            />
          </label>

          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={sending}>
              {sending ? <span className="mr-1.5 inline-flex"><Spinner size={14} /></span> : <Send size={14} className="mr-1.5" />}
              Send Email
            </Button>
          </div>
        </div>
      </form>
    </Modal>
  );
}

// ==========================================
// Add Domain Modal
// ==========================================
function AddDomainModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { toast } = useStore();
  const [domain, setDomain] = useState("");
  const [inboundMode, setInboundMode] = useState<"inbox" | "forward" | "both">("both");
  const [forwardEmail, setForwardEmail] = useState("");
  const [outboundProvider, setOutboundProvider] = useState<"resend" | "smtp">("resend");
  const [resendApiKey, setResendApiKey] = useState("");
  const [saving, setSaving] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      await api("POST", "/api/emails/domains", {
        domain,
        inboundMode,
        forwardEmail,
        outboundProvider,
        resendApiKey,
      });
      toast(`Domain ${domain} added! Default contact@ and noreply@ aliases created.`);
      onCreated();
    } catch (err) {
      toast((err as ApiError).message, "err");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Add Email Domain">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="text-xs font-semibold text-fg">Domain Name</label>
          <input
            type="text"
            required
            placeholder="e.g. example.com"
            value={domain}
            onChange={(e) => setDomain(e.target.value.toLowerCase())}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 font-mono text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">Inbound Receiving Mode</label>
          <select
            value={inboundMode}
            onChange={(e) => setInboundMode(e.target.value as any)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          >
            <option value="both">Both: Keep in Fleetwatch Inbox & Forward to Gmail</option>
            <option value="inbox">Fleetwatch Inbox Only</option>
            <option value="forward">Forward to Gmail Only</option>
          </select>
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">
            Forward to Email (optional Gmail address)
          </label>
          <input
            type="email"
            placeholder="yourname@gmail.com"
            value={forwardEmail}
            onChange={(e) => setForwardEmail(e.target.value)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          />
          <p className="mt-1 text-[11px] text-muted">
            Incoming emails will be automatically forwarded to this address when forwarding is enabled.
          </p>
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">Outbound Sending Provider</label>
          <select
            value={outboundProvider}
            onChange={(e) => setOutboundProvider(e.target.value as any)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          >
            <option value="resend">Resend API (Automatic DKIM & SPF)</option>
            <option value="smtp">Custom SMTP Server</option>
          </select>
        </div>

        {outboundProvider === "resend" && (
          <div>
            <label className="text-xs font-semibold text-fg">Resend API Key</label>
            <input
              type="password"
              placeholder="re_123456789..."
              value={resendApiKey}
              onChange={(e) => setResendApiKey(e.target.value)}
              className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 font-mono text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
            />
            <p className="mt-1 text-[11px] text-muted">
              Fleetwatch will use this key to generate DKIM records and send emails.
            </p>
          </div>
        )}

        <div className="flex justify-end gap-2 border-t border-line pt-3">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" disabled={saving}>
            {saving ? <span className="mr-1.5 inline-flex"><Spinner size={14} /></span> : null}
            Add Domain
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// ==========================================
// Add Alias Modal
// ==========================================
function AddAliasModal({
  open,
  onClose,
  domains,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  domains: EmailDomain[];
  onCreated: () => void;
}) {
  const { toast } = useStore();
  const [domainId, setDomainId] = useState(domains[0]?.id || "");
  const [localPart, setLocalPart] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [deliveryMode, setDeliveryMode] = useState<"inbox" | "forward" | "both">("both");
  const [forwardTo, setForwardTo] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (domains[0] && !domainId) setDomainId(domains[0].id);
  }, [domains]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!domainId) {
      toast("Please add a domain first", "err");
      return;
    }
    setSaving(true);
    try {
      await api("POST", "/api/emails/aliases", {
        domainId,
        localPart,
        displayName,
        deliveryMode,
        forwardTo,
      });
      toast(`Alias created!`);
      onCreated();
    } catch (err) {
      toast((err as ApiError).message, "err");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Add Email Address Alias">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="text-xs font-semibold text-fg">Domain</label>
          <select
            value={domainId}
            onChange={(e) => setDomainId(e.target.value)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 font-mono text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          >
            {domains.map((d) => (
              <option key={d.id} value={d.id}>
                @{d.domain}
              </option>
            ))}
          </select>
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">Email Address Local-Part</label>
          <div className="mt-1 flex items-center">
            <input
              type="text"
              required
              placeholder="e.g. support, billing, or * (catch-all)"
              value={localPart}
              onChange={(e) => setLocalPart(e.target.value.toLowerCase())}
              className="h-9 flex-1 rounded-l-lg border border-line bg-surface px-3 font-mono text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
            />
            <span className="flex h-9 items-center rounded-r-lg border border-l-0 border-line bg-surface2 px-3 text-xs text-muted">
              @{domains.find((d) => d.id === domainId)?.domain || "domain.com"}
            </span>
          </div>
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">Display Name</label>
          <input
            type="text"
            placeholder="e.g. Support Team"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">Delivery Mode</label>
          <select
            value={deliveryMode}
            onChange={(e) => setDeliveryMode(e.target.value as any)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          >
            <option value="both">Both: Keep in Fleetwatch Inbox & Forward to Gmail</option>
            <option value="inbox">Fleetwatch Inbox Only</option>
            <option value="forward">Forward to Gmail Only</option>
          </select>
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">Forward Destination (Gmail)</label>
          <input
            type="email"
            placeholder="destination@gmail.com"
            value={forwardTo}
            onChange={(e) => setForwardTo(e.target.value)}
            className="mt-1 h-9 w-full rounded-lg border border-line bg-surface px-3 text-xs text-fg focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </div>

        <div className="flex justify-end gap-2 border-t border-line pt-3">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" disabled={saving}>
            {saving ? <span className="mr-1.5 inline-flex"><Spinner size={14} /></span> : null}
            Create Alias
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// ==========================================
// Cloudflare Worker Setup Modal
// ==========================================
function WorkerSetupModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { toast } = useStore();
  const scriptData = useApi<{ script: string; webhookSecret: string; endpoint: string }>(
    "/api/emails/worker-script"
  );
  const [copiedScript, setCopiedScript] = useState(false);
  const [copiedSecret, setCopiedSecret] = useState(false);

  const copyScript = () => {
    if (!scriptData.data?.script) return;
    navigator.clipboard.writeText(scriptData.data.script);
    setCopiedScript(true);
    toast("Worker script copied to clipboard!");
    setTimeout(() => setCopiedScript(false), 2000);
  };

  const copySecret = () => {
    if (!scriptData.data?.webhookSecret) return;
    navigator.clipboard.writeText(scriptData.data.webhookSecret);
    setCopiedSecret(true);
    toast("Webhook secret copied!");
    setTimeout(() => setCopiedSecret(false), 2000);
  };

  return (
    <Modal open={open} onClose={onClose} wide title="Cloudflare Email Routing & Worker Setup">
      <div className="space-y-4 text-xs">
        <p className="text-muted leading-relaxed">
          Cloudflare Email Routing receives all messages for your domain and forwards them to
          Fleetwatch using an Email Worker. Follow these quick steps:
        </p>

        <ol className="list-decimal space-y-2 pl-4 text-fg leading-relaxed">
          <li>
            <strong>Configure DNS:</strong> Click the <strong>1-Click Cloudflare DNS</strong> button
            on your domain card to automatically add the Cloudflare MX and SPF records.
          </li>
          <li>
            <strong>Create an Email Worker in Cloudflare:</strong>
            <p className="text-muted">
              In Cloudflare Dashboard, go to <strong>Compute (Workers)</strong> → <strong>Create Worker</strong>,
              paste the script below, and deploy it.
            </p>
          </li>
          <li>
            <strong>Attach Worker to Routing:</strong>
            <p className="text-muted">
              In your domain's <strong>Email Routing</strong> → <strong>Routing rules</strong>, add a rule:
              Match <em>Catch-all</em> (or custom alias) → Action: <em>Send to a Worker</em> → select your Worker.
            </p>
          </li>
        </ol>

        <div>
          <div className="flex items-center justify-between pb-1.5">
            <span className="font-semibold text-fg">Cloudflare Email Worker Script</span>
            <Button variant="secondary" size="sm" onClick={copyScript}>
              {copiedScript ? <Check size={13} className="mr-1 text-ok" /> : <Copy size={13} className="mr-1" />}
              {copiedScript ? "Copied" : "Copy Script"}
            </Button>
          </div>
          <pre className="max-h-60 overflow-x-auto rounded-xl border border-line bg-surface p-3 font-mono text-[11px] text-fg">
            {scriptData.data?.script || "// Loading script..."}
          </pre>
        </div>

        <div className="flex items-center justify-between rounded-xl bg-surface2/60 p-3">
          <div>
            <div className="font-semibold text-fg">Inbound Webhook Secret</div>
            <div className="font-mono text-muted text-[11px]">
              {scriptData.data?.webhookSecret || "—"}
            </div>
          </div>
          <Button variant="ghost" size="sm" onClick={copySecret}>
            {copiedSecret ? <Check size={14} className="text-ok" /> : <Copy size={14} />}
          </Button>
        </div>

        <div className="flex justify-end border-t border-line pt-3">
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    </Modal>
  );
}
