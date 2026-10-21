import { Server as ServerIcon } from "lucide-react";
import { useStore } from "../store/store";
import { Card, EmptyState, Gate, PageHeader, SectionTitle } from "../components/ui";
import { Sites } from "./Sites";
import { ServerDocker } from "./ServerDocker";

/** Docker in one place: apps deployed from GitHub, and each server's containers (however
 * they were started), stacks, images and volumes, with installing or removing Docker. */
export function Docker() {
  const { servers } = useStore();
  return (
    <>
      <PageHeader title="Docker" sub="Apps you deploy from GitHub, and every container on your servers with its ports, however it was started. Start, stop, restart, update, see logs, delete; install or remove Docker itself." />
      <Gate>
        <div className="space-y-6">
          <Card><Sites docker /></Card>
          {servers.length === 0 && <EmptyState icon={<ServerIcon size={20} />} title="No servers yet" desc="Add a server first." />}
          {servers.map((s) => (
            <Card key={s.id}>
              <SectionTitle icon={<ServerIcon size={15} />} title={s.name} sub="Containers, compose stacks, images and volumes on this server." />
              <ServerDocker serverId={s.id} />
            </Card>
          ))}
        </div>
      </Gate>
    </>
  );
}
