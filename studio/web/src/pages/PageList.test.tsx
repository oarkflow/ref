import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { PageList } from "./PageList";
import { catalogOf, component, layout, mount, pagesApi, tpl } from "./testKit";

function Where() {
  const l = useLocation();
  return <output aria-label="location">{l.pathname + l.search}</output>;
}
const ui = () => (
  <>
    <Routes>
      <Route path="/pages" element={<PageList />} />
      <Route path="*" element={<div />} />
    </Routes>
    <Where />
  </>
);
const rows = () => within(screen.getAllByRole("rowgroup")[1]!).getAllByRole("row");
const names = () => rows().map((r) => within(r).getAllByRole("cell")[0]!.textContent);

describe("Page designs list", () => {
  it("lists pages first, then layouts, then components", async () => {
    await mount(ui());
    await screen.findByText("Todos list");
    const order = names();
    expect(order[0]).toContain("Todos list");
    expect(order[1]).toContain("Base");
    expect(order[2]).toContain("Alert");
    expect(order[3]).toContain("Row");
  });

  it("shows what each design is, whether it was changed, and where it is used", async () => {
    await mount(ui());
    const row = (await screen.findByText("Todos list")).closest("tr")!;
    expect(within(row).getByText("Page")).toBeInTheDocument();
    expect(within(row).getByText("Original")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /GET \/todos/ })).toBeInTheDocument();
    const alert = screen.getByText("Alert").closest("tr")!;
    expect(within(alert).getByText("Included by other designs")).toBeInTheDocument();
    const base = screen.getByText("Base").closest("tr")!;
    expect(within(base).getByText("Layout for 1 page")).toBeInTheDocument();
  });

  it("marks a customized design and a new one", async () => {
    const api = pagesApi({}, [tpl({ source: "override" }), component("components/new-bit", { source: "draft" })]);
    await mount(ui(), { api });
    expect((await screen.findByText("Todos list")).closest("tr")).toHaveTextContent("Customized");
    expect(screen.getByText("New bit").closest("tr")).toHaveTextContent("New");
  });

  it("hints when nothing uses a design, and counts problems", async () => {
    const api = pagesApi({}, [tpl({ routes: [], unused: true, diagnostics: [{ severity: "error", message: "bad" }], missing: ["components/gone"] })]);
    await mount(ui(), { api });
    const row = (await screen.findByText("Todos list")).closest("tr")!;
    expect(within(row).getByText("Not used")).toBeInTheDocument();
    expect(within(row).getByText("2")).toBeInTheDocument();
  });

  it("filters by type and by search", async () => {
    await mount(ui());
    await screen.findByText("Todos list");
    await userEvent.click(screen.getByRole("button", { name: "Components" }));
    expect(names().every((n) => /Alert|Row/.test(n ?? ""))).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "All" }));
    await userEvent.type(screen.getByRole("searchbox"), "alert");
    expect(names()).toHaveLength(1);
    expect(names()[0]).toContain("Alert");
    await userEvent.clear(screen.getByRole("searchbox"));
    await userEvent.type(screen.getByRole("searchbox"), "/todos"); // routes are searchable too
    expect(names().some((n) => n?.includes("Todos list"))).toBe(true);
    expect(names().some((n) => n?.includes("Alert"))).toBe(false);
  });

  it("says so when nothing matches", async () => {
    await mount(ui());
    await screen.findByText("Todos list");
    await userEvent.type(screen.getByRole("searchbox"), "zzzz");
    expect(screen.getByText("Nothing matches")).toBeInTheDocument();
  });

  it("opens a design in the editor when its row is clicked", async () => {
    await mount(ui());
    await userEvent.click((await screen.findByText("Todos list")).closest("tr")!);
    expect(screen.getByLabelText("location")).toHaveTextContent("/pages/edit?path=templates%2Fpages%2Ftodos%2Flist.html");
  });

  it("jumps to the route from a used-by chip without opening the design", async () => {
    await mount(ui());
    await userEvent.click(await screen.findByRole("button", { name: /GET \/todos/ }));
    expect(screen.getByLabelText("location")).toHaveTextContent("/edit");
  });

  it("warns about pages that point at a design that does not exist, and offers to create it", async () => {
    const api = pagesApi();
    api.templates = vi.fn(async () => catalogOf([tpl(), layout(), component()], { missing: [{ route: "web.about", template: "pages/about" }] })) as never;
    await mount(ui(), { api });
    const note = (await screen.findByText(/points at a design that doesn’t exist/)).closest(".callout") as HTMLElement;
    expect(note).toHaveTextContent("web.about");
    await userEvent.click(within(note).getByRole("button", { name: "Create it" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("textbox")).toHaveValue("about");
    expect(within(dialog).getByText("pages/about")).toBeInTheDocument();
  });

  it("creates a new design and opens it", async () => {
    const api = pagesApi();
    await mount(ui(), { api });
    await userEvent.click(await screen.findByRole("button", { name: /New design/ }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByRole("textbox"), "About us");
    expect(within(dialog).getByText("pages/about-us")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: /Create design/ }));
    await waitFor(() => expect(api.putAsset).toHaveBeenCalled());
    const [, path, content] = vi.mocked(api.putAsset).mock.calls[0]!;
    expect(path).toBe("templates/pages/about-us.html");
    expect(content).toContain('@extends("layouts/base.html")');
    await waitFor(() => expect(screen.getByLabelText("location")).toHaveTextContent("about-us.html"));
  });

  it("will not create a design that already exists", async () => {
    await mount(ui());
    await userEvent.click(await screen.findByRole("button", { name: /New design/ }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("radio", { name: /Component/ }));
    await userEvent.type(within(dialog).getByRole("textbox"), "row");
    expect(within(dialog).getByRole("alert")).toHaveTextContent("already exists");
    expect(within(dialog).getByRole("button", { name: /Create design/ })).toBeDisabled();
  });

  it("is closed to viewers", async () => {
    await mount(ui(), { roles: ["viewer"] });
    expect(await screen.findByText("Page designs are for editors")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New design/ })).toBeNull();
  });

  it("lists the static files the draft carries on the second tab", async () => {
    const api = pagesApi();
    api.listAssets = vi.fn(async () => ({ version: 1, assets: [{ path: "static/css/app.css", size: 2048, kind: "static", status: "modified", overridesDisk: true }, { path: "static/js/new.js", size: 12, kind: "static", status: "added", overridesDisk: false }] })) as never;
    await mount(ui(), { api });
    await userEvent.click(await screen.findByRole("tab", { name: /Static files/ }));
    const row = screen.getByText("css/app.css").closest("tr")!;
    expect(row).toHaveTextContent("Customized");
    expect(row).toHaveTextContent("2.0 KB");
    expect(screen.getByText("js/new.js").closest("tr")).toHaveTextContent("New");
    expect(screen.getByRole("button", { name: "Customize an existing file" })).toBeInTheDocument();
  });
});
