import { useParams } from "react-router-dom";
import { FileTextView } from "../components/FileTextView";
import { PageHeader } from "../ui/primitives";

/** Developer view only: the raw source of one file. */
export function FilesView() {
  const { file = "" } = useParams();
  const name = decodeURIComponent(file);
  return (
    <div className="view">
      <PageHeader icon="file-text" title={name} subtitle="Raw source. Most changes are easier in the friendly editor, but this shows exactly what’s stored." />
      <FileTextView file={name} />
    </div>
  );
}
