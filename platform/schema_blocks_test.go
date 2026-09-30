package platform

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Every `,block` field of Document must appear as a top-level schema.
func TestBlockSchemasCoverDocument(t *testing.T) {
	schemas := BlockSchemas()
	dt := reflect.TypeOf(Document{})
	want := 0
	for i := 0; i < dt.NumField(); i++ {
		name, opts, ok := parseBCLTag(dt.Field(i))
		if !ok || !opts["block"] {
			continue
		}
		want++
		if _, ok := schemas[name]; !ok {
			t.Errorf("top-level block %q missing from BlockSchemas", name)
		}
	}
	if len(schemas) != want {
		t.Errorf("BlockSchemas has %d blocks, Document declares %d", len(schemas), want)
	}
	for _, n := range []string{"route", "resource", "intent", "entity", "worker", "schedule", "trigger", "static", "flag", "shape", "role", "process", "pipeline"} {
		if _, ok := schemas[n]; !ok {
			t.Errorf("expected block %q", n)
		}
	}
}

func TestRouteBlockSchema(t *testing.T) {
	route := BlockSchemas()["route"]
	if !route.HasID || route.GoType != "RouteSpec" || route.Name != "route" {
		t.Fatalf("route: %+v", route)
	}
	kinds := map[string]string{"method": KindIdent, "path": KindString, "intent": KindString,
		"allow_anonymous": KindBool, "status": KindInt, "timeout": KindDuration,
		"headers": KindMap, "tags": KindList, "parameter": KindBlocks, "authz": KindBlock, "rate_limit": KindBlock}
	for name, kind := range kinds {
		f, ok := route.Field(name)
		if !ok {
			t.Errorf("route has no field %q", name)
			continue
		}
		if f.Kind != kind {
			t.Errorf("route.%s kind = %s, want %s", name, f.Kind, kind)
		}
	}
	authz, _ := route.Field("authz")
	if authz.Block == nil || !authz.Optional || len(authz.Block.Fields) == 0 {
		t.Fatalf("authz nested schema not populated: %+v", authz)
	}
	if _, ok := authz.Block.Field("roles"); !ok {
		t.Errorf("authz should have roles: %+v", authz.Block.Fields)
	}
	rl, _ := route.Field("rate_limit")
	if rl.Block == nil || len(rl.Block.Fields) == 0 {
		t.Fatalf("rate_limit nested schema not populated: %+v", rl)
	}
	if _, ok := route.Field("name"); ok {
		t.Error("the ,id field must not be listed as a regular field")
	}
}

func TestBlockSchemasJSONAndDocs(t *testing.T) {
	raw, err := BlockSchemasJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]BlockSchema
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back["route"].Fields[0].Name, BlockSchemas()["route"].Fields[0].Name) {
		t.Fatal("JSON round trip changed the schema")
	}
	AttachBlockDocs(".", "../pipeline")
	route := BlockSchemas()["route"]
	if route.Doc == "" {
		t.Error("route has no doc after AttachBlockDocs")
	}
	if f, _ := route.Field("session"); f.Doc == "" {
		t.Error("route.session has no doc after AttachBlockDocs")
	}
}
