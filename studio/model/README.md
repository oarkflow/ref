# studio/model — lossless BCL editing

The edit engine behind the visual studio. It changes a `.bcl` file through
structured operations (set a field, add/remove/rename/move a block) and returns
new source text in which **only the targeted statement differs** — comments,
declaration order, `env()` calls, expressions, heredocs and line endings are
preserved. Depends only on `github.com/oarkflow/bcl` and the standard library.

```go
f, _ := model.Open("04_routes.bcl", src)              // parses; source kept byte for byte
f, _ = f.SetField(model.ParsePath("route/web.todos_list/path"), `"/my-todos"`)
f, _ = f.SetField(model.ParsePath("route/web.todos_list/authz/roles"), `["admin"]`)
f, _ = f.AddBlock(nil, "worker", "mailer", "queue \"jobs\"\nintent \"notify\"")
f, _ = f.RenameBlock(model.ParsePath("worker/mailer"), "welcome")
f, _ = f.MoveBlock(model.ParsePath("worker/welcome"), 0)
out := f.Source()

h := model.NewHistory(f)                              // undo/redo
_ = h.Apply(func(f *model.File) (*model.File, error) { return f.RemoveField(p) })
_ = h.Undo()                                          // restores the exact previous bytes
```

## API

| | |
|---|---|
| `Open`, `OpenWith` | parse + verify; format style (tabs / indent width) is detected or given |
| `File.Source/Text/Name/Format` | current source, detected style |
| `File.Blocks`, `Statements(path)`, `Lookup(path)`, `FindBlock` | read-only `Node` views (kind, head, id, byte range, line, raw value, attached comment) |
| `SetField(path, valueSrc)` | replace a field's value, or append the field to its parent block |
| `RemoveField`, `RemoveBlock` | delete with the comment lines attached directly above |
| `AddBlock(parent, type, id, body)` | append a block (nil parent = top level) |
| `RenameBlock(path, newID)` | change a block id, keeping quoted/bare style when it can |
| `MoveBlock(path, index)` | reorder among siblings; comments and blank-line separator travel along |
| `Reformat` | whitespace-only format of the whole file |
| `History` | `Apply`, `Undo`, `Redo`, `CanUndo`, `CanRedo`, `Current` |
| `Quote`, `QuoteList`, `ParsePath`, `Path.String/Child` | helpers |

Values (`valueSrc`, block bodies) are **raw BCL text**, spliced in untouched:
`env("DB", "x")`, `30m`, `[1, 2]`, `count > 1 && ok`, `<<EOT … EOT`. Use `Quote`
for strings coming from a UI.

### Paths

`route / web.todos_list / authz / roles` — each level is the statement name
(block type) followed by its id when it has one. Repeats are indexed on the last
segment of the level: `node/a[1]`, `parameter[2]`. `Node.Path` is always the
canonical, resolvable form. A literal `/` in an id is written `\/`.

## Design

1. **Never print from the AST.** `bcl.FormatDocument`/`Canonicalize` drop comments
   and can reorder declarations. The AST is used for *validation only*.
2. **Edit the text.** The AST gives only start offsets (a block's span ends at its
   `{`), so `scan.go`/`tree.go` scan the source into a statement tree with full
   byte ranges, following the same statement-boundary decisions as bcl's parser
   (single-value assignments, expression lines, `id {` blocks, `name kind "str"`,
   dotted names, `override`/`use`/`when` headers, spreads, opaque `schema` bodies).
3. **Verify before trusting.** `Open` compares the tree with the bcl AST
   (statement count and start offset, recursively). A file the scanner reads
   differently from the parser is refused with a clear error rather than edited.
4. **Splice → `bcl.Format` → reparse → verify → post-check.** Every op computes
   text edits from byte ranges, applies them, runs the token-level `bcl.Format`
   (keeps comments, idempotent), reparses, re-verifies the tree and checks that
   the op did what was asked (statement count, target node, position). Any
   failure returns an error and *no* file; Files are immutable, so the receiver
   is never affected.
5. **Undo is snapshots.** `History` keeps earlier `*File`s, so undo is byte-exact.

## Invariants (all covered by tests)

* `Open` never rewrites the source; the first edit formats the whole file once
  (whitespace only). Edit an already-formatted file and the diff is one statement.
* Comments are never lost by any op; `Remove*` deletes only comments attached to the
  removed statement (contiguous `#`/`//` lines directly above, or trailing on its line).
* A failed op leaves the receiver and the history unchanged.
* Unrelated top-level statements are byte-identical after an edit (formatted baseline).
* `SetField(p, old)` after `SetField(p, new)` reproduces the original text.
* Undo-all after any random op sequence restores the original bytes; redo-all the final ones.
* CRLF files stay CRLF; tab / N-space indentation is detected and kept.

## Known limits

* `schema X { … }` bodies use a separate clause syntax and are opaque: the block can
  be moved/removed/renamed but not edited inside. One of bcl's own language demos
  (`example/features/08-schemas-types-validation.bcl`) is refused for this family of reasons.
* Ids are quoted unless they are simple words: `route a.b {` does not parse back as a
  block (bcl's own AST printer emits it), so dotted ids are always written `"a.b"`.
* `MoveBlock` needs the statement and its target neighbour on their own lines.
* A `SetField` value ending in a line comment is refused when it would swallow the
  closing brace of a one-line block.
* Each edit costs a full format + parse + verify of the file (~10 ms for a 20 KB file).
