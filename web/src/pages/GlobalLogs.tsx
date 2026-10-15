import { useState } from "react";
import { useStore } from "../store/store";
import { Gate, PageHeader } from "../components/ui";
import { ApplicationPicker, ServerPicker } from "../components/domain";
import { LogViewer } from "../components/LogViewer";

export function GlobalLogs() {
  const { servers } = useStore();
  const [server, setServer] = useState("all");
  const [app, setApp] = useState("all");
  return (
    <>
      <PageHeader title="Logs" sub="Search and tail logs across every server and application" />
      <Gate>
        <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:w-2/3">
          <ServerPicker value={server} onChange={(v) => { setServer(v); setApp("all"); }} />
          <ApplicationPicker value={app} onChange={setApp} serverId={server} />
        </div>
        <LogViewer
          key={server + app}
          appId={app === "all" ? undefined : app}
          serverId={server === "all" ? undefined : server}
          showApp={app === "all"}
          canStream={servers.length > 0}
        />
      </Gate>
    </>
  );
}
