// Command migrate applies (or rolls back) goose migrations against
// DATABASE_URL. Kept separate from cmd/api: migrations run once per
// deploy, not on every process start, and shouldn't race N replicas
// applying them concurrently.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/fallra1n/tvpoll/migrations"
)

func main() {
	dir := "up"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("set dialect: %v", err)
	}

	switch dir {
	case "up":
		err = goose.Up(db, ".")
	case "down":
		err = goose.Down(db, ".")
	case "status":
		err = goose.Status(db, ".")
	default:
		log.Fatalf("unknown command %q (expected up|down|status)", dir)
	}
	if err != nil {
		log.Fatalf("goose %s: %v", dir, err)
	}
	fmt.Printf("goose %s: ok\n", dir)
}
