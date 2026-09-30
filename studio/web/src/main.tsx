import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { StudioApi } from "./api/client";
import { StudioProvider } from "./state/context";
import { createStudioStore } from "./state/store";
import "./styles.css";

const store = createStudioStore(
  new StudioApi({
    onUnauthorized: () => store.setState({ meta: null, authError: "Your token was rejected. Sign in again." }),
  }),
);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <StudioProvider store={store}>
      <App />
    </StudioProvider>
  </StrictMode>,
);
