import { useState } from "react";
import { useStore } from "../store/store";
import { Boxes, Server as ServerIcon } from "lucide-react";
import { Gate, PageHeader, WithIcon } from "../components/ui";
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
          <WithIcon icon={<ServerIcon size={15} />}><ServerPicker value={server} onChange={(v) => { setServer(v); setApp("all"); }} className="pl-10" /></WithIcon>
          <WithIcon icon={<Boxes size={15} />}><ApplicationPicker value={app} onChange={setApp} serverId={server} className="pl-10" /></WithIcon>
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
