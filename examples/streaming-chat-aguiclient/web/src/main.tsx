import { createRoot } from "react-dom/client";
import App from "./App";
import "./index.css";

// NOTE: StrictMode is intentionally disabled for this example. React
// StrictMode double-invokes component bodies and effects in dev, which in
// this streaming chat context leads to two concurrent RxJS subscriptions
// on the same HttpAgent run — each independently appending token deltas
// into the same message, producing visibly scrambled output. Dropping
// StrictMode keeps the demo faithful to production behaviour. Keeping
// token accumulation in refs (see App.tsx) is the other half of the fix.
createRoot(document.getElementById("root")!).render(<App />);
