package main

import (
	"fmt"
	"os"

	"atoman/internal/modules/reference"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	if err := godotenv.Load(".env.prod"); err != nil {
		panic(err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{})
	if err != nil {
		panic(err)
	}
	viewer := reference.Viewer{UserID: uuid.MustParse("019f5eb9-285b-7a06-ba5f-034975eab083")}
	for _, targetType := range []string{"post", "short_note", "feed", "artist", "album"} {
		items, err := reference.NewRegistry(db).Search(viewer, targetType, "ye", 2)
		if err != nil {
			fmt.Printf("%s ERROR %v\n", targetType, err)
			continue
		}
		fmt.Printf("%s OK %d\n", targetType, len(items))
	}
}
