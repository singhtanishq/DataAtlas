// Command migrate applies or reverts DataAtlas control-plane database
// migrations. Usage:
//
//	go run ./cmd/migrate up        # apply all pending migrations
//	go run ./cmd/migrate down [N]  # revert the last N migrations (default 1)
//	go run ./cmd/migrate status    # list applied and pending migrations
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgresql://dataatlas:dataatlas@localhost:5432/dataatlas"
	}

	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fatal("connect: %v", err)
	}
	defer pool.Close()

	switch command {
	case "up":
		applied, err := db.Migrate(ctx, pool)
		if err != nil {
			fatal("%v", err)
		}
		if len(applied) == 0 {
			fmt.Println("database is up to date")
		}
		for _, m := range applied {
			fmt.Printf("applied %03d_%s\n", m.Version, m.Name)
		}
	case "down":
		n := 1
		if len(os.Args) > 2 {
			parsed, err := strconv.Atoi(os.Args[2])
			if err != nil || parsed < 1 {
				fatal("invalid count %q", os.Args[2])
			}
			n = parsed
		}
		reverted, err := db.Rollback(ctx, pool, n)
		if err != nil {
			fatal("%v", err)
		}
		for _, m := range reverted {
			fmt.Printf("reverted %03d_%s\n", m.Version, m.Name)
		}
	case "status":
		applied, pending, err := db.Status(ctx, pool)
		if err != nil {
			fatal("%v", err)
		}
		for _, m := range applied {
			fmt.Printf("applied  %03d_%s\n", m.Version, m.Name)
		}
		for _, m := range pending {
			fmt.Printf("pending  %03d_%s\n", m.Version, m.Name)
		}
	default:
		fatal("unknown command %q (want up|down|status)", command)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "migrate: "+format+"\n", args...)
	os.Exit(1)
}
