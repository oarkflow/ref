// Package studio holds the types shared by Studio's packages (server, preview,
// model). It contains no logic, so any of them can import it without cycles.
//
// This file is deliberately minimal and named types.go so it merges easily with
// the fuller studio.go the core backend may add.
package studio

import "github.com/oarkflow/ref/platform"

// Identity is who is calling: a name and the roles their token carries.
type Identity struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// Draft is a working copy of an application's BCL: a bundle plus a version that
// bumps on every change.
type Draft interface {
	ID() string
	Version() int64
	// Bundle returns a snapshot; later edits do not change it.
	Bundle() platform.Bundle
	// Subscribe delivers the new version after each change. The returned func
	// stops the subscription.
	Subscribe() (<-chan int64, func())
}

// AssetDraft is an optional interface a Draft may also implement when it
// carries page templates and static files (paths under "templates/" and
// "static/") next to its BCL. Preview hands them to the host so a draft's
// pages render with the draft's templates. Anything that changes the assets
// must also bump Version, or the preview will not rebuild.
type AssetDraft interface {
	Draft
	// Assets returns a snapshot; later edits do not change it.
	Assets() platform.Assets
}

// DraftAssets returns d's assets, or nil when d carries none.
func DraftAssets(d Draft) platform.Assets {
	if ad, ok := d.(AssetDraft); ok {
		return ad.Assets()
	}
	return nil
}

// DraftSource looks drafts up by id.
type DraftSource interface {
	Get(id string) (Draft, bool)
}
