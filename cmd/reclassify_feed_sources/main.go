package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/feedclass"

	"github.com/joho/godotenv"
)

type bucket struct {
	from  string
	to    string
	count int
}

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load")
	apply := flag.Bool("apply", false, "persist category changes")
	limit := flag.Int("limit", 30, "max sample rows to print")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}

	dbType := os.Getenv("DATABASE_TYPE")
	dbURL := os.Getenv("DATABASE_URL")
	if dbType == "" || dbURL == "" {
		log.Fatal("DATABASE_TYPE and DATABASE_URL are required")
	}

	db, err := app.OpenDB(config.DBConfig{Type: dbType, URL: dbURL})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	changes, err := feedclass.CollectChanges(db)
	if err != nil {
		log.Fatalf("collect changes: %v", err)
	}

	fmt.Printf("dry_run=%t total_changes=%d\n", !*apply, len(changes))
	printSummary(changes)
	printSamples(changes, *limit)

	if !*apply {
		return
	}

	if err := feedclass.ApplyChanges(db, changes); err != nil {
		log.Fatalf("apply changes: %v", err)
	}
	fmt.Println("apply=done")
}

func printSummary(changes []feedclass.Change) {
	counts := map[string]int{}
	for _, change := range changes {
		key := fmt.Sprintf("%s -> %s", blankToUncategorized(change.Current), change.Next)
		counts[key]++
	}

	rows := make([]bucket, 0, len(counts))
	for key, count := range counts {
		parts := strings.SplitN(key, " -> ", 2)
		rows = append(rows, bucket{from: parts[0], to: parts[1], count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count == rows[j].count {
			if rows[i].from == rows[j].from {
				return rows[i].to < rows[j].to
			}
			return rows[i].from < rows[j].from
		}
		return rows[i].count > rows[j].count
	})

	fmt.Println("summary:")
	for _, row := range rows {
		fmt.Printf("  %s -> %s : %d\n", row.from, row.to, row.count)
	}
}

func printSamples(changes []feedclass.Change, limit int) {
	if limit <= 0 || len(changes) == 0 {
		return
	}
	fmt.Println("samples:")
	for index, change := range changes {
		if index >= limit {
			break
		}
		fmt.Printf("  [%d] %s | %s -> %s\n", index+1, change.Title, blankToUncategorized(change.Current), change.Next)
		if change.RSSURL != "" {
			fmt.Printf("      rss: %s\n", change.RSSURL)
		}
		for _, link := range change.RecentLinks {
			fmt.Printf("      link: %s\n", link)
		}
	}
}

func blankToUncategorized(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	return value
}
