import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { TemplateExtras } from "./TemplateExtras";
import { mount, pagesApi, tpl, layout, component } from "./testKit";

const ui = (props: Partial<Parameters<typeof TemplateExtras>[0]> = {}) => (
  <Routes>
    <Route path="*" element={<TemplateExtras field="template" raw='"pages/todos/list"' path="route/web.todos_list/template" onPick={vi.fn()} {...props} />} />
  </Routes>
);

describe("the route form's template picker", () => {
  it("shows what the value points at and links to it", async () => {
    await mount(ui());
    expect(await screen.findByText("Page")).toBeInTheDocument();
    expect(screen.getByText("Original")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Open in Page designs/ })).toBeInTheDocument();
  });

  it("says when the name is not one of the designs", async () => {
    await mount(ui({ raw: '"pages/nope"' }));
    expect(await screen.findByText(/“pages\/nope” isn’t one of your page designs/)).toBeInTheDocument();
  });

  it("offers to create a design that does not exist yet, with its name filled in", async () => {
    const api = pagesApi();
    await mount(ui({ raw: '"pages/about"' }), { api });
    await userEvent.click(await screen.findByRole("button", { name: /Create “pages\/about”/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("textbox")).toHaveValue("about");
    await userEvent.click(within(dialog).getByRole("button", { name: /Create design/ }));
    await waitFor(() => expect(api.putAsset).toHaveBeenCalled());
    expect(vi.mocked(api.putAsset).mock.calls[0]![1]).toBe("templates/pages/about.html");
  });

  it("lists only pages for a template and only layouts for a layout", async () => {
    const api = pagesApi();
    const { unmount } = await mount(ui(), { api });
    await userEvent.click(await screen.findByRole("button", { name: /Choose a page design/ }));
    const names = within(await screen.findByRole("listbox")).getAllByRole("option").map((o) => o.textContent);
    expect(names.some((n) => n?.includes("Todos list"))).toBe(true);
    expect(names.some((n) => n?.includes("Base"))).toBe(false);
    unmount();
    await mount(ui({ field: "layout", raw: '"layouts/base"' }), { api: pagesApi({}, [tpl(), layout(), component()]) });
    await userEvent.click(await screen.findByRole("button", { name: /Choose a layout/ }));
    const layouts = within(await screen.findByRole("listbox")).getAllByRole("option").map((o) => o.textContent);
    expect(layouts.every((n) => n?.includes("Base"))).toBe(true);
  });

  it("hands back a quoted name when one is picked", async () => {
    const onPick = vi.fn();
    await mount(ui({ onPick }));
    await userEvent.click(await screen.findByRole("button", { name: /Choose a page design/ }));
    await userEvent.click(await screen.findByRole("option", { name: /Todos list/ }));
    expect(onPick).toHaveBeenCalledWith('"pages/todos/list"');
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("does not offer to change a read-only field", async () => {
    await mount(ui({ readOnly: true }));
    await screen.findByText("Page");
    expect(screen.queryByRole("button", { name: /Choose/ })).toBeNull();
  });
});

describe("outside the app", () => {
  it("renders nothing, so the form can be used on its own", () => {
    const { container } = render(<TemplateExtras field="template" raw='"pages/x"' path="route/x/template" onPick={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });
});
