import { describe, expect, it } from "vitest";
import {
  assetStatus, buttonSnippet, editorUrl, formSnippet, friendlyName, includeSnippet, linkSnippet, newStaticPath, newTemplateName,
  starterSource, statusOf, templateName, templatePath, urlForTemplate, variableSnippet,
} from "./lib";

describe("names and paths", () => {
  it("maps between a template name and its file", () => {
    expect(templatePath("pages/todos/list")).toBe("templates/pages/todos/list.html");
    expect(templateName("templates/pages/todos/list.html")).toBe("pages/todos/list");
    expect(editorUrl("templates/pages/a b.html")).toBe("/pages/edit?path=templates%2Fpages%2Fa%20b.html");
  });

  it("turns a plain name into a safe template name in the right folder", () => {
    expect(newTemplateName("page", "About us")).toBe("pages/about-us");
    expect(newTemplateName("layout", "Wide")).toBe("layouts/wide");
    expect(newTemplateName("component", "components/Pricing Table")).toBe("components/pricing-table");
    expect(newTemplateName("page", "  ")).toBeNull();
    // Traversal cannot survive: dots are dropped and every segment is tidied.
    expect(newTemplateName("page", "../secret")).toBe("pages/secret");
    expect(newTemplateName("page", "a/../b")).toBe("pages/a/b");
    expect(newTemplateName("page", "..")).toBeNull();
    expect(newTemplateName("page", "../../etc/passwd")).not.toMatch(/\.\./);
  });

  it("validates static file paths", () => {
    expect(newStaticPath("css/site.css")).toBe("static/css/site.css");
    expect(newStaticPath("/static/js/app.js")).toBe("static/js/app.js");
    expect(newStaticPath("css/../x.css")).toBeNull();
    expect(newStaticPath(".hidden/x.css")).toBeNull();
    expect(newStaticPath("script.exe")).toBeNull();
    expect(newStaticPath("")).toBeNull();
  });

  it("gives designs friendly titles", () => {
    expect(friendlyName("pages/todos/list").title).toBe("Todos list");
    expect(friendlyName("pages/auth/login").title).toBe("Auth login");
    expect(friendlyName("components/navbar").title).toBe("Navbar");
    expect(friendlyName("layouts/base").title).toBe("Base");
  });
});

describe("status", () => {
  it("reads original, customized and new from where the file comes from", () => {
    expect(statusOf({ source: "disk" })).toBe("original");
    expect(statusOf({ source: "override" })).toBe("customized");
    expect(statusOf({ source: "draft" })).toBe("new");
    expect(assetStatus({ overridesDisk: true })).toBe("customized");
    expect(assetStatus({ overridesDisk: false })).toBe("new");
  });
});

describe("snippets", () => {
  it("writes an include the engine accepts", () => {
    expect(includeSnippet("components/alert")).toBe('@include("components/alert.html")');
    expect(includeSnippet("components/alert.html")).toBe('@include("components/alert.html")');
  });
  it("writes a variable", () => expect(variableSnippet("user.email")).toBe("${user.email}"));
  it("escapes button text", () => expect(buttonSnippet('Save <b> & "go"')).toContain("Save &lt;b&gt; &amp; &quot;go&quot;"));

  it("turns route parameters into template expressions", () => {
    expect(urlForTemplate("/todos/{id}/edit")).toBe("/todos/${id}/edit");
    expect(urlForTemplate("/todos/:id")).toBe("/todos/${id}");
    expect(urlForTemplate("/todos")).toBe("/todos");
  });

  it("links to a page", () => {
    expect(linkSnippet("/todos/new", "New todo")).toBe('<a href="/todos/new" class="btn">New todo</a>');
  });

  it("builds a form that follows the app's redirect convention", () => {
    const f = formSnippet("POST", "/todos", { redirect: "/todos", submit: "Add", fields: ["title", "assignee_email"] });
    expect(f).toContain('<form method="POST" action="/todos?redirect=/todos">');
    expect(f).toContain('name="title"');
    expect(f).toContain('name="assignee_email"');
    expect(f).toContain(">Add</button>");
    expect(f.startsWith("<form") && f.trim().endsWith("</form>")).toBe(true);
  });

  it("posts even for PUT/PATCH/DELETE routes, since HTML forms only send GET and POST", () => {
    expect(formSnippet("DELETE", "/todos/{id}")).toContain('method="POST" action="/todos/${id}"');
  });

  it("never lets a field name break out of its attribute", () => {
    const f = formSnippet("POST", "/x", { fields: ['a"><script>'] });
    expect(f).not.toContain("<script>");
  });

  it("starts each kind of design from something that works", () => {
    expect(starterSource("page", "pages/about-us")).toMatch(/^@extends\("layouts\/base\.html"\)/);
    expect(starterSource("page", "pages/about-us")).toContain('@define("content")');
    expect(starterSource("layout", "layouts/wide")).toContain('@block("content")');
    expect(starterSource("component", "components/chip")).toContain("${label}");
  });
});
