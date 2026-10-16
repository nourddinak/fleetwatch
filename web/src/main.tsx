import { StrictMode, lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App";

// The public status page shares this build but none of the dashboard's screens.
const StatusPage = lazy(() => import("./pages/StatusPage"));
const root = document.getElementById("root")!;

createRoot(root).render(
  <StrictMode>
    {root.dataset.page === "status" ? (
      <Suspense>
        <StatusPage />
      </Suspense>
    ) : (
      <App />
    )}
  </StrictMode>
);
