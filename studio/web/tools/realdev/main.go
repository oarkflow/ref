// Command realdev runs the REAL studio/server (not the mock) against the
// starter's config and resources, mounted at the root so `vite` can proxy /api
// to it. It exists to build and check UI that needs the full server surface
// (page templates, assets, journeys, linkage warnings) offline. Preview is not
// wired here (those endpoints answer 501); use the starter binary for that.
//
//	go run ./studio/web/tools/realdev -addr 127.0.0.1:8790
//
// Tokens are the same as the mock: editor-token-000001, reviewer-token-0001,
// admin-token-0000001, viewer-token-00000001.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	studioserver "github.com/oarkflow/ref/studio/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8790", "listen address")
	cfgDir := flag.String("config", "examples/starter/resources/config", "BCL config directory")
	resDir := flag.String("resources", "examples/starter/resources", "resources directory (templates/, static/)")
	flag.Parse()

	opts := platform.LoadOptions{AllowEnv: true}
	mgr := &deploy.Manager{
		Store: deploy.NewMemoryStore(), App: "starter", Secret: []byte("dev-secret-dev-secret-dev-000000"), Approvals: 1,
		Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
			return platform.Validate(ctx, src, *cfgDir, opts)
		},
		ValidateBundle: func(ctx context.Context, b platform.Bundle) platform.ValidationReport {
			return platform.ValidateBundle(ctx, b, *cfgDir, opts)
		},
	}
	if b, err := platform.ReadBundleDir(*cfgDir); err == nil {
		ctx := context.Background()
		if r, err := mgr.ProposeBundle(ctx, b, "system", "boot revision"); err == nil {
			if _, err = mgr.Approve(ctx, r.ID, "bootstrap", "seed"); err == nil {
				_, _ = mgr.Activate(ctx, r.ID, "bootstrap")
			}
		}
	}
	srv, err := studioserver.New(studioserver.Config{
		App: "starter", Manager: mgr, ConfigDir: *cfgDir, ResourcesDir: *resDir, LoadOptions: opts,
		Tokens: map[string]studio.Identity{
			"editor-token-000001":   {Name: "alice", Roles: []string{"editor"}},
			"reviewer-token-0001":   {Name: "bob", Roles: []string{"reviewer"}},
			"admin-token-0000001":   {Name: "root", Roles: []string{"admin"}},
			"viewer-token-00000001": {Name: "vera", Roles: []string{"viewer"}},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("real studio server on http://%s (config %s, resources %s)", *addr, *cfgDir, *resDir)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
