import { useEffect, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { AlertTriangle, ArrowLeft, Eye, EyeOff, KeyRound, LockKeyhole, LogIn, ShieldCheck, UserRound } from "lucide-react";
import { useStore } from "../store/store";
import { Button, Field, Github, Input, Spinner, WithIcon } from "../components/ui";
import { Logo, ThemeToggle } from "../components/Layout";

export function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="relative flex min-h-screen items-center justify-center bg-bg p-4">
      <div className="absolute right-4 top-4">
        <ThemeToggle />
      </div>
      <div className="fade-in w-full max-w-md">
        <Logo className="mb-8 justify-center" />
        <div className="rounded-[2rem] border border-line bg-surface p-8">{children}</div>
      </div>
    </div>
  );
}

export function Login() {
  const { auth, login } = useStore();
  const nav = useNavigate();
  const [u, setU] = useState("");
  const [p, setP] = useState("");
  const [show, setShow] = useState(false);
  const [params] = useSearchParams();
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [github, setGithub] = useState(false);
  useEffect(() => {
    const e = params.get("error");
    if (e) setErr(e.charAt(0).toUpperCase() + e.slice(1));
  }, [params]);
  useEffect(() => {
    fetch("/api/auth/methods").then((r) => (r.ok ? r.json() : {})).then((m: { github?: boolean }) => setGithub(!!m.github)).catch(() => {});
  }, []);
  if (auth === "in") return <Navigate to="/" replace />;
  if (auth === "needs2fa") return <Navigate to="/2fa" replace />;
  if (auth === "setup") return <Navigate to="/setup" replace />;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    setBusy(true);
    const r = await login(u, p);
    setBusy(false);
    if (!r.ok) {
      setErr(r.error ?? "Login failed");
      setP("");
    } else nav("/");
  };

  return (
    <Shell>
      <div className="mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-surface2"><LogIn size={20} /></div>
      <h1 className="text-2xl font-semibold tracking-tight">Welcome back</h1>
      <p className="mt-1 text-sm text-muted">Sign in to monitor and control your VPS fleet.</p>
      <form onSubmit={submit} className="mt-6 space-y-4">
        <Field label="Username">
          <WithIcon icon={<UserRound size={15} />}><Input value={u} onChange={(e) => setU(e.target.value)} autoComplete="username" autoFocus className="pl-10" /></WithIcon>
        </Field>
        <Field label="Password">
          <div className="relative">
            <LockKeyhole size={15} className="pointer-events-none absolute left-4 top-1/2 z-10 -translate-y-1/2 text-muted" />
            <Input type={show ? "text" : "password"} value={p} onChange={(e) => setP(e.target.value)} autoComplete="current-password" className="pl-10 pr-11" />
            <button type="button" onClick={() => setShow(!show)} className="absolute right-4 top-1/2 -translate-y-1/2 text-muted hover:text-fg" aria-label="Toggle password visibility">
              {show ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
        </Field>
        {err && <div role="alert" className="flex items-center gap-2 rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit"><AlertTriangle size={15} className="shrink-0" />{err}</div>}
        <Button type="submit" className="w-full" disabled={busy}>
          {busy ? <Spinner /> : <LogIn size={15} />} Sign in
        </Button>
      </form>
      {github && (
        <>
          <div className="my-5 flex items-center gap-3 text-xs text-muted"><span className="h-px flex-1 bg-line" />or<span className="h-px flex-1 bg-line" /></div>
          <a href="/api/auth/github" className="flex w-full items-center justify-center gap-2 rounded-full border border-line px-4 py-2.5 text-sm font-medium transition hover:bg-surface2">
            <Github size={16} /> Continue with GitHub
          </a>
          <p className="mt-2 text-center text-[11px] text-muted">For anyone who linked their GitHub account on the Sites page.</p>
        </>
      )}
    </Shell>
  );
}

export function TwoFA() {
  const { auth, verify2fa, backToLogin } = useStore();
  const nav = useNavigate();
  const [recovery, setRecovery] = useState(false);
  const [code, setCode] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  if (auth === "in") return <Navigate to="/" replace />;
  if (auth === "out") return <Navigate to="/login" replace />;

  const verify = async (value: string) => {
    if (busy) return;
    setErr("");
    setBusy(true);
    const r = await verify2fa(value, recovery);
    setBusy(false);
    if (!r.ok) {
      setErr(r.error ?? "Invalid code");
      setCode("");
    } else nav("/");
  };
  const onCode = (v: string) => {
    if (recovery) return setCode(v);
    const digits = v.replace(/\D/g, "").slice(0, 6);
    setCode(digits);
    if (digits.length === 6) verify(digits);
  };
  const ready = recovery ? code.trim().length >= 10 : code.length === 6;

  return (
    <Shell>
      <div className="mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-surface2">{recovery ? <KeyRound size={20} /> : <ShieldCheck size={20} />}</div>
      <h1 className="text-2xl font-semibold tracking-tight">{recovery ? "Use a recovery code" : "Two-factor authentication"}</h1>
      <p className="mt-1 text-sm text-muted">{recovery ? "Enter one of your single-use recovery codes." : "Enter the 6-digit code from your authenticator app."}</p>
      <form onSubmit={(e) => { e.preventDefault(); verify(code); }} className="mt-6 space-y-4">
        <Input
          key={recovery ? "rc" : "otp"}
          value={code}
          onChange={(e) => onCode(e.target.value)}
          placeholder={recovery ? "xxxxx-xxxxx" : "123456"}
          inputMode={recovery ? "text" : "numeric"}
          autoComplete="one-time-code"
          aria-label={recovery ? "Recovery code" : "Authentication code"}
          className="h-14 text-center font-mono text-xl tracking-[0.4em]"
          autoFocus
        />
        <Button type="submit" className="w-full" disabled={busy || !ready}>
          {busy && <Spinner />} Verify
        </Button>
      </form>
      {err && <div role="alert" className="mt-4 rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
      <div className="mt-6 flex items-center justify-between text-sm">
        <button className="inline-flex items-center gap-1.5 text-muted hover:text-fg" onClick={backToLogin}>
          <ArrowLeft size={14} /> Back to login
        </button>
        <button className="text-muted underline-offset-2 hover:text-fg hover:underline" onClick={() => { setRecovery(!recovery); setCode(""); setErr(""); }}>
          {recovery ? "Use authenticator code" : "Use recovery code"}
        </button>
      </div>
    </Shell>
  );
}
