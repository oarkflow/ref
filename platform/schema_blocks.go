package platform

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Field kinds reported by BlockSchemas.
const (
	KindString   = "string"
	KindIdent    = "ident" // a bare identifier, e.g. an HTTP method
	KindInt      = "int"
	KindNumber   = "number"
	KindBool     = "bool"
	KindDuration = "duration"
	KindList     = "list"   // Items names the element kind
	KindMap      = "map"    // free-form key/value; Items names the value kind
	KindBlock    = "block"  // one nested block (Block holds its schema)
	KindBlocks   = "blocks" // a repeatable nested block
	KindAny      = "any"
)

// FieldSchema describes one key or nested block a block accepts.
type FieldSchema struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Items is the element kind of a list, or the value kind of a map.
	Items string `json:"items,omitempty"`
	// Optional marks a pointer field, whose absence differs from its zero value.
	Optional bool   `json:"optional,omitempty"`
	Doc      string `json:"doc,omitempty"`
	// Block is the nested block's schema for kind block/blocks. A block that
	// contains itself is cut off with Recursive set and Block.Fields empty.
	Block     *BlockSchema `json:"block,omitempty"`
	Recursive bool         `json:"recursive,omitempty"`
}

// BlockSchema describes one BCL block, generated from the Go spec struct that
// decodes it. It is the source of truth for schema-driven editors.
type BlockSchema struct {
	// Name is the BCL block keyword ("route"); empty for a nested block whose
	// keyword is its field name.
	Name string `json:"name,omitempty"`
	// GoType is the spec struct's name, e.g. "RouteSpec".
	GoType string `json:"go_type"`
	// HasID reports that the block is written `keyword "id" { ... }`.
	HasID  bool          `json:"has_id,omitempty"`
	Doc    string        `json:"doc,omitempty"`
	Fields []FieldSchema `json:"fields"`
}

// Field returns the named field.
func (b BlockSchema) Field(name string) (FieldSchema, bool) {
	for _, f := range b.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return FieldSchema{}, false
}

var (
	blockSchemaOnce sync.Once
	blockSchemaMemo map[string]BlockSchema
)

// BlockSchemas returns the schema of every top-level Document block, keyed by
// BCL keyword ("route", "resource", "intent", ...). It is derived by
// reflection over Document and the spec structs' `bcl:"..."` tags, so it
// cannot drift from what the decoder accepts. Doc strings are empty unless
// AttachBlockDocs has been called. The result must not be modified.
func BlockSchemas() map[string]BlockSchema {
	blockSchemaOnce.Do(func() {
		blockSchemaMemo = map[string]BlockSchema{}
		dt := reflect.TypeOf(Document{})
		for i := 0; i < dt.NumField(); i++ {
			name, opts, ok := parseBCLTag(dt.Field(i))
			if !ok || !opts["block"] {
				continue
			}
			t := dt.Field(i).Type
			if t.Kind() == reflect.Slice {
				t = t.Elem()
			}
			s := buildBlockSchema(t, map[reflect.Type]bool{})
			s.Name = name
			blockSchemaMemo[name] = s
		}
	})
	return blockSchemaMemo
}

// BlockSchemasJSON is BlockSchemas as indented JSON, with keys sorted.
func BlockSchemasJSON() ([]byte, error) {
	return json.MarshalIndent(BlockSchemas(), "", "  ")
}

func parseBCLTag(f reflect.StructField) (name string, opts map[string]bool, ok bool) {
	if !f.IsExported() {
		return "", nil, false
	}
	tag, has := f.Tag.Lookup("bcl")
	if !has || tag == "-" {
		return "", nil, false
	}
	parts := strings.Split(tag, ",")
	opts = map[string]bool{}
	for _, o := range parts[1:] {
		opts[o] = true
	}
	return parts[0], opts, true
}

func buildBlockSchema(t reflect.Type, seen map[reflect.Type]bool) BlockSchema {
	bs := BlockSchema{GoType: t.Name(), Fields: []FieldSchema{}}
	seen[t] = true
	defer delete(seen, t)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, ok := parseBCLTag(f)
		if !ok {
			continue
		}
		if opts["id"] {
			bs.HasID = true
			continue
		}
		fs := FieldSchema{Name: name}
		ft := f.Type
		if ft.Kind() == reflect.Ptr {
			fs.Optional = true
			ft = ft.Elem()
		}
		switch {
		case ft == reflect.TypeOf(Duration("")):
			fs.Kind = KindDuration
		case ft.Kind() == reflect.String:
			fs.Kind = KindString
			if opts["ident"] {
				fs.Kind = KindIdent
			}
		case ft.Kind() == reflect.Bool:
			fs.Kind = KindBool
		case ft.Kind() >= reflect.Int && ft.Kind() <= reflect.Uint64:
			fs.Kind = KindInt
		case ft.Kind() == reflect.Float32 || ft.Kind() == reflect.Float64:
			fs.Kind = KindNumber
		case ft.Kind() == reflect.Map:
			fs.Kind, fs.Items = KindMap, scalarKind(ft.Elem())
		case ft.Kind() == reflect.Slice:
			et := ft.Elem()
			if et.Kind() == reflect.Ptr {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				fs.Kind = KindBlocks
				attachNested(&fs, et, seen)
			} else {
				fs.Kind, fs.Items = KindList, scalarKind(et)
			}
		case ft.Kind() == reflect.Struct:
			fs.Kind = KindBlock
			attachNested(&fs, ft, seen)
		default:
			fs.Kind = KindAny
		}
		bs.Fields = append(bs.Fields, fs)
	}
	return bs
}

func attachNested(fs *FieldSchema, t reflect.Type, seen map[reflect.Type]bool) {
	if seen[t] {
		fs.Recursive = true
		fs.Block = &BlockSchema{GoType: t.Name(), Fields: []FieldSchema{}}
		return
	}
	nested := buildBlockSchema(t, seen)
	fs.Block = &nested
}

func scalarKind(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeOf(Duration("")):
		return KindDuration
	case t.Kind() == reflect.String:
		return KindString
	case t.Kind() == reflect.Bool:
		return KindBool
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Uint64:
		return KindInt
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		return KindNumber
	case t.Kind() == reflect.Struct:
		return KindBlock
	}
	return KindAny
}

// AttachBlockDocs fills Doc on the schemas returned by BlockSchemas from the Go
// doc comments of the spec structs. Doc comments exist only in source, so the
// caller names directories holding it (for a checkout: "platform", "pipeline");
// it is a development-time aid for an editor server, not something a deployed
// binary can do. Unreadable directories are skipped. It must be called before
// the schemas are shared between goroutines.
func AttachBlockDocs(dirs ...string) {
	typeDocs := map[string]string{}
	fieldDocs := map[string]map[string]string{} // go type -> bcl field name -> doc
	fset := token.NewFileSet()
	docsFromDirs(fset, dirs, typeDocs, fieldDocs)
	var apply func(b *BlockSchema)
	apply = func(b *BlockSchema) {
		if d := typeDocs[b.GoType]; d != "" && b.Doc == "" {
			b.Doc = d
		}
		for i := range b.Fields {
			f := &b.Fields[i]
			if d := fieldDocs[b.GoType][f.Name]; d != "" && f.Doc == "" {
				f.Doc = d
			}
			if f.Block != nil {
				apply(f.Block)
			}
		}
	}
	m := BlockSchemas()
	for k, b := range m {
		apply(&b)
		m[k] = b
	}
}

func docsFromDirs(fset *token.FileSet, dirs []string, typeDocs map[string]string, fieldDocs map[string]map[string]string) {
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, dir, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		for _, pkg := range pkgs {
			for fname, file := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				for _, decl := range file.Decls {
					gd, ok := decl.(*ast.GenDecl)
					if !ok || gd.Tok != token.TYPE {
						continue
					}
					for _, spec := range gd.Specs {
						ts := spec.(*ast.TypeSpec)
						st, ok := ts.Type.(*ast.StructType)
						if !ok {
							continue
						}
						doc := ts.Doc
						if doc == nil {
							doc = gd.Doc
						}
						if doc != nil {
							typeDocs[ts.Name.Name] = strings.TrimSpace(doc.Text())
						}
						for _, f := range st.Fields.List {
							if f.Tag == nil || f.Doc == nil {
								continue
							}
							tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`"))
							bt, ok := tag.Lookup("bcl")
							if !ok {
								continue
							}
							name := strings.Split(bt, ",")[0]
							if name == "" {
								continue
							}
							if fieldDocs[ts.Name.Name] == nil {
								fieldDocs[ts.Name.Name] = map[string]string{}
							}
							fieldDocs[ts.Name.Name][name] = strings.TrimSpace(f.Doc.Text())
						}
					}
				}
			}
		}
	}
}

// SortedBlockNames lists the top-level block keywords in order.
func SortedBlockNames() []string {
	names := make([]string, 0, len(BlockSchemas()))
	for n := range BlockSchemas() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
