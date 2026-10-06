package main

import (
	"context"
	"log"
	"os"

	"github.com/oarkflow/ref/examples/medical-coding-platform/internal/bootstrap"
	"github.com/oarkflow/ref/examples/medical-coding-platform/internal/migrations"
)

func main() {
	ctx := context.Background()
	if err := bootstrap.LoadDotenv(bootstrap.DotenvPath()); err != nil {
		log.Fatalf("migrator: loading %s: %v", bootstrap.DotenvPath(), err)
	}

	mgr, _, err := migrations.NewManager(ctx, migrations.Config{
		Driver:       env("DB_DRIVER", "sqlite"),
		DSN:          env("DB_DSN", "file:.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"),
		MigrationDir: bootstrap.ResolveDir("examples/medical-coding-platform/resources/migrations", "./resources/migrations", "resources/migrations"),
		SeedDir:      bootstrap.ResolveDir("examples/medical-coding-platform/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds"),
	})
	if err != nil {
		log.Fatalf("migrator: %v", err)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "seed", "db:seed":
			if err := migrations.Seed(ctx, mgr); err != nil {
				log.Fatalf("migrator seed: %v", err)
			}
			log.Println("seeds applied successfully")
			return
		case "migrate", "up":
			if err := migrations.Apply(ctx, mgr); err != nil {
				log.Fatalf("migrator apply: %v", err)
			}
			log.Println("migrations applied successfully")
			return
		}
	}

	mgr.Run(ctx)
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
