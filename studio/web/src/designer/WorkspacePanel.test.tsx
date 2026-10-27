import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { BlockNode } from "../api/types";
import { StudioApi } from "../api/client";
import { StudioProvider } from "../state/context";
import { createStudioStore } from "../state/store";
import { WorkspacePanel } from "./WorkspacePanel";

const f = (name: string, raw: string): BlockNode => ({ kind: "field", name, raw, path: "x/" + name, line: 0, start: 0, end: 0 });
const blk = (type: string, id: string | undefined, path: string, children: BlockNode[]): BlockNode => ({ kind: "block", type, id, path, children, line: 0, start: 0, end: 0 });
const cases = blk("resource", "cases", "resource/cases", [f("kind", '"pipeline.cases"'), blk("config", undefined, "resource/cases/config", [f("pipelines", '["p"]')])]);
const db = blk("resource", "database", "resource/database", [f("kind", '"database.sql"')]);

function setup() {
  const apply = vi.fn();
  render(
    <StudioProvider store={createStudioStore(new StudioApi({ onUnauthorized: () => {} }))}>
      <WorkspacePanel workspaces={[]} sources={[]} trees={{ "r.bcl": [cases, db] }} pipelineName="p" pipelineFile="r.bcl" forms={[]} canEdit apply={apply} />
    </StudioProvider>,
  );
  return apply;
}
const type = (label: RegExp | string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe("adding lookup sets", () => {
  it("creates a source with an API connection, allowing a private address only when asked", () => {
    const apply = setup();
    type("Name", "payers_api");
    fireEvent.change(screen.getByLabelText("Source"), { target: { value: "http" } });
    fireEvent.change(screen.getByPlaceholderText("payer_api"), { target: { value: "payer_api" } });
    fireEvent.change(screen.getByPlaceholderText("https://api.example.com/v1"), { target: { value: "http://127.0.0.1:8765" } });
    fireEvent.click(screen.getByLabelText(/Allow a private or local address/));
    fireEvent.change(screen.getByPlaceholderText("/payers"), { target: { value: "/payers" } });
    fireEvent.click(screen.getByText("Add set"));
    const ops = apply.mock.calls[0]![0] as { op: string; body?: string; type?: string }[];
    const service = ops.find((o) => o.type === "resource" && o.body?.includes("service.http"));
    expect(service?.body).toContain("allow_private_networks true");
    expect(service?.body).toContain('allowed_hosts ["127.0.0.1"]');
    const source = ops.find((o) => o.body?.includes("lookup.sources"));
    expect(source?.body).toContain('service "payer_api"');
  });

  it("does not allow a private address by default", () => {
    const apply = setup();
    type("Name", "payers_api");
    fireEvent.change(screen.getByLabelText("Source"), { target: { value: "http" } });
    fireEvent.change(screen.getByPlaceholderText("payer_api"), { target: { value: "payer_api" } });
    fireEvent.change(screen.getByPlaceholderText("https://api.example.com/v1"), { target: { value: "https://api.example.com/v1" } });
    fireEvent.change(screen.getByPlaceholderText("/payers"), { target: { value: "/payers" } });
    fireEvent.click(screen.getByText("Add set"));
    const ops = apply.mock.calls[0]![0] as { body?: string }[];
    expect(ops.find((o) => o.body?.includes("service.http"))?.body).not.toContain("allow_private_networks");
  });

  it("connects an existing database for a query set", () => {
    const apply = setup();
    type("Name", "facilities");
    fireEvent.change(screen.getByLabelText("Source"), { target: { value: "sql" } });
    fireEvent.change(screen.getByPlaceholderText(/SELECT code AS value/), { target: { value: "SELECT id AS value, name AS label FROM f" } });
    fireEvent.click(screen.getByText("Add set"));
    const ops = apply.mock.calls[0]![0] as { body?: string }[];
    expect(ops[0]!.body).toContain('database "database"');
    expect(ops[0]!.body).toContain('type "sql"');
  });

  it("tests a query set against the running app and shows the real error", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ error: { message: "SQL logic error: no such column: id (1)" } }), { status: 422 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ options: [{ value: "a", label: "Alpha" }], total: 1, duration_ms: 3 }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    try {
      setup();
      type("Name", "facilities");
      fireEvent.change(screen.getByLabelText("Source"), { target: { value: "sql" } });
      expect(screen.queryByText("Test this set")).toBeNull(); // nothing to test until the query is written
      fireEvent.change(screen.getByPlaceholderText(/SELECT code AS value/), { target: { value: "SELECT id AS value FROM f WHERE a = ?" } });
      fireEvent.change(screen.getByPlaceholderText("mdm_level"), { target: { value: "mdm" } });
      fireEvent.change(screen.getByLabelText("mdm"), { target: { value: "High" } });
      fireEvent.click(screen.getByText("Test this set"));
      expect((await screen.findAllByRole("alert"))[0]).toHaveTextContent("no such column: id");
      const body = JSON.parse(String((fetchMock.mock.calls[0]![1] as RequestInit).body));
      expect(body).toMatchObject({ database: "database", deps: { mdm: "High" }, set: { type: "sql", args: ["mdm"] } });

      fireEvent.click(screen.getByText("Test this set"));
      await waitFor(() => expect(screen.getByText("Alpha")).toBeInTheDocument());
      expect(screen.getByText(/1 choice in 3 ms/)).toBeInTheDocument();
      expect(screen.queryByRole("alert")).toBeNull();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("warns after a failed test and offers to add anyway; a later success clears it", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ error: { message: "no such column: id" } }), { status: 422 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ options: [], total: 0, duration_ms: 1 }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    try {
      const apply = setup();
      type("Name", "sites");
      fireEvent.change(screen.getByLabelText("Source"), { target: { value: "sql" } });
      fireEvent.change(screen.getByPlaceholderText(/SELECT code AS value/), { target: { value: "SELECT id AS value FROM f" } });
      expect(screen.getByText(/Not tested yet/)).toBeInTheDocument();
      fireEvent.click(screen.getByText("Test this set"));
      await screen.findByText(/last test of this set failed/);
      expect(screen.getByRole("button", { name: "Add anyway" })).toBeInTheDocument();
      // editing the query makes the old result stale
      fireEvent.change(screen.getByPlaceholderText(/SELECT code AS value/), { target: { value: "SELECT code AS value FROM f" } });
      expect(screen.queryByText(/last test of this set failed/)).toBeNull();
      expect(screen.getByText(/Not tested yet/)).toBeInTheDocument();
      fireEvent.click(screen.getByText("Test this set"));
      await waitFor(() => expect(screen.getByRole("button", { name: "Add set" })).toBeInTheDocument());
      fireEvent.click(screen.getByRole("button", { name: "Add set" }));
      expect(apply).toHaveBeenCalledTimes(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
