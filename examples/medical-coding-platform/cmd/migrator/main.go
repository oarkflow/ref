package main

import (
	"context"
	"log"
	"os"

	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/contrib/migrate"
)

func main() {
	ctx := context.Background()
	if err := config.LoadDotenv(config.DotenvPath()); err != nil {
		log.Fatalf("migrator: loading %s: %v", config.DotenvPath(), err)
	}

	mgr, _, err := migrate.NewManager(ctx, migrate.Config{
		Driver:       config.Env("DB_DRIVER", "sqlite"),
		DSN:          config.Env("DB_DSN", "file:.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"),
		MigrationDir: config.ResolveDir("examples/medical-coding-platform/resources/migrations", "./resources/migrations", "resources/migrations"),
		SeedDir:      config.ResolveDir("examples/medical-coding-platform/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds"),
	})
	if err != nil {
		log.Fatalf("migrator: %v", err)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "seed", "db:seed":
			if err := migrate.Seed(ctx, mgr); err != nil {
				log.Fatalf("migrator seed: %v", err)
			}
			log.Println("seeds applied successfully")
			return
		case "migrate", "up":
			if err := migrate.Apply(ctx, mgr); err != nil {
				log.Fatalf("migrator apply: %v", err)
			}
			log.Println("migrations applied successfully")
			return
		}
	}

	mgr.Run(ctx)
}
