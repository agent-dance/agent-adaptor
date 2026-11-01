import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./style.css";

const url = new URL(location.href);
const threadId = url.searchParams.get("thread") || sessionStorage.getItem("agui-thread") || crypto.randomUUID();
sessionStorage.setItem("agui-thread", threadId);
url.searchParams.set("thread", threadId);
history.replaceState(null, "", url);
createRoot(document.getElementById("root")!).render(<App threadId={threadId} />);
