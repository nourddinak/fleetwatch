import { ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { setConfirmer } from "../api/client";
import { Button, Field, Input, Modal } from "./ui";

/** Asks for a 2FA code (or the password) when a dangerous action needs one (see api()). */
export function ConfirmPrompt() {
  const [ask, setAsk] = useState<{ kind: "2fa" | "password"; message: string } | null>(null);
  const [value, setValue] = useState("");
  const done = useRef<((v: string | null) => void) | null>(null);
  useEffect(() => {
    setConfirmer((kind, message) => new Promise((resolve) => {
      done.current?.(null); // one at a time
      done.current = resolve;
      setValue("");
      setAsk({ kind, message });
    }));
    return () => setConfirmer(null);
  }, []);
  const finish = (v: string | null) => {
    done.current?.(v);
    done.current = null;
    setAsk(null);
  };
  if (!ask) return null;
  const twofa = ask.kind === "2fa";
  return (
    <Modal open onClose={() => finish(null)} title="Confirm this action"
      footer={<>
        <Button variant="ghost" onClick={() => finish(null)}>Cancel</Button>
        <Button disabled={!value.trim()} onClick={() => finish(value.trim())}><ShieldCheck size={14} /> Confirm</Button>
      </>}>
      <form onSubmit={(e) => (e.preventDefault(), value.trim() && finish(value.trim()))} className="space-y-3">
        <p className="text-sm text-muted">{ask.message} The owner asks for it before dangerous actions.</p>
        <Field label={twofa ? "Code from your authenticator app" : "Your password"}>
          <Input autoFocus type={twofa ? "text" : "password"} inputMode={twofa ? "numeric" : undefined} autoComplete={twofa ? "one-time-code" : "current-password"}
            value={value} onChange={(e) => setValue(e.target.value)} placeholder={twofa ? "123456" : ""} />
        </Field>
      </form>
    </Modal>
  );
}
