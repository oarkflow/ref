import { Navigate } from "react-router-dom";
import { FileTextView } from "../components/FileTextView";
import { Inspector } from "../forms/Inspector";
import { useStudio } from "../state/context";
import { useUi } from "../state/ui";
import { useState } from "react";

/** The friendly form for the selected item; Developer view adds the raw source tab. */
export function EditorView() {
  const selection = useStudio((s) => s.selection);
  const draft = useStudio((s) => s.draft);
  const dev = useUi((s) => s.devView);
  const [tab, setTab] = useState<"form" | "source">("form");
  const sourceOnly = selection?.path === "";

  if (!draft || !selection) return <Navigate to="/overview" replace />;

  return (
    <div className="view editor">
      {dev && !sourceOnly && (
        <div className="tab-bar editor-tabs" role="tablist" aria-label="Editor view">
          <button type="button" role="tab" aria-selected={tab === "form"} className={tab === "form" ? "on" : ""} onClick={() => setTab("form")}>Form</button>
          <button type="button" role="tab" aria-selected={tab === "source"} className={tab === "source" ? "on" : ""} onClick={() => setTab("source")}>Source</button>
        </div>
      )}
      {sourceOnly || (dev && tab === "source") ? <FileTextView file={selection.file} /> : <Inspector />}
    </div>
  );
}
